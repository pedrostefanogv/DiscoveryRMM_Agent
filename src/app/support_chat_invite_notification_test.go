package app

import (
	"strings"
	"testing"
	"time"

	"discovery/app/supportchat"
)

func TestSupportChatInviteNotificationMessage(t *testing.T) {
	cases := []struct {
		name   string
		invite supportchat.Invite
		want   []string
	}{
		{
			name:   "sem atendente nem chamado",
			invite: supportchat.Invite{SessionID: "s1"},
			want:   []string{"O suporte quer abrir o chat com você", "Deseja abrir a conversa agora?"},
		},
		{
			name:   "com atendente e chamado",
			invite: supportchat.Invite{SessionID: "s1", SupportName: "Ana", TicketTitle: "Notebook lento"},
			want:   []string{"Ana quer abrir o chat com você", "Notebook lento", "Deseja abrir a conversa agora?"},
		},
		{
			name:   "chamado vazio nao deixa aspas soltas",
			invite: supportchat.Invite{SessionID: "s1", SupportName: "  ", TicketTitle: "   "},
			want:   []string{"O suporte quer abrir o chat com você. Deseja abrir a conversa agora?"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := supportChatInviteNotificationMessage(tc.invite)
			for _, part := range tc.want {
				if !strings.Contains(got, part) {
					t.Fatalf("mensagem %q nao contem %q", got, part)
				}
			}
			if tc.name == "chamado vazio nao deixa aspas soltas" && strings.Contains(got, "\"") {
				t.Fatalf("mensagem com aspas soltas: %q", got)
			}
		})
	}
}

func TestSupportChatInviteNotificationLabels(t *testing.T) {
	if got := supportChatInviteNotificationTitle(); got != "O suporte quer abrir o chat" {
		t.Fatalf("titulo = %q", got)
	}
	if got := supportChatInviteNotificationAllow(); got != "Abrir chat" {
		t.Fatalf("botao de abrir = %q", got)
	}
	if got := supportChatInviteNotificationDeny(); got != "Agora não" {
		t.Fatalf("botao de recusar = %q", got)
	}
	if hint := supportChatInviteNotificationHint(); !strings.Contains(hint, "sem ação automática") {
		t.Fatalf("dica deveria dizer que nao ha acao automatica: %q", hint)
	}
}
func TestSupportChatInviteNotificationAnsweredSuppressesReopen(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteNotificationAnswered("sessao-1")

	if !a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("sessão respondida deveria estar marcada")
	}
	// show() deve devolver true (tratado como já apresentado) sem criar cartão.
	if !a.showSupportChatInviteNotification(supportchat.Invite{SessionID: "sessao-1"}, false) {
		t.Fatal("convite já respondido deveria ser suprimido")
	}
	a.supportChatNotifyMu.Lock()
	cards := len(a.supportChatNotify)
	a.supportChatNotifyMu.Unlock()
	if cards != 0 {
		t.Fatalf("cartão criado para convite já respondido: %d", cards)
	}
}

func TestSupportChatInviteNotificationAnsweredExpires(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteNotificationAnswered("sessao-1")

	a.supportChatNotifyMu.Lock()
	a.supportChatNotifyAnswered["sessao-1"] = time.Now().Add(-time.Second)
	a.supportChatNotifyMu.Unlock()

	if a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("marca vencida não deveria suprimir o convite")
	}
}
