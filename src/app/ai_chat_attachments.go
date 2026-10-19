package app

import (
	"time"

	appsupport "discovery/app/support"
)

// ── Política de anexos do chat IA/LLM ─────────────────────────────────────
//
// A configuração de Anexos do servidor agora é UNIFICADA: o mesmo
// TicketAttachmentSettings (limite geral 10 MB + allowlist MIME) vale para o
// chat IA, anexos de tickets/comentários e chat de suporte. Para o chat IA o
// endpoint é GET /api/v1/agent-auth/me/ai-chat/attachment-settings.
//
// A AUTORIDADE da validação é o servidor. O App consulta a política e a
// compartilha entre:
//   - a validação no Go dos data URLs enviados pelo composer; e
//   - o binding AIChatAttachmentPolicy() consumido pela UI (accept/limite).

const (
	// aiChatPolicyTTL: a política muda raramente (console > Anexos); 5 min de
	// cache evita uma consulta por imagem anexada.
	aiChatPolicyTTL = 5 * time.Minute
	// aiChatPolicyRetry: falha (servidor antigo/rede) tenta de novo em 1 min em
	// vez de ficar no default por 5.
	aiChatPolicyRetry = time.Minute
)

// cachedAIChatAttachmentSettings devolve a política cacheada, buscando no
// servidor quando o cache venceu. Em erro cai no default (10 MB, imagens) e
// agenda retry curto — nunca bloqueia o composer.
func (a *App) cachedAIChatAttachmentSettings() appsupport.AIAttachmentSettings {
	now := time.Now()

	a.aiChatPolicyMu.Lock()
	if a.aiChatPolicyReady && now.Before(a.aiChatPolicyExpires) {
		settings := a.aiChatPolicy
		a.aiChatPolicyMu.Unlock()
		return settings
	}
	a.aiChatPolicyMu.Unlock()

	settings := appsupport.DefaultAIAttachmentSettings()
	ttl := aiChatPolicyTTL
	if a.SupportSvc == nil {
		return settings
	}

	// A chamada HTTP do serviço tem timeout próprio; falha cai no default e
	// agenda retry curto, sem travar o composer.
	fetched, err := a.SupportSvc.GetAIChatAttachmentSettings()
	if err != nil {
		// Servidor antigo (rota ainda não publicada) cai aqui: log curto e
		// retry em 1 min — sem repetir o aviso a cada anexo.
		a.Logs.Append("[chat] politica de anexos do chat IA indisponivel no servidor; usando o padrao: " + err.Error())
		ttl = aiChatPolicyRetry
	} else {
		settings = fetched
	}

	a.aiChatPolicyMu.Lock()
	a.aiChatPolicy = settings
	a.aiChatPolicyExpires = now.Add(ttl)
	a.aiChatPolicyReady = true
	a.aiChatPolicyMu.Unlock()

	return settings
}

// AIChatAttachmentPolicy expõe à UI do chat (seletor/validação de imagens) a
// política vigente: enabled, allowlist MIME e limite em bytes. A UI usa isto
// apenas para evitar erro previsível; o servidor continua validando.
func (a *App) AIChatAttachmentPolicy() map[string]any {
	settings := a.cachedAIChatAttachmentSettings()
	return map[string]any{
		"enabled":             settings.Enabled,
		"allowedContentTypes": settings.AllowedContentTypes,
		"maxFileSizeBytes":    settings.MaxFileSizeBytes,
	}
}
