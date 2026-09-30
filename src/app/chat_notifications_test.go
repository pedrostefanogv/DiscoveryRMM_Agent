package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func TestShouldNotifyChatResponse(t *testing.T) {
	cases := []struct {
		name          string
		state         chatWindowState
		chatTabActive bool
		want          bool
	}{
		{
			name:          "janela visivel e focada com aba de chat ativa",
			state:         chatWindowState{Visible: true, Focused: true},
			chatTabActive: true,
			want:          false,
		},
		{
			name:          "janela visivel e focada em outra aba",
			state:         chatWindowState{Visible: true, Focused: true},
			chatTabActive: false,
			want:          true,
		},
		{
			name:          "janela minimizada",
			state:         chatWindowState{Visible: true, Minimised: true, Focused: true},
			chatTabActive: true,
			want:          true,
		},
		{
			name:          "janela escondida no tray",
			state:         chatWindowState{Visible: false, Focused: false},
			chatTabActive: true,
			want:          true,
		},
		{
			name:          "janela sem foco",
			state:         chatWindowState{Visible: true, Focused: false},
			chatTabActive: true,
			want:          true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldNotifyChatResponse(tc.state, tc.chatTabActive); got != tc.want {
				t.Fatalf("shouldNotifyChatResponse(%+v, %t) = %t, esperado %t", tc.state, tc.chatTabActive, got, tc.want)
			}
		})
	}
}

func TestChatResponsePreview(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "texto simples", content: "Pronto, atualizei o ticket.", want: "Pronto, atualizei o ticket."},
		{name: "pula linhas vazias", content: "\n\n   \nResposta final", want: "Resposta final"},
		{name: "remove marcadores de markdown", content: "## Resumo\n- nada", want: "Resumo"},
		{name: "normaliza espacos", content: "linha   com\tmuitos   espacos", want: "linha com muitos espacos"},
		{name: "vazio", content: "   \n\t\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatResponsePreview(tc.content); got != tc.want {
				t.Fatalf("chatResponsePreview(%q) = %q, esperado %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestChatResponsePreviewTruncates(t *testing.T) {
	got := chatResponsePreview(strings.Repeat("a", chatNotificationPreviewLimit+50))
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("preview deveria terminar com reticencias: %q", got)
	}
	if n := len([]rune(got)); n != chatNotificationPreviewLimit+1 {
		t.Fatalf("preview truncado com %d runas, esperado %d", n, chatNotificationPreviewLimit+1)
	}
}

func TestTruncateRunesPreservesUTF8(t *testing.T) {
	got := truncateRunes(strings.Repeat("ç", 10), 4)
	if got != "çççç…" {
		t.Fatalf("truncateRunes = %q", got)
	}
}

func TestChatNotificationTextLocalized(t *testing.T) {
	titlePT, bodyPT := chatNotificationText(chatNotifyResponse, "Pacote instalado.", "pt-BR", true)
	if titlePT != "Nova resposta no Chat IA" || bodyPT != "Pacote instalado." {
		t.Fatalf("pt-BR inesperado: %q / %q", titlePT, bodyPT)
	}
	titleEN, bodyEN := chatNotificationText(chatNotifyResponse, "Package installed.", "en-US", true)
	if titleEN != "New reply in AI Chat" || bodyEN != "Package installed." {
		t.Fatalf("en-US inesperado: %q / %q", titleEN, bodyEN)
	}
}

// TestChatNotificationTextPrivacy cobre a preferência de privacidade: sem
// preview o corpo não pode conter nada da resposta.
func TestChatNotificationTextPrivacy(t *testing.T) {
	secret := "senha do cofre: hunter2"
	for _, lang := range []string{"pt-BR", "en-US"} {
		_, body := chatNotificationText(chatNotifyResponse, secret, lang, false)
		if strings.Contains(body, secret) {
			t.Fatalf("corpo sem preview vazou o conteúdo (%s): %q", lang, body)
		}
		if !strings.Contains(body, "chat") {
			t.Fatalf("corpo genérico inesperado (%s): %q", lang, body)
		}
	}
	// Com preview, o trecho aparece (comportamento antigo preservado).
	_, withPreview := chatNotificationText(chatNotifyResponse, secret, "pt-BR", true)
	if !strings.Contains(withPreview, "hunter2") {
		t.Fatalf("preview habilitado deveria mostrar o trecho: %q", withPreview)
	}
}

