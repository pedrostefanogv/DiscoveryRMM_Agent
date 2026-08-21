//go:build windows

package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"discovery/app/core/sysctrl"
)

// SessionProcesses gerencia uma sessão remota de processos e serviços.
//
// Usa os subjects .proc.req (viewer→agent) e .proc.resp (agent→viewer),
// mesmo padrão do arquivos (files.req/files.resp). Publica proc.ready após o
// subscribe para evitar a race em que o primeiro request chega antes do
// subscribe (NATS core não tem replay).
type SessionProcesses struct {
	sessionID  string
	natsStream *NatsStreamHandler
	stopCh     chan struct{}
	doneCh     chan struct{}
}

// NewSessionProcesses cria um gerenciador de sessão de processos/serviços.
func NewSessionProcesses(sessionID string, natsStream *NatsStreamHandler) *SessionProcesses {
	return &SessionProcesses{
		sessionID:  sessionID,
		natsStream: natsStream,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start subscreve .proc.req e processa requisições.
func (sp *SessionProcesses) Start(ctx context.Context) error {
	defer close(sp.doneCh)

	sub, err := sp.natsStream.SubscribeToProcReq(sp.sessionID, func(reqData []byte) []byte {
		return sp.handleRequest(reqData)
	})
	if err != nil {
		return fmt.Errorf("subscribe proc.req: %w", err)
	}
	defer sub.Unsubscribe()

	log.Printf("[session-proc] sessão de processos/serviços iniciada para %s", sp.sessionID)

	// Notifica o viewer que a sessão está pronta (subscribe ativo).
	_ = sp.natsStream.PublishProcReady(sp.sessionID, map[string]any{
		"status": "ready",
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sp.stopCh:
		return nil
	}
}

// Stop encerra a sessão de processos/serviços.
func (sp *SessionProcesses) Stop() {
	select {
	case <-sp.stopCh:
	default:
		close(sp.stopCh)
	}
	<-sp.doneCh
}

// procRequest é o contrato de requisição do viewer.
type procRequest struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	PID       uint32 `json:"pid"`
	Name      string `json:"name"`
}

// procResponse é o contrato de resposta para o viewer.
type procResponse struct {
	Success   bool                  `json:"success"`
	Error     string                `json:"error,omitempty"`
	RequestID string                `json:"requestId,omitempty"`
	Processes []sysctrl.ProcessInfo `json:"processes,omitempty"`
	Services  []sysctrl.ServiceInfo `json:"services,omitempty"`
}

func (sp *SessionProcesses) handleRequest(reqData []byte) []byte {
	var req procRequest
	if err := json.Unmarshal(reqData, &req); err != nil {
		return sp.encode(procResponse{Success: false, Error: "payload json inválido: " + err.Error()})
	}
	if req.Version != 1 {
		return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: "versão não suportada"})
	}

	switch req.Action {
	case "listProcesses":
		procs, err := sysctrl.ListProcesses()
		if err != nil {
			return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: err.Error()})
		}
		// Evita nil → frontend serializa [] no JSON.
		if procs == nil {
			procs = []sysctrl.ProcessInfo{}
		}
		return sp.encode(procResponse{RequestID: req.RequestID, Success: true, Processes: procs})
	case "killProcess":
		if req.PID == 0 {
			return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: "pid é obrigatório"})
		}
		if err := sysctrl.KillProcess(req.PID); err != nil {
			return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: err.Error()})
		}
		return sp.encode(procResponse{RequestID: req.RequestID, Success: true})
	case "listServices":
		svcs, err := sysctrl.ListServices()
		if err != nil {
			return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: err.Error()})
		}
		if svcs == nil {
			svcs = []sysctrl.ServiceInfo{}
		}
		return sp.encode(procResponse{RequestID: req.RequestID, Success: true, Services: svcs})
	case "startService":
		return sp.serviceAction(req, sysctrl.StartService)
	case "stopService":
		return sp.serviceAction(req, sysctrl.StopService)
	case "restartService":
		return sp.serviceAction(req, sysctrl.RestartService)
	default:
		return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: "ação desconhecida: " + req.Action})
	}
}

func (sp *SessionProcesses) serviceAction(req procRequest, fn func(string) error) []byte {
	if req.Name == "" {
		return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: "name é obrigatório"})
	}
	if err := fn(req.Name); err != nil {
		return sp.encode(procResponse{RequestID: req.RequestID, Success: false, Error: err.Error()})
	}
	return sp.encode(procResponse{RequestID: req.RequestID, Success: true})
}

func (sp *SessionProcesses) encode(resp procResponse) []byte {
	b, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"success":false,"error":"falha ao serializar resposta"}`)
	}
	return b
}
