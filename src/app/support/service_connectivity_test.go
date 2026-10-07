package support

import (
	"errors"
	"testing"
)

// Item 4: a queda do transporte NATS coloca o suporte em somente-consulta
// imediatamente, sem esperar uma operação falhar.
func TestNotifyConnectivity_MarksOffline(t *testing.T) {
	svc := NewService(Options{DB: newMemCacheDB()})
	if svc.isOffline() {
		t.Fatal("serviço deveria iniciar online")
	}

	svc.NotifyConnectivity(false)
	if !svc.isOffline() {
		t.Fatal("NotifyConnectivity(false) deveria marcar offline")
	}
	if err := svc.ensureOnline("abrir chamado"); !errors.Is(err, ErrOfflineReadOnly) {
		t.Fatalf("ensureOnline deveria bloquear offline, veio: %v", err)
	}

	// A volta do transporte NÃO força reachable: quem reabre é uma operação
	// bem-sucedida (evita mascarar falha da API HTTP).
	svc.NotifyConnectivity(true)
	if !svc.isOffline() {
		t.Fatal("NotifyConnectivity(true) não deve forçar reachable")
	}
	svc.markReachable()
	if svc.isOffline() {
		t.Fatal("operação bem-sucedida deveria reabrir a escrita")
	}
}