// TestChatNotificationTextFailureIsGeneric garante que a notificação de falha
// nunca carrega o texto bruto do erro.
func TestChatNotificationTextFailureIsGeneric(t *testing.T) {
	errMsg := "dial tcp 10.0.0.5:443: connect: connection refused (token abc)"
	for _, lang := range []string{"pt-BR", "en-US"} {
		title, body := chatNotificationText(chatNotifyFailure, errMsg, lang, true)
		if strings.Contains(body, "connection refused") || strings.Contains(body, "abc") {
			t.Fatalf("corpo de falha vazou detalhe do erro (%s): %q", lang, body)
		}
		if title == "" || body == "" {
			t.Fatalf("texto de falha vazio (%s)", lang)
		}
	}
}

// fakeSender captura as notificações entregues.
type fakeSender struct {
	sent []wailsnotifications.NotificationOptions
	err  error
}

func (f *fakeSender) send(opts wailsnotifications.NotificationOptions) error {
	f.sent = append(f.sent, opts)
	return f.err
}

func awayState() chatWindowState {
	return chatWindowState{Visible: false, Focused: false}
}

// TestDeliverChatNotificationOncePerAbsence cobre a deduplicação por ausência:
// uma resposta da fila não deve gerar um toast por mensagem, mas voltar a ver o
// chat reabre a janela para a próxima ausência.
func TestDeliverChatNotificationOncePerAbsence(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	sender := &fakeSender{}

	if !a.deliverChatNotification(chatNotifyResponse, "Primeira resposta.", awayState(), false, true, sender.send) {
		t.Fatal("primeira notificação da ausência deveria ser entregue")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("esperado 1 envio, got %d", len(sender.sent))
	}
	opts := sender.sent[0]
	if opts.ID != chatNotificationID {
		t.Fatalf("ID inesperado: %+v", opts)
	}
	// ThreadID ficaria visível como caption do toast no Windows (e o header
	// gerado não preenche "arguments"): não pode ser usado.
	if opts.ThreadID != "" {
		t.Fatalf("ThreadID não deve ser usado: %q", opts.ThreadID)
	}
	if kind, _ := opts.Data["kind"].(string); kind != "chat.response" {
		t.Fatalf("kind inesperado: %+v", opts.Data)
	}
	if !strings.Contains(opts.Body, "Primeira resposta.") {
		t.Fatalf("corpo deveria conter o preview: %q", opts.Body)
	}

	// Segunda resposta da fila, ainda ausente: não notifica de novo.
	if a.deliverChatNotification(chatNotifyResponse, "Segunda resposta.", awayState(), false, true, sender.send) {
		t.Fatal("segunda notificação da mesma ausência deveria ser suprimida")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("esperado 1 envio após supressão, got %d", len(sender.sent))
	}

	// Usuário voltou ao chat (janela focada + aba ativa): encerra a ausência
	// sem enviar nada.
	visible := chatWindowState{Visible: true, Focused: true}
	if a.deliverChatNotification(chatNotifyResponse, "Terceira resposta.", visible, true, true, sender.send) {
		t.Fatal("com o chat em tela não deveria notificar")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("esperado 1 envio com chat visível, got %d", len(sender.sent))
	}

	// Nova ausência: notifica de novo imediatamente (o debounce foi zerado).
	if !a.deliverChatNotification(chatNotifyResponse, "Quarta resposta.", awayState(), false, true, sender.send) {
		t.Fatal("nova ausência deveria notificar")
	}
	if len(sender.sent) != 2 {
		t.Fatalf("esperado 2 envios, got %d", len(sender.sent))
	}
}

// TestDeliverChatNotificationRollsBackOnError garante que uma falha de envio não
// consome a ausência (a próxima resposta tenta de novo).
func TestDeliverChatNotificationRollsBackOnError(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	failing := &fakeSender{err: errFakeSend{}}
	if a.deliverChatNotification(chatNotifyResponse, "x", awayState(), false, true, failing.send) {
		t.Fatal("envio com erro deveria devolver false")
	}
	if a.chatNotifyPending {
		t.Fatal("falha de envio não pode manter a ausência marcada")
	}
}

type errFakeSend struct{}

func (errFakeSend) Error() string { return "envio indisponivel" }

func TestDeliverChatNotificationNilSender(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	if a.deliverChatNotification(chatNotifyResponse, "x", awayState(), false, true, nil) {
		t.Fatal("sender nil não pode entregar")
	}
}

