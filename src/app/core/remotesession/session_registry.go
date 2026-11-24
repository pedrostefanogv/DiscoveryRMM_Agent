//go:build windows

package remotesession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"discovery/app/core/winregistry"
)

// SessionRegistry gerencia uma sessão remota de navegação/edição do Registro.
//
// Mesmo desenho de SessionProcesses: request/response sobre NATS core, nos
// subjects .registry.req (viewer→agent) e .registry.resp (agent→viewer), com
// .registry.ready publicado APÓS o subscribe. O ready não é decoração: o NATS
// core não tem replay, e sem ele o primeiro request do viewer se perde na
// corrida entre "conectou" e "o subscribe está ativo" — o operador via a árvore
// vazia e precisava recarregar.
type SessionRegistry struct {
	sessionID  string
	natsStream *NatsStreamHandler
	stopCh     chan struct{}
	doneCh     chan struct{}
}

// NewSessionRegistry cria um gerenciador de sessão de registro.
func NewSessionRegistry(sessionID string, natsStream *NatsStreamHandler) *SessionRegistry {
	return &SessionRegistry{
		sessionID:  sessionID,
		natsStream: natsStream,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// registryProtocolVersion é a versão do contrato aceita por este agente.
//
// Versionar o protocolo transforma uma incompatibilidade silenciosa ("a UI não
// faz nada") em um erro explícito e legível. O viewer declara a versão no
// request; um agente antigo recusa com "versão não suportada" em vez de
// interpretar campos que não conhece.
const registryProtocolVersion = 1

// Start subscreve .registry.req e processa requisições até o cancelamento.
func (sr *SessionRegistry) Start(ctx context.Context) error {
	defer close(sr.doneCh)

	sub, err := sr.natsStream.SubscribeToRegistryReq(sr.sessionID, func(reqData []byte) []byte {
		return sr.handleRequest(reqData)
	})
	if err != nil {
		return fmt.Errorf("subscribe registry.req: %w", err)
	}
	defer sub.Unsubscribe()

	log.Printf("[session-registry] sessão de registro iniciada para %s", sr.sessionID)

	// Notifica o viewer que o subscribe está ativo (anti-race, igual a
	// proc.ready/files.ready). Carrega a versão do protocolo para o viewer
	// recusar cedo se houver incompatibilidade.
	readyPayload := map[string]any{
		"status":  "ready",
		"version": registryProtocolVersion,
	}
	if activeUser := winregistry.ActiveUserContext(); activeUser != nil {
		readyPayload["activeUser"] = activeUser
	}
	_ = sr.natsStream.PublishRegistryReady(sr.sessionID, readyPayload)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sr.stopCh:
		return nil
	}
}

// Stop encerra a sessão de registro.
func (sr *SessionRegistry) Stop() {
	select {
	case <-sr.stopCh:
	default:
		close(sr.stopCh)
	}
	<-sr.doneCh
}

// registryRequest é o contrato de requisição do viewer.
//
// Confirmed é a barreira de confirmação vinda da UI: o agente recusa QUALQUER
// mutação sem confirmed=true, mesmo que a API e a UI tenham deixado passar. É
// defesa em profundidade — um viewer adulterado que publique direto no NATS
// ainda não consegue escrever.
type registryRequest struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	Hive      string `json:"hive"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Data      string `json:"data"`
	NewName   string `json:"newName"`
	Token     string `json:"token"`
	Recursive bool   `json:"recursive"`
	Confirmed bool   `json:"confirmed"`
}

// registryResponse é o contrato de resposta para o viewer.
//
// RequestID é ecoado em TODAS as respostas, inclusive nas de erro: sem ele o
// viewer não consegue correlacionar resposta e requisição em uma sessão com
// várias operações concorrentes (a árvore carrega vários nós em paralelo).
type registryResponse struct {
	Success   bool                    `json:"success"`
	Error     string                  `json:"error,omitempty"`
	ErrorKind string                  `json:"errorKind,omitempty"`
	RequestID string                  `json:"requestId,omitempty"`
	Hive      string                  `json:"hive,omitempty"`
	Path      string                  `json:"path,omitempty"`
	Key       *winregistry.KeyInfo    `json:"key,omitempty"`
	Page      *winregistry.ListPage   `json:"page,omitempty"`
	Value     *winregistry.ValueEntry `json:"value,omitempty"`
	Export    string                  `json:"export,omitempty"`
	// Note carrega limitações/avisos ao operador (ex.: a visão de HKCU).
	Note       string                   `json:"note,omitempty"`
	ActiveUser *winregistry.UserContext `json:"activeUser,omitempty"`
}

// Ações suportadas. Lista fechada: qualquer outra coisa é recusada sem tocar no
// registro (mesma filosofia de ParseServiceAction em sysctrl).
const (
	ActionListKey     = "listKey"
	ActionStatKey     = "statKey"
	ActionGetValue    = "getValue"
	ActionSetValue    = "setValue"
	ActionDeleteValue = "deleteValue"
	ActionCreateKey   = "createKey"
	ActionDeleteKey   = "deleteKey"
	ActionRenameKey   = "renameKey"
	ActionExport      = "export"
)

func (sr *SessionRegistry) handleRequest(reqData []byte) []byte {
	var req registryRequest
	if err := json.Unmarshal(reqData, &req); err != nil {
		return sr.encode(registryResponse{Success: false, Error: "payload json inválido: " + err.Error(), ErrorKind: "bad_request"})
	}

	// Versão ANTES de qualquer outra validação: um request de versão
	// desconhecida pode ter campos com semântica diferente, e interpretá-lo
	// seria pior que recusá-lo.
	if req.Version != registryProtocolVersion {
		return sr.encode(registryResponse{
			RequestID: req.RequestID,
			Success:   false,
			ErrorKind: "version_unsupported",
			Error:     fmt.Sprintf("versão de protocolo não suportada: recebido %d, esperado %d", req.Version, registryProtocolVersion),
		})
	}

	hive, ok := winregistry.ParseHive(req.Hive)
	if !ok {
		return sr.encode(sr.fail(req, "invalid_hive", fmt.Errorf("%w: %q", winregistry.ErrInvalidHive, req.Hive)))
	}

	// Log de auditoria local no agente. O servidor também audita; ter os dois
	// lados é o que permite reconstruir a ação quando a sessão cai no meio.
	log.Printf("[session-registry] sessionId=%s action=%s hive=%s path=%q name=%q confirmed=%t",
		sr.sessionID, req.Action, hive, req.Path, req.Name, req.Confirmed)

	switch req.Action {
	case ActionListKey:
		page, err := winregistry.ListKey(hive, req.Path, req.Token)
		if err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		var userCtx *winregistry.UserContext
		if hive == winregistry.HiveCurrentUser {
			userCtx = page.ActiveUser
		}
		return sr.encode(registryResponse{
			RequestID:  req.RequestID,
			Success:    true,
			Hive:       string(hive),
			Path:       page.Path,
			Page:       page,
			Note:       registryHiveNote(hive, userCtx),
			ActiveUser: userCtx,
		})

	case ActionStatKey:
		info, err := winregistry.StatKey(hive, req.Path)
		if err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		var userCtx *winregistry.UserContext
		if hive == winregistry.HiveCurrentUser {
			userCtx = info.ActiveUser
		}
		return sr.encode(registryResponse{
			RequestID:  req.RequestID,
			Success:    true,
			Hive:       string(hive),
			Path:       info.Path,
			Key:        info,
			Note:       registryHiveNote(hive, userCtx),
			ActiveUser: userCtx,
		})

	case ActionGetValue:
		value, err := winregistry.GetValue(hive, req.Path, req.Name)
		if err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{
			RequestID: req.RequestID,
			Success:   true,
			Hive:      string(hive),
			Path:      req.Path,
			Value:     value,
		})

	case ActionSetValue:
		vType, err := winregistry.ParseValueType(req.Type)
		if err != nil {
			return sr.encode(sr.fail(req, "invalid_type", err))
		}
		if err := winregistry.SetValue(hive, req.Path, req.Name, vType, req.Data, req.Confirmed); err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path})

	case ActionDeleteValue:
		if err := winregistry.DeleteValue(hive, req.Path, req.Name, req.Confirmed); err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path})

	case ActionCreateKey:
		if err := winregistry.CreateKey(hive, req.Path, req.Confirmed); err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path})

	case ActionDeleteKey:
		if err := winregistry.DeleteKey(hive, req.Path, req.Recursive, req.Confirmed); err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path})

	case ActionRenameKey:
		if err := winregistry.RenameKey(hive, req.Path, req.NewName, req.Confirmed); err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path})

	case ActionExport:
		content, err := winregistry.ExportKey(hive, req.Path, req.Recursive)
		if err != nil {
			return sr.encode(sr.fail(req, classifyRegistryError(err), err))
		}
		return sr.encode(registryResponse{RequestID: req.RequestID, Success: true, Hive: string(hive), Path: req.Path, Export: content})

	default:
		// Ação desconhecida NUNCA é "adivinhada" nem cai em um default permissivo.
		return sr.encode(registryResponse{
			RequestID: req.RequestID,
			Success:   false,
			ErrorKind: "unknown_action",
			Error:     "ação desconhecida: " + req.Action,
		})
	}
}

// fail monta a resposta de erro preservando o requestId.
func (sr *SessionRegistry) fail(req registryRequest, kind string, err error) registryResponse {
	return registryResponse{
		RequestID: req.RequestID,
		Success:   false,
		ErrorKind: kind,
		Error:     err.Error(),
	}
}

// classifyRegistryError traduz o sentinela do pacote em um código estável.
//
// A UI decide o que fazer pelo CÓDIGO, não pelo texto da mensagem: "protegida"
// mostra um aviso de política, "não encontrada" oferece criar a chave, "acesso
// negado" sugere problema de privilégio. Um único "erro" genérico obrigaria a
// UI a fazer matching de string em português — frágil e intraduzível.
func classifyRegistryError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, winregistry.ErrInvalidHive):
		return "invalid_hive"
	case errors.Is(err, winregistry.ErrInvalidPath):
		return "invalid_path"
	case errors.Is(err, winregistry.ErrProtectedKey):
		return "protected"
	case errors.Is(err, winregistry.ErrAccessDenied):
		// Distinto de "protected": a chave nao esta na denylist, apenas o agente
		// nao tem privilegio nela. A UI mostra mensagem diferente ("sem
		// permissao" vs "bloqueado por politica"), e a auditoria registra certo.
		return "access_denied"
	case errors.Is(err, winregistry.ErrKeyNotEmpty):
		// Erro acionavel: a chave tem filhos. Distinto de "access_denied" para a
		// UI poder oferecer a exclusao recursiva em vez de sugerir privilegio.
		return "key_not_empty"
	case errors.Is(err, winregistry.ErrKeyNotFound):
		return "not_found"
	case errors.Is(err, winregistry.ErrValueNotFound):
		return "value_not_found"
	case errors.Is(err, winregistry.ErrUnsupportedType):
		return "invalid_type"
	case errors.Is(err, winregistry.ErrInvalidData):
		return "invalid_data"
	case errors.Is(err, winregistry.ErrConfirmationRequired):
		return "confirmation_required"
	case errors.Is(err, winregistry.ErrLimitExceeded):
		return "limit_exceeded"
	default:
		return "internal"
	}
}

// registryHiveNote expõe a visão efetiva do hive ao operador.
//
// Quando há usuário interativo conectado, informa o usuário e SID para o qual
// HKCU está mapeado via HKEY_USERS\<SID>. Sem usuário conectado, avisa que
// HKCU exibe a chave da conta do agente (SYSTEM no serviço).
func registryHiveNote(hive winregistry.Hive, user *winregistry.UserContext) string {
	if hive != winregistry.HiveCurrentUser {
		return ""
	}
	if user != nil {
		return fmt.Sprintf("HKCU mapeado para o usuário conectado: %s (%s). Visão 64-bit.", user.DisplayName(), user.SID)
	}
	return "Nenhum usuário interativo conectado — HKCU exibe a chave da conta do agente (SYSTEM no serviço). Visão 64-bit."
}

func (sr *SessionRegistry) encode(resp registryResponse) []byte {
	b, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"success":false,"error":"falha ao serializar resposta","errorKind":"internal"}`)
	}
	return b
}
