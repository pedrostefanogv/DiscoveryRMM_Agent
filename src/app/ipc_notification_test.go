package app

// Testes do ciclo de notificação via IPC (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2 + implementação 2026-09-04).

import (
	"testing"
)

func TestHandleIPCNotificationRespondNilGuards(t *testing.T) {
	// App nil, service nil e payload nil não podem panic.
	var aNil *App
	if aNil.handleIPCNotificationRespond(nil) {
		t.Fatal("App nil deve retornar false")
	}
	a := &App{}
	if a.handleIPCNotificationRespond(map[string]any{"notificationId": "n1", "result": "approved"}) {
		t.Fatal("notificationSvc nil deve retornar false")
	}
	if a.handleIPCNotificationRespond(nil) {
		t.Fatal("payload nil deve retornar false")
	}
}

func TestHandleIPCNotificationRespondPayloadValidation(t *testing.T) {
	a := &App{} // notificationSvc nil — validação de payload vem antes
	if a.handleIPCNotificationRespond(map[string]any{"notificationId": "", "result": "approved"}) {
		t.Fatal("notificationId vazio deve retornar false")
	}
	if a.handleIPCNotificationRespond(map[string]any{"notificationId": "n1", "result": ""}) {
		t.Fatal("result vazio deve retornar false")
	}
}

func TestBroadcastIPCEventNilSafe(t *testing.T) {
	var aNil *App
	aNil.broadcastIPCEvent("test")        // não pode panic
	a := &App{}
	a.broadcastIPCEvent("test", "k", "v") // ipcServer nil — não pode panic
}