func TestSetChatTabActiveAndNilSafeNotification(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	a.SetChatTabActive(true)
	if !a.chatTabActive.Load() {
		t.Fatal("chatTabActive deveria ser true")
	}
	a.SetChatTabActive(false)
	if a.chatTabActive.Load() {
		t.Fatal("chatTabActive deveria ser false")
	}

	// Sem serviço nativo registrado nada é enviado e nada panica.
	a.notifyChatResponseComplete("resposta")
	a.notifyChatResponseFailed("erro")
	a.sendChatNotification(chatNotifyResponse, "resposta")
	a.SetNativeNotificationService(nil)
	// Lifecycle nil-safe (sem serviço registrado nada acontece).
	a.startNativeNotificationService(context.Background())
	a.stopNativeNotificationService()
	a.openChatFromNotification()
}

func TestNotifyChatResponseCompleteWithoutWindowIsNoop(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	a.sendChatNotification(chatNotifyResponse, "resposta")
	// NewApp não cria janela; a função precisa apenas não panicar.
}

// TestSetChatTabActiveResetsAbsence garante que voltar a ver o chat reabre a
// janela de notificação.
func TestSetChatTabActiveResetsAbsence(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	a.chatNotifyPending = true
	a.chatNotifyLastAt = time.Now()

	a.SetChatTabActive(false)
	if !a.chatNotifyPending {
		t.Fatal("reportar a aba inativa não deve encerrar a ausência")
	}

	a.SetChatTabActive(true)
	if a.chatNotifyPending || !a.chatNotifyLastAt.IsZero() {
		t.Fatal("voltar a ver o chat deve encerrar a ausência e zerar o debounce")
	}
}

// TestNativeNotificationFanOut cobre o fan-out do slot único de
// OnNotificationResponse: todos os handlers registrados recebem.
func TestNativeNotificationFanOut(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	var first, second int
	a.RegisterNativeNotificationHandler(func(wailsnotifications.NotificationResult) { first++ })
	a.RegisterNativeNotificationHandler(func(wailsnotifications.NotificationResult) { second++ })
	a.RegisterNativeNotificationHandler(nil)

	a.dispatchNativeNotification(wailsnotifications.NotificationResult{})
	if first != 1 || second != 1 {
		t.Fatalf("fan-out esperado first=1 second=1, got first=%d second=%d", first, second)
	}
}

func activationArg(t *testing.T, id string) string {
	t.Helper()
	payload := map[string]any{
		"action": "DEFAULT_ACTION",
		"payload": map[string]any{
			"id":    id,
			"title": "Nova resposta no Chat IA",
			"body":  "texto",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestChatActivationArgsMatch(t *testing.T) {
	match := activationArg(t, chatNotificationID)
	if !chatActivationArgsMatch([]string{"--startup-minimized", match}) {
		t.Fatal("payload do chat deveria casar com o ID")
	}
	if chatActivationArgsMatch([]string{"--debug", "nao-e-base64!!"}) {
		t.Fatal("argumento invalido não pode casar")
	}
	if chatActivationArgsMatch([]string{activationArg(t, "outra.notificacao")}) {
		t.Fatal("payload de outro recurso não pode casar")
	}
	if chatActivationArgsMatch(nil) {
		t.Fatal("args vazios não podem casar")
	}
}

func TestHandleActivationArgsSetsPendingBeforeUIRready(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	a.HandleActivationArgs([]string{activationArg(t, chatNotificationID)})
	if !a.pendingChatFocus.Load() {
		t.Fatal("boot vindo de clique em toast deve marcar foco pendente")
	}
	// Primeiro report do frontend consome o pendente (sem janela real o foco
	// é no-op, mas o estado deve ser consumido).
	a.SetChatTabActive(false)
	if a.pendingChatFocus.Load() {
		t.Fatal("pendingChatFocus deveria ser consumido no primeiro report")
	}
	if !a.uiReady.Load() {
		t.Fatal("uiReady deveria ser marcado pelo primeiro report")
	}
}

func TestHandleActivationArgsIgnoresUnrelated(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	a.HandleActivationArgs([]string{"--debug", activationArg(t, "outro.id")})
	if a.pendingChatFocus.Load() {
		t.Fatal("args sem relação com o chat não podem marcar foco pendente")
	}
}
