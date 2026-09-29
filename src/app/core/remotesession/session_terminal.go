//go:build windows

package remotesession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/app/core/terminal"
)

// ── Constantes de timing do terminal ──

const (
	termOutputCoalesceMs = 16        // debounce de coalescimento (~60Hz)
	termMaxMsgPerSec     = 60        // rate limit maximo de mensagens/segundo
	termRateWindowMs     = 100       // janela deslizante para rate limit
	termMaxInputSize     = 32 * 1024 // limite de input por mensagem (32KB — suporta paste de textos longos)

	// Teto do buffer do coalescer: saída intensa (ex.: type de arquivo grande)
	// não pode crescer sem limite entre flushes — o rate limit segura o
	// dispatch e o buffer acumularia memória/lag indefinidamente. Ao atingir
	// o teto, o flush é imediato (o payload resultante ~700KB após base64+
	// JSON cabe folgado no max payload do NATS, 2MB — ver nats_stream.go).
	maxCoalesceBufferBytes = 512 * 1024

	// termReplayMaxBytes limita o anel de replay por sessão. O NATS core é
	// fire-and-forget: output publicado enquanto o viewer está desconectado não
	// é recebido por ninguém e se perderia para sempre (o MeshCentral contorna
	// isso derrubando o terminal junto com o WebSocket; aqui a sessão sobrevive
	// e o viewer reconecta). Guardamos os últimos frames de term.out para
	// reenviar no handshake (term.in "hello" com lastSeq).
	termReplayMaxBytes = 512 * 1024

	// Limites de dimensão aceitos do servidor. 0/negativo já vira default; aqui
	// cobrimos o extremo oposto (valor absurdo não pode ir ao ConPTY/console).
	termMinCols, termMaxCols = 20, 500
	termMinRows, termMaxRows = 5, 200
)

// clampTermDims limita cols/rows à faixa suportada. Valores fora da faixa são
// aproximados para o limite mais próximo (nunca rejeitados: o terminal precisa
// abrir).
func clampTermDims(cols, rows int) (int, int) {
	if cols < termMinCols {
		cols = termMinCols
	}
	if cols > termMaxCols {
		cols = termMaxCols
	}
	if rows < termMinRows {
		rows = termMinRows
	}
	if rows > termMaxRows {
		rows = termMaxRows
	}
	return cols, rows
}

// ── Replay sob reconexão ──
//
// Anel limitado dos últimos frames de term.out. O viewer informa no handshake
// o último seq que renderizou (lastSeq); reenviamos os frames com seq maior.
// Quando o viewer está mais antigo que o anel, não há como garantir
// continuidade: publicamos {"reset":true} para ele limpar o buffer em vez de
// emendar saída nova em saída velha (texto torto).

type termFrame struct {
	seq     int64
	payload string // JSON já pronto (data base64 + seq)
}

type termReplayRing struct {
	mu     sync.Mutex
	frames []termFrame
	bytes  int
	max    int
}

func newTermReplayRing(maxBytes int) *termReplayRing {
	return &termReplayRing{max: maxBytes}
}

func (r *termReplayRing) add(seq int64, payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, termFrame{seq: seq, payload: payload})
	r.bytes += len(payload)
	// Mantém ao menos 1 frame (um único frame maior que o teto é aceitável).
	for r.bytes > r.max && len(r.frames) > 1 {
		r.bytes -= len(r.frames[0].payload)
		r.frames = r.frames[1:]
	}
}

// since devolve os frames com seq > fromSeq em ordem. ok=false quando fromSeq é
// mais antigo que o primeiro frame retido (há lacuna — não dá para continuar).
func (r *termReplayRing) since(fromSeq int64) (frames []termFrame, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.frames) == 0 {
		return nil, true
	}
	if fromSeq < r.frames[0].seq-1 {
		return nil, false
	}
	for _, f := range r.frames {
		if f.seq > fromSeq {
			frames = append(frames, f)
		}
	}
	return frames, true
}

func (r *termReplayRing) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = nil
	r.bytes = 0
}

// ── TerminalSession ──

// TerminalSession representa o console remoto unico de uma sessao.
// Ao contrario do design antigo (multi-abas), mantemos UM console por sessao,
// usando subjects fixos (term.out / term.in).
type TerminalSession struct {
	ID        string
	Shell     terminal.IShell
	ShellKind terminal.ShellKind
	Cols      int
	Rows      int
	stopCh    chan struct{}
}

