//go:build windows

package app

// Toast nativo do Windows para o serviço quando NÃO há UI companion conectada
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C2 — decisão D4).
//
// Comportamento:
//   - Notificação informativa (notify_only): dispara toast nativo e registra
//     persistência (caminho headless do notifications.Service permanece).
//   - Notificação require_confirmation: tenta toast com botões de ação. Toasts
//     SYSTEM na sessão 0 podem não exibir ações (limitação conhecida) — nesse
//     caso o caller cai no caminho headless atual (timeout_policy_applied).
//   - Com UI conectada: serviço NÃO usa toast (a UI renderiza via IPC).
//
// A lib git.sr.ht/~jackmordaunt/go-toast/v2 usa WinRT (wintoast); a resposta
// de ações chega via SetActivationCallback (COM in-process). O callback é
// roteado para o notificationSvc.Respond quando os arguments codificam
// "notification:<id>:<result>".

import (
	"fmt"
	"strings"
	"sync"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"

	"discovery/app/agentconfig"
	"discovery/app/services/notifications"
)

const (
	// toastAppID é o AppID registrado no Action Center.
	toastAppID = "Discovery.Agent"

	// toastArgsPrefix codifica arguments "notification:<id>:<result>".
	toastArgsPrefix = "notification:"
)

var (
	toastInitOnce       sync.Once
	toastActivationHook func(notificationID, result string)
	toastMu             sync.Mutex
)

// initToastOnce registra o AppData e o activation callback uma única vez por
// processo. O callback roteia a resposta para o hook ativo (o notificationSvc
// do App, quando existir).
func initToastOnce() {
	toastInitOnce.Do(func() {
		_ = toast.SetAppData(toast.AppData{AppID: toastAppID})
		toast.SetActivationCallback(func(args string, _ []toast.UserData) {
			if !strings.HasPrefix(args, toastArgsPrefix) {
				return
			}
			parts := strings.SplitN(strings.TrimPrefix(args, toastArgsPrefix), ":", 2)
			if len(parts) != 2 {
				return
			}
			toastMu.Lock()
			hook := toastActivationHook
			toastMu.Unlock()
			if hook != nil {
				hook(parts[0], parts[1])
			}
		})
	})
}

// setToastActivationHook conecta o callback de ativação do toast ao
// notificationSvc (respond). Chamado pelo App no modo serviço.
func setToastActivationHook(fn func(notificationID, result string)) {
	toastMu.Lock()
	defer toastMu.Unlock()
	toastActivationHook = fn
}

// pushNativeToast envia um toast nativo do Windows.
// actions: lista de ações opcionais (label, value). Cada botão codifica a
// resposta como arguments "notification:<id>:<value>".
// Retorna erro quando o toast não pôde ser enviado (caller cai no headless).
func pushNativeToast(notificationID, title, message string, actions [][2]string) error {
	initToastOnce()
	n := toast.Notification{
		AppID: toastAppID,
		Title: strings.TrimSpace(title),
		Body:  strings.TrimSpace(message),
		Audio: toast.Default,
		Actions: func() []toast.Action {
			out := make([]toast.Action, 0, len(actions))
			for _, a := range actions {
				out = append(out, toast.Action{
					Type:      toast.Protocol,
					Content:   a[0],
					Arguments: fmt.Sprintf("%s%s:%s", toastArgsPrefix, notificationID, a[1]),
				})
			}
			return out
		}(),
	}
	return n.Push()
}

// dispatchNativeToastWhenHeadless é chamado pelo notificationSvc no modo
// serviço quando NÃO há UI conectada: dispara toast nativo e conecta a
// resposta ao serviço de notificações. Ações (require_confirmation) são
// codificadas via Metadata["actions"] quando presentes pelo dispatcher; se o
// toast falhar (ex.: ações não suportadas na sessão 0), o caller cai no
// timeout_policy_applied.
func dispatchNativeToastWhenHeadless(req notifications.DispatchRequest) {
	// Ações vindas no metadata (label:value) — contratado pelo notificationSvc.
	// M2: aceita []any (formato normalizado) e []agentconfig.AgentNotificationAction
	// (tipo concreto que podia ser injetado direto no metadata).
	var actions [][2]string
	switch md := req.Metadata["actions"].(type) {
	case []any:
		for _, item := range md {
			if m, ok := item.(map[string]any); ok {
				label, _ := m["label"].(string)
				value, _ := m["value"].(string)
				if label != "" && value != "" {
					actions = append(actions, [2]string{label, value})
				}
			}
		}
	case []agentconfig.AgentNotificationAction:
		for _, a := range md {
			if a.Label != "" && a.ID != "" {
				actions = append(actions, [2]string{a.Label, a.ID})
			}
		}
	}
	_ = pushNativeToast(req.NotificationID, req.Title, req.Message, actions)
}
