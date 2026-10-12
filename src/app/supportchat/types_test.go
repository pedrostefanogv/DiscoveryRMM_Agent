package supportchat

import (
	"testing"
	"time"
)

// A entrega do comando de convite NÃO é pontual (o servidor reentrega comandos
// não confirmados), então ExpiredAt é a primeira linha de defesa contra abrir a
// janela do chat para uma conversa que já morreu.
func TestInviteExpiredAt(t *testing.T) {
	now := time.Date(2026, 10, 12, 2, 38, 41, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt string
		want      bool
	}{
		{name: "futuro", expiresAt: "2026-10-12T02:40:00Z", want: false},
		{name: "fracao de segundo no futuro", expiresAt: "2026-10-12T02:38:41.5000000Z", want: false},
		{name: "passado distante (caso real, ~4 min apos o vencimento)", expiresAt: "2026-10-12T02:34:46.6479901Z", want: true},
		{name: "vencido dentro da tolerancia de relogio", expiresAt: "2026-10-12T02:37:30Z", want: false},
		{name: "vencido acima da tolerancia", expiresAt: "2026-10-12T02:36:30Z", want: true},
		{name: "com espacos", expiresAt: "  2026-10-12T02:36:30Z  ", want: true},
		{name: "ausente e tratado como valido", expiresAt: "", want: false},
		{name: "malformado e tratado como valido", expiresAt: "ontem", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invite := Invite{ExpiresAt: tt.expiresAt}
			if got := invite.ExpiredAt(now); got != tt.want {
				t.Fatalf("ExpiredAt(%q) = %v, esperado %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}