// ── OutputCoalescer ──
// Junta chunks consecutivos de output do terminal em uma so mensagem,
// reduzindo NATS publishes em ~90% para comandos com saida rapida (ex: dir, logs).

type outputCoalescer struct {
	mu sync.Mutex
	// buf acumula os chunks de output em BYTES (não string): o flush pode
	// reter o tail de uma runa UTF-8 incompleta (ver Utf8IncompleteTail) e
	// anexá-lo ao próximo chunk — impossível de fazer por ranhura em Builder.
	buf      []byte
	timer    *time.Timer
	onFlush  func(string)
	interval time.Duration

	// Rate limiting — janela deslizante
	msgTimestamps []time.Time
	maxPerWindow  int
	windowMs      time.Duration

	// Segmentation ANSI — número de ticks consecutivos adiados por sequência
	// ANSI incompleta. Limita a retenção do buffer a poucos intervalos.
	deferCount   int
	maxAnsiDefer int
}

func newOutputCoalescer(onFlush func(string), interval time.Duration) *outputCoalescer {
	return &outputCoalescer{
		onFlush:       onFlush,
		interval:      interval,
		maxPerWindow:  termMaxMsgPerSec * termRateWindowMs / 1000,
		windowMs:      termRateWindowMs,
		msgTimestamps: make([]time.Time, 0, 16),
		maxAnsiDefer:  4,
	}
}

