package app

import (
	"context"
	"time"

	"discovery/app/supportchat"
)

// ── Chat de suporte vinculado ao chamado (lado do agent) ────────────────────
//
// O serviço mantém APENAS estado em memória: nada de conversa é gravado em
// SQLite/chat.db. O histórico canônico é do servidor e só é consultável pelo
// console web; a janela do chat carrega somente a sessão ativa.

// supportChatService devolve (criando na primeira chamada) o serviço do chat.
func (a *App) supportChatService() *supportchat.Service {
	a.supportChatMu.Lock()
	defer a.supportChatMu.Unlock()

	if a.supportChatSvc != nil {
		return a.supportChatSvc
	}

	a.supportChatSvc = supportchat.New(supportchat.Deps{
		Ctx: func() context.Context { return a.ctx },
		Logf: func(line string) {
			a.Logs.Append(line)
		},
		GetDebugConfig: func() supportchat.DebugConfig {
			cfg := a.GetDebugConfig()
			return supportchat.DebugConfig{
				AgentID:   cfg.AgentID,
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
			}
		},
		Emit: func(name string, data ...any) {
			a.EmitEvent(name, data...)
			// Transições de estado do chat também governam o modo da janela:
			// "sempre no topo" apenas enquanto o convite está pendente.
			a.syncSupportChatWindowFromEvent(name, data...)
		},
	})

	return a.supportChatSvc
}

// handleSupportChatEnvelope entrega um envelope do canal servidor -> agent ao
// serviço (chamado pelo agentconn). Erro devolve Ack negativo (reentrega).
func (a *App) handleSupportChatEnvelope(_ context.Context, data []byte) error {
	return a.supportChatService().HandleEnvelope(data)
}

// Constantes do cache da política de anexos do chat.
const (
	// supportChatPolicyTTL: a política muda raramente (Configurações > Anexos
	// do console); 5 min de cache evita uma consulta por abertura de janela.
	supportChatPolicyTTL = 5 * time.Minute
	// supportChatPolicyRetry: quando a consulta falha, tenta de novo em 1 min em
	// vez de ficar com o default por 5.
	supportChatPolicyRetry = time.Minute
	// supportChatPolicyWait limita a latência do BINDING chamado na abertura da
	// janela: uma rede degradada não pode atrasar o carregamento do chat.
	supportChatPolicyWait = 4 * time.Second
)

// supportChatAttachmentPolicy devolve a dica de UI (accept/tamanho) para o
// seletor de arquivos da janela do chat.
//
// A AUTORIDADE da allowlist é o servidor (TicketAttachmentSettings), que valida
// tipo e tamanho no presign e no complete. A política é CONSULTADA no servidor
// (GET .../support-chat/attachment-policy) e cacheada aqui: antes o agent
// devolvia um espelho hardcoded e o usuário só descobria o tipo proibido no
// envio. Falha de rede cai no default do servidor — nunca trava o seletor.
func (a *App) supportChatAttachmentPolicy() map[string]any {
	policy := a.cachedSupportChatAttachmentPolicy()
	return map[string]any{
		"enabled":             policy.Enabled,
		"allowedContentTypes": policy.AllowedContentTypes,
		"maxFileSizeBytes":    policy.MaxFileSizeBytes,
	}
}

// cachedSupportChatAttachmentPolicy devolve a política cacheada, buscando no
// servidor quando o cache venceu.
func (a *App) cachedSupportChatAttachmentPolicy() supportchat.AttachmentPolicy {
	now := time.Now()

	a.supportChatPolicyMu.Lock()
	if a.supportChatPolicyReady && now.Before(a.supportChatPolicyExpires) {
		policy := a.supportChatPolicy
		a.supportChatPolicyMu.Unlock()
		return policy
	}
	a.supportChatPolicyMu.Unlock()

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatPolicyWait)
	defer cancel()

	policy, err := a.supportChatService().FetchAttachmentPolicy(ctx)

	ttl := supportChatPolicyTTL
	if err != nil {
		// Servidor antigo (rota ainda não publicada) cai aqui: log curto e
		// retry em 1 min — sem repetir o aviso a cada janela aberta.
		a.Logs.Append("[supportchat] politica de anexos indisponivel no servidor; usando o padrao: " + err.Error())
		ttl = supportChatPolicyRetry
	}

	a.supportChatPolicyMu.Lock()
	a.supportChatPolicy = policy
	a.supportChatPolicyExpires = now.Add(ttl)
	a.supportChatPolicyReady = true
	a.supportChatPolicyMu.Unlock()

	return policy
}
