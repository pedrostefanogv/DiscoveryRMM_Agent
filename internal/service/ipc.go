package service

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

const (
	// PipeName é o nome da Named Pipe para comunicação Service ↔ UI
	PipeName = `\\.\pipe\Discovery_Service`
	// Permite SYSTEM/Admin com controle total e usuários autenticados com leitura/escrita.
	pipeSecurityDescriptor = `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)`
)

// IPCServer escuta conexões de UI apps via Named Pipe
// Despacha comandos para o ServiceManager
type IPCServer struct {
	pipeName  string
	listener  net.Listener
	done      chan bool
	manager   *ServiceManager
	mu        sync.RWMutex
	isRunning bool
}

// NewIPCServer cria um novo servidor IPC
func NewIPCServer(dataDir string) *IPCServer {
	return &IPCServer{
		pipeName: PipeName,
		done:     make(chan bool, 1),
	}
}

// ClientRequest representa um comando vindo de uma UI app
type ClientRequest struct {
	ID       string                 `json:"id"`        // UUID único do request
	Command  string                 `json:"command"`   // "getStatus", "getConfig", "execute", "getPolicies"
	UserSID  string                 `json:"user_sid"`  // SID do usuário (S-1-5-...)
	UserName string                 `json:"user_name"` // Ex: "DESKTOP\pedro"
	Elevated bool                   `json:"elevated"`  // Se UI está rodando com admin?
	Payload  map[string]interface{} `json:"payload"`   // Dados do comando
}

// ServiceResponse representa a resposta do service
type ServiceResponse struct {
	ID      string                 `json:"id"`      // Echo do request ID
	Status  string                 `json:"status"`  // "success", "error", "pending"
	Code    int                    `json:"code"`    // HTTP-like status code
	Message string                 `json:"message"` // Mensagem descritiva
	Data    map[string]interface{} `json:"data"`    // Payload de resposta
}

// Start inicia o servidor IPC
func (server *IPCServer) Start(manager *ServiceManager) error {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.isRunning {
		return fmt.Errorf("IPC server já está rodando")
	}

	server.manager = manager

	// Criar listener na Named Pipe
	// Configuração para multiusuário: SYSTEM/Admin full, usuários autenticados read/write.
	ln, err := winio.ListenPipe(server.pipeName, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
	})
	if err != nil {
		return fmt.Errorf("falha ao criar listener em %s: %w", server.pipeName, err)
	}

	server.listener = ln
	server.isRunning = true

	// Loop aceitando conexões em background
	go server.acceptConnections()

	return nil
}

// Stop encerra o servidor IPC
func (server *IPCServer) Stop() {
	server.mu.Lock()
	defer server.mu.Unlock()

	if !server.isRunning {
		return
	}

	server.isRunning = false
	if server.listener != nil {
		server.listener.Close()
	}

	select {
	case server.done <- true:
	default:
	}
}

// acceptConnections loop principal que aceita conexões
func (server *IPCServer) acceptConnections() {
	for {
		server.mu.RLock()
		if !server.isRunning {
			server.mu.RUnlock()
			break
		}
		server.mu.RUnlock()

		conn, err := server.listener.Accept()
		if err != nil {
			// Se encerrou, sair
			if strings.Contains(err.Error(), "closed") {
				return
			}
			fmt.Fprintf(os.Stderr, "[IPC.AcceptConnections] Accept error: %v\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Processar cliente em goroutine
		go server.handleClient(conn)
	}
}

// handleClient processa uma conexão de cliente
func (server *IPCServer) handleClient(conn net.Conn) {
	defer conn.Close()

	// Set timeout para leitura
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Ler request JSON
	var req ClientRequest
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&req); err != nil {
		resp := &ServiceResponse{
			ID:      "unknown",
			Status:  "error",
			Code:    400,
			Message: "Invalid JSON: " + err.Error(),
		}
		server.respond(conn, resp)
		return
	}

	fmt.Printf("[IPC.Client] Request: id=%s, cmd=%s, user=%s\n",
		req.ID, req.Command, req.UserName)

	// Despachar comando
	response := server.dispatchCommand(&req)

	// Enviar resposta
	server.respond(conn, response)
}

// dispatchCommand processa o comando recebido
func (server *IPCServer) dispatchCommand(req *ClientRequest) *ServiceResponse {
	switch strings.ToLower(req.Command) {
	case "getstatus":
		return server.cmdGetStatus(req)
	case "getconfig":
		return server.cmdGetConfig(req)
	case "execute":
		return server.cmdExecute(req)
	case "getpolicies":
		return server.cmdGetPolicies(req)
	default:
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    404,
			Message: "Comando desconhecido: " + req.Command,
		}
	}
}

// cmdGetStatus retorna status do service
func (server *IPCServer) cmdGetStatus(req *ClientRequest) *ServiceResponse {
	status := server.manager.GetStatus()

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data:    status,
	}
}

// cmdGetConfig retorna configuração compartilhada
func (server *IPCServer) cmdGetConfig(req *ClientRequest) *ServiceResponse {
	cfg := server.manager.GetConfig()
	if cfg == nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    503,
			Message: "Serviço inicializando, tente novamente em segundos",
		}
	}

	// Retornar config com token redacted para segurança
	data := map[string]interface{}{
		"agent_id":    cfg.AgentID,
		"server_url":  cfg.ServerURL,
		"api_scheme":  cfg.ApiScheme,
		"api_server":  cfg.ApiServer,
		"auth_token":  "***redacted***", // Nunca enviar token completo
		"client_id":   cfg.ClientID,
		"p2p_enabled": cfg.P2PEnabled,
		"last_sync":   cfg.LastSync,
	}

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data:    data,
	}
}

// cmdExecute executa uma ação no contexto do service
func (server *IPCServer) cmdExecute(req *ClientRequest) *ServiceResponse {
	// TODO: Implementar executor com contexto do usuário
	// Por enquanto, apenas queue o comando

	action, ok := req.Payload["action"].(string)
	if !ok {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: "Campo 'action' obrigatório",
		}
	}

	actionID := req.ID + "_" + fmt.Sprintf("%d", time.Now().Unix())

	fmt.Printf("[IPC.Execute] Action queued: id=%s, action=%s, user=%s\n",
		actionID, action, req.UserName)

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "pending",
		Code:    202,
		Message: "Ação enfileirada",
		Data: map[string]interface{}{
			"action_id":  actionID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "queued",
		},
	}
}

// cmdGetPolicies retorna políticas de automação ativas
func (server *IPCServer) cmdGetPolicies(req *ClientRequest) *ServiceResponse {
	// TODO: Implementar busca de políticas
	// Por enquanto, retornar lista vazia

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data: map[string]interface{}{
			"policies": []map[string]interface{}{},
		},
	}
}

// respond envia resposta JSON ao cliente
func (server *IPCServer) respond(conn net.Conn, resp *ServiceResponse) error {
	encoder := json.NewEncoder(conn)
	return encoder.Encode(resp)
}