func (oc *outputCoalescer) Write(s string) {
	oc.mu.Lock()
	oc.buf = append(oc.buf, s...)
	if oc.timer == nil {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
	// Teto de buffer: acima do limite, despacha imediatamente IGNORANDO o rate
	// limit. Com dispatchLocked puro o rate limit seguraria o flush e o buffer
	// continuaria crescendo a cada Write — o teto não valeria de fato.
	if len(oc.buf) >= maxCoalesceBufferBytes {
		oc.forceDispatchLocked()
	}
	oc.mu.Unlock()
}

// flush é agendado a cada interval enquanto houver dados. Para reduzir
// artefatos visuais de sequências ANSI/VT cortadas no meio, só despacha quando
// o buffer não termina em uma sequência de escape possivelmente incompleta
// (ex.: ESC [ com parâmetros sem o byte final). Se estiver incompleto,
// re-agenda por mais um intervalo (timeout efetivo) até o resto chegar.
func (oc *outputCoalescer) flush() {
	oc.mu.Lock()
	oc.mustDispatchLocked()
	oc.mu.Unlock()
}

// mustDispatchLocked evita dupla checagem de rate limit: se o buffer está
// "incompleto" (provavelmente meio de uma sequência ANSI), ainda despacha se o
// rate limit permitir; caso contrário segura mais um tick. Mesmo assim, há um
// teto implícito: a cada tick o buffer que acumulou é enviado inteiro, então
// nunca fica retido para sempre.
func (oc *outputCoalescer) mustDispatchLocked() {
	// Guard 1: sequência ANSI/VT possivelmente incompleta no fim — segura o
	// buffer inteiro por mais um tick (até o teto de adiamentos). Só olhamos
	// os últimos 256 bytes: sequências CSI são curtas; sem a janela, o custo
	// seria copiar até maxCoalesceBufferBytes para string a cada tick.
	tail := oc.buf
	if len(tail) > 256 {
		tail = tail[len(tail)-256:]
	}
	if len(oc.buf) > 0 && endsWithIncompleteAnsi(string(tail)) && oc.deferCount < oc.maxAnsiDefer {
		oc.deferCount++
		oc.timer = nil
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
		return
	}

	// Guard 2 (fix mojibake): runa UTF-8 incompleta no fim — despacha SOMENTE
	// o prefixo válido e retém os bytes da runa parcial para o próximo flush.
	// Sem isso, um acento dividido entre chunks chegava quebrado ao viewer
	// (TextDecoder sem stream → U+FFFD), intermitentemente em pt-BR.
	if t := terminal.Utf8IncompleteTail(oc.buf); t > 0 && t < len(oc.buf) {
		oc.dispatchPrefixLocked(len(oc.buf) - t)
		return
	}

	oc.dispatchLocked()
}

// dispatchPrefixLocked despacha os primeiros n bytes e RETÉM o restante no
// buffer (runa UTF-8 parcial aguardando o próximo chunk). Sob rate limit,
// mantém tudo e re-agenda — nenhum byte é perdido.
func (oc *outputCoalescer) dispatchPrefixLocked(n int) {
	oc.deferCount = 0
	if n <= 0 || !oc.allowMessage() {
		oc.timer = nil
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
		return
	}
	prefix := string(oc.buf[:n])
	rest := append([]byte(nil), oc.buf[n:]...)
	oc.buf = rest
	oc.onFlush(prefix)
	oc.timer = nil
	if len(oc.buf) > 0 {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
}

// forceDispatchLocked despacha o buffer INTEIRO ignorando o rate limit —
// usado quando o teto de bytes é atingido (memória não pode crescer sem
// limite). Preserva o guard de runa UTF-8: um tail incompleto fica retido para
// o próximo chunk, como em mustDispatchLocked. Nunca descarta bytes.
func (oc *outputCoalescer) forceDispatchLocked() {
	oc.deferCount = 0
	if len(oc.buf) == 0 {
		oc.timer = nil
		return
	}
	if t := terminal.Utf8IncompleteTail(oc.buf); t > 0 && t < len(oc.buf) {
		prefix := string(oc.buf[:len(oc.buf)-t])
		oc.buf = append([]byte(nil), oc.buf[len(oc.buf)-t:]...)
		oc.onFlush(prefix)
	} else {
		oc.onFlush(string(oc.buf))
		oc.buf = nil
	}
	oc.timer = nil
	if len(oc.buf) > 0 {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
}

func (oc *outputCoalescer) dispatchLocked() {
	oc.deferCount = 0 // reset ao despachar
	if len(oc.buf) > 0 {
		if oc.allowMessage() {
			oc.onFlush(string(oc.buf))
			oc.buf = nil
		}
	}
	oc.timer = nil
	// Re-agenda o flush se ainda há dados pendentes (rate limit bloqueou).
	if len(oc.buf) > 0 {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
}

// endsWithIncompleteAnsi reporta se a string termina no meio de uma sequência
// de escape ANSI/VT (EJ.: "ESC [" ou "ESC [2;3" sem o byte final em [0-9;<>]?
// [a-zA-Z]). Nesse caso esperamos mais um tick para completar, evitando que o
// xterm.js desenhe artefatos (linhas/colunas com início de cor cortado).
func endsWithIncompleteAnsi(s string) bool {
	if len(s) == 0 {
		return false
	}
	// Encontra o último byte ESC (0x1B).
	lastESC := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == 0x1B {
			lastESC = i
			break
		}
	}
	if lastESC < 0 {
		return false
	}

	rest := s[lastESC+1:]
	if len(rest) == 0 {
		// "ESC" sozinho no fim — pode ser início de uma sequência.
		return true
	}

	// Sequências CSI (ESC [ ... final em 0x40–0x7E) e OSC (ESC ] ... até BEL
	// ou ESC \) podem ficar truncadas. Importante testar '[' ANTES do bloco
	// de two-character escape (0x40–0x5F), pois '[' = 0x5B cairia nesse bloco
	// e seria tratado como "completo" erroneamente — deixando o CSI cortado.
	if rest[0] == '[' {
		// Estados: parâmetros/bytes intermediários em 0x20–0x3F, final em 0x40–0x7E.
		for i := 1; i < len(rest); i++ {
			c := rest[i]
			if c >= 0x40 && c <= 0x7E {
				return false // sequência CSI completa → ok despachar
			}
			if c < 0x20 || c > 0x3F {
				return true // byte estranho fora do esperado → incompleto/inválido
			}
		}
		return true // só parâmetros até o fim → incompleto
	}

	// Two-character escape (ESC <letra>): completa. Exclui ']' (0x5D, OSC) que
	// requer término por BEL (0x07) ou ESC '\' — tratamos um ']' truncado como
	// incompleto para não cortar uma OSC no meio.
	if rest[0] != ']' && rest[0] >= 0x40 && rest[0] <= 0x5F {
		return false
	}

	// OSC (ESC ] ...) truncada ou ESC seguido de byte fora de faixa → mantém.
	return true
}

// allowMessage verifica rate limit via janela deslizante.
func (oc *outputCoalescer) allowMessage() bool {
	now := time.Now()
	cutoff := now.Add(-oc.windowMs)

	// Remove timestamps fora da janela
	valid := oc.msgTimestamps[:0]
	for _, t := range oc.msgTimestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	oc.msgTimestamps = valid

	if len(oc.msgTimestamps) >= oc.maxPerWindow {
		return false // rate limit atingido
	}

	oc.msgTimestamps = append(oc.msgTimestamps, now)
	return true
}

// ForceFlush esvazia o buffer imediatamente (chamado no shutdown/exit do
// shell). IGNORA o rate limit de propósito: é o último flush da sessão —
// descartar o buffer aqui (comportamento antigo, quando a janela de 6
// msgs/100ms estava cheia) perdia os últimos bytes de output do terminal.
func (oc *outputCoalescer) ForceFlush() {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.timer != nil {
		oc.timer.Stop()
		oc.timer = nil
	}
	if len(oc.buf) > 0 {
		oc.onFlush(string(oc.buf))
		oc.buf = nil
	}
}

// ── RecordingTap ──

// RecordingTap captura output do terminal para gravacao.
type RecordingTap struct {
	sessionID  string
	natsStream *NatsStreamHandler
	enabled    bool
	mu         sync.Mutex
}

// TermRecordingFrame representa um frame de terminal para gravacao.
type TermRecordingFrame struct {
	Data        string `json:"data"`
	Seq         int64  `json:"seq"`
	TimestampMs int64  `json:"timestampMs"`
}

// Write grava um frame de terminal, thread-safe.
func (rt *RecordingTap) Write(data string, seq int64) {
	rt.mu.Lock()
	if !rt.enabled {
		rt.mu.Unlock()
		return
	}
	rt.mu.Unlock()

	frame := TermRecordingFrame{
		Data:        data,
		Seq:         seq,
		TimestampMs: time.Now().UnixMilli(),
	}
	payload, _ := json.Marshal(frame)
	_ = rt.natsStream.PublishRecordingTerm(rt.sessionID, payload)
}

// SetEnabled habilita/desabilita gravacao, thread-safe.
func (rt *RecordingTap) SetEnabled(v bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.enabled = v
}

// ── SessionTerminal ──

// SessionTerminal gerencia uma sessao de terminal com UM console unico.
type SessionTerminal struct {
	sessionID  string
	natsStream *NatsStreamHandler
	terminal   *TerminalSession

	recordingEnabled bool
	recordingTap     *RecordingTap

	// onExit é chamado quando o console/shell encerra (para o manager encerrar a sessão).
	onExit func(reason string)

	// readyPayload é republicado no primeiro term.in (handshake) e em CADA
	// term.in de resize, para o viewer não perder o term.ready — o NATS core
	// é fire-and-forget e o viewer pode conectar/reconectar depois do publish
	// original. Resize é raro, então republicar nele é barato; digitação
	// (data-only) NÃO republica (evita spam por tecla).
	readyPayload []byte
	readySent    bool
	// readyRequested cobre a corrida entre a assinatura de term.in (dentro de
	// Start) e o SetReadyPayload (chamado pelo manager DEPOIS de Start): se um
	// hello/resize chegar antes do payload existir, ele é publicado assim que
	// o manager o guardar — senão o viewer ficaria sem shells/dimensões.
	readyRequested bool

	// outMu serializa as publicações em term.out (coalescer, replay, erro,
	// exit). Sem ele o replay do handshake poderia intercalar com frames live e
	// o viewer renderizaria seq fora de ordem (texto torto).
	outMu sync.Mutex

	stopCh chan struct{}
	doneCh chan struct{}
	mu     sync.RWMutex
}

// SetOnExit define o callback de encerramento do console.
func (st *SessionTerminal) SetOnExit(cb func(reason string)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.onExit = cb
}

// SetReadyPayload guarda o payload do term.ready para republicar no primeiro
// term.in (handshake) — evita que o viewer perca o ready (NATS fire-and-forget).
func (st *SessionTerminal) SetReadyPayload(payload []byte) {
	st.mu.Lock()
	st.readyPayload = payload
	// Um hello/resize chegou antes do payload existir: publica agora.
	send := st.readyRequested && len(payload) > 0
	st.readyRequested = false
	st.mu.Unlock()
	if send {
		_ = st.natsStream.PublishTermOut(st.sessionID, string(payload))
	}
}

// PublishError publica um frame de erro de terminal no term.out. Usado pelo
// manager quando o console sequer chegou a iniciar: é a única forma de o
// viewer saber POR QUE o terminal veio vazio. O `.event` (usado antes) tinha a
// permissão do viewer mas nenhum assinante — a falha ficava invisível.
func (st *SessionTerminal) PublishError(code, reason string) {
	payload, _ := json.Marshal(map[string]any{
		"error":  true,
		"code":   code,
		"reason": reason,
		"seq":    int64(0),
	})
	if err := st.natsStream.PublishTermOut(st.sessionID, string(payload)); err != nil {
		log.Printf("[session-terminal] erro ao publicar frame de erro: %v", err)
	}
}

// NewSessionTerminal cria um novo gerenciador de sessao de terminal (console unico).
func NewSessionTerminal(sessionID string, natsStream *NatsStreamHandler, recordingEnabled bool) *SessionTerminal {
	st := &SessionTerminal{
		sessionID:  sessionID,
		natsStream: natsStream,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}

	if recordingEnabled {
		st.recordingTap = &RecordingTap{
			sessionID:  sessionID,
			natsStream: natsStream,
			enabled:    true,
		}
	}

	return st
}

// EnableRecording habilita gravacao dinamicamente.
func (st *SessionTerminal) EnableRecording() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.recordingTap != nil {
		st.recordingTap.SetEnabled(true)
		st.recordingEnabled = true
	}
}

// DisableRecording desabilita gravacao.
func (st *SessionTerminal) DisableRecording() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.recordingTap != nil {
		st.recordingTap.SetEnabled(false)
	}
	st.recordingEnabled = false
}

// Start inicia o console unico com output coalescing, rate limiting e subjects fixos
// (term.out para saida, term.in para entrada).
func (st *SessionTerminal) Start(ctx context.Context, shellKind terminal.ShellKind, cols, rows int) (*TerminalSession, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Se já existe um console ativo, fecha antes de recriar (substituição limpa)
	if st.terminal != nil {
		st.closeTerminalLocked()
	}

	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}
	// O servidor manda termCols/termRows sem validação; dimensão absurda não
	// pode chegar ao CreatePseudoConsole/console real.
	cols, rows = clampTermDims(cols, rows)

	term := &TerminalSession{
		ID:        "main",
		ShellKind: shellKind,
		Cols:      cols,
		Rows:      rows,
		stopCh:    make(chan struct{}),
	}

	var seq int64
	var seqMu sync.Mutex

	// nextSeq aloca o próximo seq de publicação. TODA mensagem que carrega seq
	// (output, erro, exit) passa por aqui: reutilizar o seq entre dois frames
	// faria o viewer acreditar que um frame que ele nunca recebeu já foi visto
	// — e o replay da reconexão o pularia (perda silenciosa de output).
	nextSeq := func() int64 {
		seqMu.Lock()
		s := seq
		seq++
		seqMu.Unlock()
		return s
	}

	// Anel de replay: alimentado por publishFrame, lido no handshake (hello).
	replay := newTermReplayRing(termReplayMaxBytes)

	// publishFrame publica um frame em term.out mantendo a ordem (outMu) e
	// registrando-o no anel de replay. Falha de publish NÃO impede o registro:
	// o replay do handshake é justamente a rede de recuperação.
	publishFrame := func(frameSeq int64, payload string) {
		st.outMu.Lock()
		replay.add(frameSeq, payload)
		err := st.natsStream.PublishTermOut(st.sessionID, payload)
		st.outMu.Unlock()
		if err != nil {
			log.Printf("[session-terminal] erro ao publicar term.out: %v", err)
		}
	}

	// emitError publica um erro de terminal no term.out com seq PRÓPRIO e
	// também no anel de replay: o seq é a identidade do frame, então dois frames
	// não podem compartilhar o mesmo número. Reenviar o erro no replay é
	// desejável (explica a lacuna ao operador).
	emitError := func(code, reason string) {
		errSeq := nextSeq()
		payload, _ := json.Marshal(map[string]any{
			"error":  true,
			"code":   code,
			"reason": reason,
			"seq":    errSeq,
		})
		publishFrame(errSeq, string(payload))
	}

	// Coalescer: junta chunks consecutivos em uma so mensagem
	coalescer := newOutputCoalescer(func(output string) {
		currentSeq := nextSeq()

		encoded := base64.StdEncoding.EncodeToString([]byte(output))
		payload, _ := json.Marshal(map[string]any{
			"data": encoded,
			"seq":  currentSeq,
		})

		// Subject fixo term.out (console unico)
		publishFrame(currentSeq, string(payload))
		// (Log de sucesso por mensagem removido — dezenas de linhas/segundo em
		// saída intensa; erro continua logado no publishFrame.)

		// Gravação (thread-safe)
		if st.recordingTap != nil {
			st.recordingTap.Write(encoded, currentSeq)
		}
	}, termOutputCoalesceMs*time.Millisecond)

	shell, err := terminal.NewShellInteractive(shellKind, cols, rows, func(output string) {
		coalescer.Write(output)
	})
	if err != nil {
		return nil, fmt.Errorf("criar shell %s: %w", shellKind, err)
	}

	term.Shell = shell
	st.terminal = term

	// Monitor de exit do shell — notifica viewer e encerra a sessão
	go func() {
		err := shell.Wait()
		exitMsg := "shell encerrado"
		if err != nil {
			exitMsg = err.Error()
		}
		coalescer.ForceFlush() // flush final

		// Só notifica exit se há dados pendentes ou seq > 0 (shell produziu output)
		seqMu.Lock()
		hasOutput := seq > 0
		seqMu.Unlock()
		exitSeq := nextSeq() // seq próprio: nunca colide com um erro concorrente

		// P2: loga o exit code também em hexadecimal (0xC0000142 =
		// STATUS_DLL_INIT_FAILED) para diagnóstico imediato.
		if exitCode, ok := terminal.ExitCodeOf(err); ok {
			log.Printf("[session-terminal] shell saiu: shell=%s exitCode=0x%08X (%d) hasOutput=%v",
				shellKind, uint32(exitCode), exitCode, hasOutput)
		} else {
			log.Printf("[session-terminal] shell saiu: shell=%s motivo=%s hasOutput=%v",
				shellKind, exitMsg, hasOutput)
		}
		exitPayload, _ := json.Marshal(map[string]any{
			"data":   "",
			"seq":    exitSeq,
			"exit":   true,
			"reason": exitMsg,
		})
		// Serializa com o coalescer/emitError para o exit ser o ÚLTIMO frame
		// da sessão (o ForceFlush logo acima já esvaziou o buffer).
		st.outMu.Lock()
		_ = st.natsStream.PublishTermOut(st.sessionID, string(exitPayload))
		st.outMu.Unlock()

		// Notifica o manager para encerrar a sessão (console morto)
		st.mu.RLock()
		cb := st.onExit
		st.mu.RUnlock()
		if cb != nil {
			cb(exitMsg)
		}
	}()

	// Subscrever input do viewer no subject fixo term.in
	sub, err := st.natsStream.SubscribeToTermIn(st.sessionID, func(data []byte) {
		var req struct {
			Data    string `json:"data"`
			Cols    int    `json:"cols"`
			Rows    int    `json:"rows"`
			Hello   bool   `json:"hello"`
			LastSeq *int64 `json:"lastSeq"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			log.Printf("[session-terminal] term.in JSON invalido: %v\n", err)
			return
		}

		// Handshake explícito do viewer logo após o +OK do NATS. O ready é
		// publicado UMA vez no start da sessão: se o viewer assinar depois (ou
		// reconectar) ele nunca mais voltava — terminal sem lista de shells,
		// sem banner de backend e sem dimensões ("morto"). O hello cobre isso
		// de forma determinística, sem depender de haver resize nem de o
		// usuário digitar. (Havia também a república no resize e no 1º term.in;
		// o hello elimina a corrida entre assinatura e publish.)
		isHello := req.Hello || (req.Data == "" && req.Cols <= 0 && req.Rows <= 0)
		isResize := req.Cols > 0 && req.Rows > 0

		st.mu.Lock()
		rp := st.readyPayload
		switch {
		case len(rp) == 0:
			// Start ainda não guardou o payload (corrida entre assinar term.in
			// e SetReadyPayload): marca para publicar assim que existir.
			st.readyRequested = true
			rp = nil
		case isHello || isResize || !st.readySent:
			st.readySent = true
		default:
			rp = nil
		}
		st.mu.Unlock()
		if len(rp) > 0 {
			_ = st.natsStream.PublishTermOut(st.sessionID, string(rp))
		}

		// Replay do que o viewer perdeu enquanto o WebSocket esteve caído.
		// Serializado por outMu com os frames live: sem isso o viewer poderia
		// receber um seq novo antes de um antigo e renderizar fora de ordem.
		if isHello && req.LastSeq != nil {
			st.outMu.Lock()
			frames, ok := replay.since(*req.LastSeq)
			if ok {
				for _, f := range frames {
					if err := st.natsStream.PublishTermOut(st.sessionID, f.payload); err != nil {
						log.Printf("[session-terminal] erro ao reenviar term.out: %v", err)
					}
				}
			} else {
				// Lacuna maior que o anel retido: manda o viewer LIMPAR o buffer
				// para não emendar saída nova em saída velha. NÃO enviamos um
				// "lastSeq" novo: o viewer só pode assumir continuidade até o
				// último seq que ELE recebeu. Adotar outro valor (em especial o
				// seq corrente, ainda não publicado) faria o replay seguinte
				// pular um frame nunca recebido — perda silenciosa de output.
				reset, _ := json.Marshal(map[string]any{
					"reset":         true,
					"requestedFrom": *req.LastSeq,
				})
				_ = st.natsStream.PublishTermOut(st.sessionID, string(reset))
				log.Printf("[session-terminal] replay indisponivel (lastSeq=%d): enviando reset", *req.LastSeq)
			}
			st.outMu.Unlock()
		}

		// Resize se dimensoes informadas. Num hello com dimensões seguimos
		// adiante (o input vem em mensagem separada e é vazio); num resize
		// puro encerramos aqui, como antes.
		if isResize {
			_ = shell.Resize(req.Cols, req.Rows)
			term.Cols = req.Cols
			term.Rows = req.Rows
			if !isHello {
				return
			}
		}

		if req.Data == "" {
			return
		}

		// Um único PUB acima do teto é REJEITADO com aviso — antes era
		// descartado em silêncio (parecia "o terminal engoliu o paste"). O
		// front fatia pastes grandes em vários PUBs abaixo deste limite.
		if len(req.Data) > termMaxInputSize {
			log.Printf("[session-terminal] term.in acima do limite: %d bytes base64 (max %d)",
				len(req.Data), termMaxInputSize)
			emitError("input_too_large",
				fmt.Sprintf("entrada de %d bytes excede o limite de %d", len(req.Data), termMaxInputSize))
			return
		}

		decoded, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			log.Printf("[session-terminal] term.in base64 invalido: %v\n", err)
			return
		}
		if err := shell.WriteStdin(string(decoded)); err != nil {
			// Fila de input cheia / shell fechado: nunca descartar em silêncio.
			log.Printf("[session-terminal] WriteStdin falhou: %v", err)
			emitError("stdin_rejected", err.Error())
		}
	})
	if err != nil {
		_ = shell.Close()
		st.terminal = nil
		return nil, fmt.Errorf("subscribe term.in: %w", err)
	}

	// Cleanup da subscription quando o console for encerrado
	go func() {
		<-term.stopCh
		_ = sub.Unsubscribe()
	}()

	log.Printf("[session-terminal] console criado: shell=%s backend=%s cols=%d rows=%d coalesce=%dms",
		shellKind, terminal.ShellBackendName(shell), cols, rows, termOutputCoalesceMs)

	return term, nil
}

// closeTerminalLocked fecha o console atual (assume st.mu travado).
func (st *SessionTerminal) closeTerminalLocked() {
	term := st.terminal
	st.terminal = nil
	if term == nil {
		return
	}

	select {
	case <-term.stopCh:
		// ja fechado
	default:
		close(term.stopCh)
	}

	if term.Shell != nil {
		_ = term.Shell.Close()
	}

	log.Printf("[session-terminal] console encerrado: session=%s", st.sessionID)
}

// CloseTerminal fecha o console atual.
func (st *SessionTerminal) CloseTerminal() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.closeTerminalLocked()
}

// HasTerminal indica se há um console ativo.
func (st *SessionTerminal) HasTerminal() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.terminal != nil
}

// GetTerminal retorna o console ativo (ou nil).
func (st *SessionTerminal) GetTerminal() *TerminalSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.terminal
}

// Stop encerra o console e libera recursos.
func (st *SessionTerminal) Stop() {
	select {
	case <-st.stopCh:
		return
	default:
		close(st.stopCh)
	}

	st.mu.Lock()
	st.closeTerminalLocked()
	st.mu.Unlock()

	if st.recordingTap != nil {
		st.recordingTap.SetEnabled(false)
	}

	log.Printf("[session-terminal] console encerrado: session=%s", st.sessionID)
}

// Ensure imports
var _ context.Context
