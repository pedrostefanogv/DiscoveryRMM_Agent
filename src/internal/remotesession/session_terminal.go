package remotesession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"discovery/internal/terminal"
)

// TerminalTab representa uma aba de terminal individual dentro da sessao.
type TerminalTab struct {
	ID        string              // UUID para isolamento de subjects
	Shell     *terminal.ConPTYShell
	ShellKind terminal.ShellKind
	Cols      int
	Rows      int
	stopCh    chan struct{}
}

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

// RecordingTap captura output do terminal para gravacao.
type RecordingTap struct {
	sessionID  string
	natsStream *NatsStreamHandler
	enabled    bool
	mu         sync.Mutex
}

// TermRecordingFrame representa um frame de terminal para gravacao.
type TermRecordingFrame struct {
	TabID      string `json:"tabId"`
	Data       string `json:"data"`
	Seq        int64  `json:"seq"`
	TimestampMs int64 `json:"timestampMs"`
}

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

// CreateTab cria uma nova aba de terminal.
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

	shell, err := terminal.NewConPTYShell(shellKind, cols, rows, func(output string) {
		// Codificar saida como base64 para transporte seguro via JSON
		encoded := base64.StdEncoding.EncodeToString([]byte(output))
		payload, _ := json.Marshal(map[string]any{
			"data": encoded,
			"seq":  seq,
		})
		seq++

		if err := st.natsStream.PublishTermOutTab(st.sessionID, tabID, string(payload)); err != nil {
			log.Printf("[session-terminal] erro ao publicar term.out.%s: %v", tabID, err)
		}

		// Enviar para gravacao se habilitado
		if st.recordingTap != nil {
			st.recordingTap.Write(tabID, encoded, seq-1)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("criar shell %s: %w", shellKind, err)
	}

	tab.Shell = shell

	st.mu.Lock()
	st.tabs[tabID] = tab
	st.mu.Unlock()

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

		// Decodificar input base64
		if req.Data != "" {
			decoded, err := base64.StdEncoding.DecodeString(req.Data)
			if err != nil {
				return
			}
			_ = shell.WriteStdin(string(decoded))
		}
	})
	if err != nil {
		shell.Close()
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

	log.Printf("[session-terminal] tab criada: %s shell=%s cols=%d rows=%d",
		tabID, shellKind, cols, rows)

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
		st.recordingTap.enabled = false
	}

	log.Printf("[session-terminal] todas as tabs fechadas: session=%s", st.sessionID)
}

// RecordingTap methods

func (rt *RecordingTap) Write(tabID string, data string, seq int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.enabled {
		return
	}

	frame := TermRecordingFrame{
		TabID:       tabID,
		Data:        data,
		Seq:         seq,
		TimestampMs: time.Now().UnixMilli(),
	}
	payload, _ := json.Marshal(frame)
	_ = rt.natsStream.PublishRecordingTerm(rt.sessionID, payload)
}

// Ensure imports
var _ context.Context
var _ fmt.Stringer
