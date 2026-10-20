package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// Regressão dos relatos do agent no tray (2026-10-20): convite VENCIDO não pode
// ser apresentado por nenhum caminho (IPC, retomada, reentrega) — o cartão
// nasceria já morto ("a notificação aparece mas já é atrasada").
func TestSupportChatInvite_RejeitaConviteVencido(t *testing.T) {
	expired := supportchat.Invite{
		SessionID: "sessao-velha",
		ExpiresAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
	}
	if !expired.ExpiredAt(time.Now()) {
		t.Fatal("convite com expiresAt no passado precisa ser considerado vencido")
	}

	payload := map[string]any{
		"name":      "supportchat:invite",
		"sessionId": "sessao-velha",
		"expiresAt": expired.ExpiresAt,
	}
	got, ok := supportChatInviteFromIPCPayloadFull(payload)
	if !ok {
		t.Fatal("payload com sessionId deveria ser aceito")
	}
	if !got.ExpiredAt(time.Now()) {
		t.Fatal("o expiresAt precisa viajar no payload IPC (sem ele o cartão vencido volta)")
	}
}

// O payload IPC continua aceitando convites vivos e mantém a API antiga.
func TestSupportChatInviteFromIPCPayloadFull_Vivo(t *testing.T) {
	payload := map[string]any{
		"name":        "supportchat:invite",
		"sessionId":   "sessao-ok",
		"supportName": "Ana",
		"expiresAt":   time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}
	invite, ok := supportChatInviteFromIPCPayloadFull(payload)
	if !ok || invite.SessionID != "sessao-ok" || invite.SupportName != "Ana" {
		t.Fatalf("payload vivo mal interpretado: %+v ok=%v", invite, ok)
	}
	if invite.ExpiredAt(time.Now()) {
		t.Fatal("convite dentro do prazo não pode ser vencido")
	}

	sessionID, _, _, support, ok := supportChatInviteFromIPCPayload(payload)
	if !ok || sessionID != "sessao-ok" || support != "Ana" {
		t.Fatalf("API antiga quebrou: %q %q ok=%v", sessionID, support, ok)
	}
}
