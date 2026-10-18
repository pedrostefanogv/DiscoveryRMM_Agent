package app

import (
	"context"

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

// supportChatAttachmentPolicy devolve a dica de UI (accept/tamanho) para o
// seletor de arquivos da janela do chat.
//
// A AUTORIDADE da allowlist é o servidor (TicketAttachmentSettings), que valida
// tipo e tamanho no presign e no complete. O agent não expõe hoje a
// ServerConfiguration à camada de UI, então a janela usa o default do servidor
// para montar o atributo "accept" — um arquivo fora da lista continua sendo
// rejeitado pelo servidor com mensagem clara.
func (a *App) supportChatAttachmentPolicy() map[string]any {
	return map[string]any{
		"enabled":             true,
		"allowedContentTypes": []string{"image/jpeg", "image/png", "image/webp", "application/pdf"},
		"maxFileSizeBytes":    int64(10 * 1024 * 1024),
	}
}
