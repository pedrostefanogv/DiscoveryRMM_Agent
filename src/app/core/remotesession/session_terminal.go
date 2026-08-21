//go:build windows

package remotesession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
)

// ── TerminalSession ──

// TerminalSession representa o console remoto unico de uma sessao.
// Ao contrario do design antigo (multi-abas), mantemos UM console por sessao,
// usando subjects fixos (term.out / term.in), similar ao MeshCentral.
type TerminalSession struct {
	ID        string
	Shell     *terminal.ConPTYShell
	ShellKind terminal.ShellKind
	Cols      int
	Rows      int
	stopCh    chan struct{}
}

// ── OutputCoalescer ──
// Junta chunks consecutivos de output do terminal em uma so mensagem,
// reduzindo NATS publishes em ~90% para comandos com saida rapida (ex: dir, logs).

type outputCoalescer struct {
	mu       sync.Mutex
	buf      strings.Builder
	timer    *time.Timer
	onFlush  func(string)
	interval time.Duration

	// Rate limiting — janela deslizante
	msgTimestamps []time.Time
	maxPerWindow  int
	windowMs      time.Duration
}

func newOutputCoalescer(onFlush func(string), interval time.Duration) *outputCoalescer {
	return &outputCoalescer{
		onFlush:       onFlush,
		interval:      interval,
		maxPerWindow:  termMaxMsgPerSec * termRateWindowMs / 1000,
		windowMs:      termRateWindowMs,
		msgTimestamps: make([]time.Time, 0, 16),
	}
}

func (oc *outputCoalescer) Write(s string) {
	oc.mu.Lock()
	oc.buf.WriteString(s)
	if oc.timer == nil {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
	oc.mu.Unlock()
}

func (oc *outputCoalescer) flush() {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.buf.Len() > 0 {
		if oc.allowMessage() {
			oc.onFlush(oc.buf.String())
			oc.buf.Reset()
		}
		// Se o rate limit foi atingido, NÃO descarta o buffer: mantém o
		// conteúdo acumulado para ser enviado no próximo flush (o timer
		// é re-agendado abaixo). Isso evita perda de output em comandos
		// com saída volumosa (dir /s, Get-ChildItem -Recurse, logs).
	}
	oc.timer = nil
	// Re-agenda o flush se ainda há dados pendentes (rate limit bloqueou).
	if oc.buf.Len() > 0 {
		oc.timer = time.AfterFunc(oc.interval, oc.flush)
	}
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

// ForceFlush esvazia o buffer imediatamente (chamado no shutdown).
func (oc *outputCoalescer) ForceFlush() {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.timer != nil {
		oc.timer.Stop()
		oc.timer = nil
	}
	if oc.buf.Len() > 0 {
		oc.onFlush(oc.buf.String())
		oc.buf.Reset()
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

	// readyPayload é republicado no primeiro term.in (handshake) para o viewer
	// não perder o term.ready (NATS core é fire-and-forget).
	readyPayload []byte

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
		} else {
			log.Printf("[session-terminal] term.out publicado: session=%s seq=%d bytes=%d\n",
				st.sessionID, currentSeq, len(output))
		}

		// Gravação (thread-safe)
		if st.recordingTap != nil {
			st.recordingTap.Write(encoded, currentSeq)
		}
	}, termOutputCoalesceMs*time.Millisecond)

	shell, err := terminal.NewConPTYShell(shellKind, cols, rows, func(output string) {
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

		log.Printf("[session-terminal] shell saiu: shell=%s motivo=%s hasOutput=%v",
			shellKind, exitMsg, hasOutput)
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

		// Handshake: republica o term.ready no primeiro term.in (qualquer tipo),
		// para o viewer não perder o ready (NATS core é fire-and-forget).
		st.mu.RLock()
		rp := st.readyPayload
		st.mu.RUnlock()
		if len(rp) > 0 {
			_ = st.natsStream.PublishTermOut(st.sessionID, string(rp))
			st.mu.Lock()
			st.readyPayload = nil // só republica uma vez
			st.mu.Unlock()
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

	log.Printf("[session-terminal] console criado: shell=%s cols=%d rows=%d coalesce=%dms",
		shellKind, cols, rows, termOutputCoalesceMs)

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
