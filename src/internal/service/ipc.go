package service

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Command  string                 `json:"command"`   // "getStatus", "getConfig", "getServiceHealth", "execute", "getPolicies"
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

	// Criar listener na Named Pipe (Windows) ou retornar erro explícito em
	// plataformas que não suportam esse mecanismo de IPC.
	ln, err := createIPCListener(server.pipeName)
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

	// Sobrescrever identidade com dados verificados pelo servidor (Windows: client-supplied descartado).
	applyServerSideIdentity(conn, &req)

	fmt.Printf("[IPC.Client] Request: id=%s, cmd=%s, user=%s (sid=%s)\n",
		req.ID, req.Command, req.UserName, req.UserSID)

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
	case "getservicehealth":
		return server.cmdGetServiceHealth(req)
	case "triggerupdatecheck":
		return server.cmdTriggerUpdateCheck(req)
	case "execute":
		return server.cmdExecute(req)
	case "getpolicies":
		return server.cmdGetPolicies(req)
	case "getactionstatus":
		return server.cmdGetActionStatus(req)
	case "getactionhistory":
		return server.cmdGetActionHistory(req)
	default:
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    404,
			Message: "Comando desconhecido: " + req.Command,
		}
	}
}

func (server *IPCServer) cmdTriggerUpdateCheck(req *ClientRequest) *ServiceResponse {
	source := "manual"
	if req.Payload != nil {
		if raw, ok := req.Payload["source"]; ok {
			if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
				source = strings.TrimSpace(value)
			}
		}
	}
	queued := server.manager.RequestSelfUpdateCheck(source)
	message := "self-update force-check solicitado"
	if !queued {
		message = "self-update force-check ja estava pendente"
	}
	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    202,
		Message: message,
		Data: map[string]interface{}{
			"queued": queued,
			"source": source,
		},
	}
}

// cmdGetServiceHealth retorna saúde detalhada dos componentes monitorados no service.
func (server *IPCServer) cmdGetServiceHealth(req *ClientRequest) *ServiceResponse {
	status := server.manager.GetServiceHealth()

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data:    status,
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
// Armazena em action_queue para processamento e rastreamento
func (server *IPCServer) cmdExecute(req *ClientRequest) *ServiceResponse {
	if req.Payload == nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: "Payload obrigatório",
		}
	}

	action, ok := req.Payload["action"].(string)
	action = strings.TrimSpace(action)
	if !ok {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: "Campo 'action' obrigatório",
		}
	}
	if action == "" {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: "Campo 'action' obrigatório",
		}
	}

	actionID := req.ID + "_" + fmt.Sprintf("%d", time.Now().Unix())
	if strings.TrimSpace(req.ID) == "" {
		actionID = fmt.Sprintf("action_%d", time.Now().UnixNano())
	}

	payloadJSON, err := json.Marshal(req.Payload)
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: "Payload inválido",
		}
	}

	userSID := strings.TrimSpace(req.UserSID)
	if userSID == "" {
		userSID = "unknown"
	}
	userName := strings.TrimSpace(req.UserName)
	if userName == "" {
		userName = "unknown"
	}

	if err := server.manager.EnqueueAction(actionID, userSID, userName, action, string(payloadJSON)); err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    503,
			Message: "Falha ao enfileirar ação: " + err.Error(),
		}
	}

	// Promover token de conexão para o registro de ações (Windows: impersonation assíncrona).
	promoteTokenForAction(req.ID, actionID)

	fmt.Printf("[IPC.Execute] Action queued: id=%s, action=%s, user=%s\n",
		actionID, action, userName)

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "pending",
		Code:    202,
		Message: "Ação enfileirada",
		Data: map[string]interface{}{
			"action_id":  actionID,
			"started_at": time.Now().Format(time.RFC3339),
			"status":     "queued",
			"user":       userName,
		},
	}
}

// cmdGetPolicies retorna políticas de automação ativas
func (server *IPCServer) cmdGetPolicies(req *ClientRequest) *ServiceResponse {
	policies, err := server.manager.GetPolicies()
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    503,
			Message: "Falha ao carregar políticas: " + err.Error(),
		}
	}

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data: map[string]interface{}{
			"policies": policies,
		},
	}
}

func (server *IPCServer) cmdGetActionStatus(req *ClientRequest) *ServiceResponse {
	actionID, err := requiredPayloadString(req.Payload, "action_id")
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: err.Error(),
		}
	}

	data, found, err := server.manager.GetActionStatus(actionID)
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    503,
			Message: "Falha ao carregar status da ação: " + err.Error(),
		}
	}
	if !found {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    404,
			Message: "Ação não encontrada",
		}
	}

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data:    data,
	}
}

func (server *IPCServer) cmdGetActionHistory(req *ClientRequest) *ServiceResponse {
	actionID, err := requiredPayloadString(req.Payload, "action_id")
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: err.Error(),
		}
	}

	limit, err := optionalPayloadInt(req.Payload, "limit", 20, 100)
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    400,
			Message: err.Error(),
		}
	}

	data, found, err := server.manager.GetActionHistory(actionID, limit)
	if err != nil {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    503,
			Message: "Falha ao carregar histórico da ação: " + err.Error(),
		}
	}
	if !found {
		return &ServiceResponse{
			ID:      req.ID,
			Status:  "error",
			Code:    404,
			Message: "Ação não encontrada",
		}
	}

	return &ServiceResponse{
		ID:      req.ID,
		Status:  "success",
		Code:    200,
		Message: "OK",
		Data:    data,
	}
}

func requiredPayloadString(payload map[string]interface{}, key string) (string, error) {
	if payload == nil {
		return "", fmt.Errorf("payload obrigatório")
	}
	raw, ok := payload[key]
	if !ok {
		return "", fmt.Errorf("campo '%s' obrigatório", key)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("campo '%s' obrigatório", key)
	}
	return strings.TrimSpace(value), nil
}

func optionalPayloadInt(payload map[string]interface{}, key string, defaultValue, maxValue int) (int, error) {
	if payload == nil {
		return defaultValue, nil
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return defaultValue, nil
	}

	var value int
	switch typed := raw.(type) {
	case float64:
		value = int(typed)
	case float32:
		value = int(typed)
	case int:
		value = typed
	case int32:
		value = int(typed)
	case int64:
		value = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("campo '%s' inválido", key)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("campo '%s' inválido", key)
	}

	if value <= 0 {
		return 0, fmt.Errorf("campo '%s' inválido", key)
	}
	if maxValue > 0 && value > maxValue {
		return maxValue, nil
	}
	return value, nil
}

// respond envia resposta JSON ao cliente
func (server *IPCServer) respond(conn net.Conn, resp *ServiceResponse) error {
	encoder := json.NewEncoder(conn)
	return encoder.Encode(resp)
}
