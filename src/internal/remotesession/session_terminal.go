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

	"github.com/google/uuid"

	"discovery/internal/terminal"
)

// ── Constantes de timing do terminal ──

const (
	termOutputCoalesceMs = 16  // debounce de coalescimento (~60Hz)
	termMaxMsgPerSec     = 60  // rate limit maximo de mensagens/segundo
	termRateWindowMs     = 100 // janela deslizante para rate limit
	termMaxInputSize     = 4 * 1024 // limite de input por mensagem (4KB)
)

// ── TerminalTab ──

// TerminalTab representa uma aba de terminal individual dentro da sessao.
type TerminalTab struct {
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
		}
		oc.buf.Reset()
	}
	oc.timer = nil
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

// ── SessionTerminal ──

// SessionTerminal gerencia uma sessao de terminal com multiplas abas.
type SessionTerminal struct {
	sessionID  string
	natsStream *NatsStreamHandler
	tabs       map[string]*TerminalTab

	recordingEnabled bool
	recordingTap     *RecordingTap

	stopCh chan struct{}
	doneCh chan struct{}
	mu     sync.RWMutex
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
	TabID       string `json:"tabId"`
	Data        string `json:"data"`
	Seq         int64  `json:"seq"`
	TimestampMs int64  `json:"timestampMs"`
}

// Write grava um frame de terminal, thread-safe.
func (rt *RecordingTap) Write(tabID string, data string, seq int64) {
	rt.mu.Lock()
	if !rt.enabled {
		rt.mu.Unlock()
		return
	}
	rt.mu.Unlock()

	frame := TermRecordingFrame{
		TabID:       tabID,
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

// ── NewSessionTerminal ──

// NewSessionTerminal cria um novo gerenciador de sessao de terminal.
func NewSessionTerminal(sessionID string, natsStream *NatsStreamHandler, recordingEnabled bool) *SessionTerminal {
	st := &SessionTerminal{
		sessionID:  sessionID,
		natsStream: natsStream,
		tabs:       make(map[string]*TerminalTab),
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

// ── CreateTab ──

// CreateTab cria uma nova aba de terminal com output coalescing e rate limiting.
func (st *SessionTerminal) CreateTab(ctx context.Context, shellKind terminal.ShellKind, cols, rows int) (*TerminalTab, error) {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	tabID := uuid.New().String()
	tab := &TerminalTab{
		ID:        tabID,
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

		if err := st.natsStream.PublishTermOutTab(st.sessionID, tabID, string(payload)); err != nil {
			log.Printf("[session-terminal] erro ao publicar term.out.%s: %v", tabID, err)
		}

		// Gravação (thread-safe)
		if st.recordingTap != nil {
			st.recordingTap.Write(tabID, encoded, currentSeq)
		}
	}, termOutputCoalesceMs*time.Millisecond)

	shell, err := terminal.NewConPTYShell(shellKind, cols, rows, func(output string) {
		coalescer.Write(output)
	})
	if err != nil {
		return nil, fmt.Errorf("criar shell %s: %w", shellKind, err)
	}

	tab.Shell = shell

	st.mu.Lock()
	st.tabs[tabID] = tab
	st.mu.Unlock()

	// Monitor de exit do shell — notifica viewer
	go func() {
		err := shell.Wait()
		exitMsg := "shell encerrado"
		if err != nil {
			exitMsg = err.Error()
		}
		coalescer.ForceFlush() // flush final

		// Captura o seq final de forma segura
		seqMu.Lock()
		exitSeq := seq
		seqMu.Unlock()

		log.Printf("[session-terminal] shell saiu: tab=%s shell=%s motivo=%s", tabID, shellKind, exitMsg)
		st.natsStream.PublishTermOutTab(st.sessionID, tabID,
			fmt.Sprintf(`{"data":"","seq":%d,"exit":true,"reason":"%s"}`, exitSeq, exitMsg))
	}()

	// Subscrever input do viewer para esta tab
	sub, err := st.natsStream.SubscribeToTermInTab(st.sessionID, tabID, func(data []byte) {
		var req struct {
			Data string `json:"data"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}

		// Resize se dimensoes informadas
		if req.Cols > 0 && req.Rows > 0 {
			_ = shell.Resize(req.Cols, req.Rows)
			tab.Cols = req.Cols
			tab.Rows = req.Rows
			return
		}

		// Decodificar input base64 (com limitacao de tamanho)
		if req.Data != "" && len(req.Data) <= termMaxInputSize {
			decoded, err := base64.StdEncoding.DecodeString(req.Data)
			if err != nil {
				return
			}
			_ = shell.WriteStdin(string(decoded))
		}
	})
	if err != nil {
		_ = shell.Close()
		st.mu.Lock()
		delete(st.tabs, tabID)
		st.mu.Unlock()
		return nil, fmt.Errorf("subscribe term.in.%s: %w", tabID, err)
	}

	// Cleanup da subscription quando a tab for fechada
	go func() {
		<-tab.stopCh
		_ = sub.Unsubscribe()
	}()

	log.Printf("[session-terminal] tab criada: %s shell=%s cols=%d rows=%d coalesce=%dms",
		tabID, shellKind, cols, rows, termOutputCoalesceMs)

	return tab, nil
}

// CloseTab fecha uma aba de terminal especifica.
func (st *SessionTerminal) CloseTab(tabID string) {
	st.mu.Lock()
	tab, ok := st.tabs[tabID]
	if !ok {
		st.mu.Unlock()
		return
	}
	delete(st.tabs, tabID)
	st.mu.Unlock()

	select {
	case <-tab.stopCh:
		// ja fechado
	default:
		close(tab.stopCh)
	}

	if tab.Shell != nil {
		_ = tab.Shell.Close()
	}

	log.Printf("[session-terminal] tab fechada: %s", tabID)
}

// TabCount retorna o numero de abas ativas.
func (st *SessionTerminal) TabCount() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.tabs)
}

// GetTabs retorna um snapshot das abas ativas.
func (st *SessionTerminal) GetTabs() []TerminalTab {
	st.mu.RLock()
	defer st.mu.RUnlock()
	tabs := make([]TerminalTab, 0, len(st.tabs))
	for _, t := range st.tabs {
		tabs = append(tabs, *t)
	}
	return tabs
}

// Stop fecha todas as abas e libera recursos.
func (st *SessionTerminal) Stop() {
	select {
	case <-st.stopCh:
		return
	default:
		close(st.stopCh)
	}

	st.mu.Lock()
	tabs := make([]*TerminalTab, 0, len(st.tabs))
	for id, tab := range st.tabs {
		tabs = append(tabs, tab)
		delete(st.tabs, id)
	}
	st.mu.Unlock()

	for _, tab := range tabs {
		select {
		case <-tab.stopCh:
		default:
			close(tab.stopCh)
		}
		if tab.Shell != nil {
			_ = tab.Shell.Close()
		}
	}

	if st.recordingTap != nil {
		st.recordingTap.SetEnabled(false)
	}

	log.Printf("[session-terminal] todas as tabs fechadas: session=%s", st.sessionID)
}

// Ensure imports
var _ context.Context
var _ fmt.Stringer
