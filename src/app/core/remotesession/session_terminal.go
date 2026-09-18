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
)

// ── TerminalSession ──

// TerminalSession representa o console remoto unico de uma sessao.
// Ao contrario do design antigo (multi-abas), mantemos UM console por sessao,
// usando subjects fixos (term.out / term.in), similar ao MeshCentral.
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
	// Teto de buffer: acima do limite, despacha imediatamente (ignora os
	// guards de adiamento) — memória/lag não podem crescer indefinidamente.
	if len(oc.buf) >= maxCoalesceBufferBytes {
		oc.dispatchLocked()
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
	defer st.mu.Unlock()
	st.readyPayload = payload
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
// (term.out para saida, term.in para entrada), similar ao MeshCentral.
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

	term := &TerminalSession{
		ID:        "main",
		ShellKind: shellKind,
		Cols:      cols,
		Rows:      rows,
		stopCh:    make(chan struct{}),
	}

	var seq int64
	var seqMu sync.Mutex

	// Coalescer: junta chunks consecutivos em uma so mensagem
	coalescer := newOutputCoalescer(func(output string) {
		seqMu.Lock()
		currentSeq := seq
		seq++
		seqMu.Unlock()

		encoded := base64.StdEncoding.EncodeToString([]byte(output))
		payload, _ := json.Marshal(map[string]any{
			"data": encoded,
			"seq":  currentSeq,
		})

		// Subject fixo term.out (console unico)
		if err := st.natsStream.PublishTermOut(st.sessionID, string(payload)); err != nil {
			log.Printf("[session-terminal] erro ao publicar term.out: %v", err)
		}
		// (Log de sucesso por mensagem removido — dezenas de linhas/segundo em
		// saída intensa; erro continua logado acima.)

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
		exitSeq := seq
		seqMu.Unlock()

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
		st.natsStream.PublishTermOut(st.sessionID, string(exitPayload))

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
			Data string `json:"data"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			log.Printf("[session-terminal] term.in JSON invalido: %v\n", err)
			return
		}

		// Handshake: republica o term.ready no primeiro term.in (qualquer
		// tipo) e em CADA resize. Antes era one-shot (payload nil'd após o
		// 1º envio) — em reconexão do viewer o ready nunca mais voltava e o
		// terminal ficava sem shells/dimensões ("morto"). Resize é raro, e o
		// viewer sempre envia o fit logo após conectar — cobre reconexões.
		isResize := req.Cols > 0 && req.Rows > 0
		st.mu.Lock()
		rp := st.readyPayload
		firstIn := !st.readySent
		if len(rp) > 0 && (firstIn || isResize) {
			st.readySent = true
		} else {
			rp = nil
		}
		st.mu.Unlock()
		if len(rp) > 0 {
			_ = st.natsStream.PublishTermOut(st.sessionID, string(rp))
		}

		// Resize se dimensoes informadas
		if req.Cols > 0 && req.Rows > 0 {
			log.Printf("[session-terminal] term.in resize: session=%s cols=%d rows=%d\n",
				st.sessionID, req.Cols, req.Rows)
			_ = shell.Resize(req.Cols, req.Rows)
			term.Cols = req.Cols
			term.Rows = req.Rows
			return
		}

		// Decodificar input base64 (com limitacao de tamanho)
		if req.Data != "" && len(req.Data) <= termMaxInputSize {
			decoded, err := base64.StdEncoding.DecodeString(req.Data)
			if err != nil {
				log.Printf("[session-terminal] term.in base64 invalido: %v\n", err)
				return
			}
			log.Printf("[session-terminal] term.in input: session=%s bytes=%d\n",
				st.sessionID, len(decoded))
			_ = shell.WriteStdin(string(decoded))
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
