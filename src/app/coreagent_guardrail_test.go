package app

// coreagent_guardrail_test.go — guardrail da Fase A
// (PLANO_SEPARACAO_SERVICO_UI.md): valida que o caminho de startup do modo
// serviço não depende de Wails em runtime (a App criada com ServiceMode=true
// nunca toca em a.app/a.mainWindow/a.systemTray, que são nil no serviço).

import (
	"context"
	"os"
	"testing"
)

// TestServiceModeAppHasNoWailsRefs verifica que uma App em modo serviço não
// inicializa nenhum componente Wails (app, mainWindow, systemTray permanecem
// nil até que SetApplication/SetMainWindow sejam chamados pela UI).
func TestServiceModeAppHasNoWailsRefs(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	if a.app != nil {
		t.Fatal("App em modo serviço não deve ter application Wails")
	}
	if a.mainWindow != nil {
		t.Fatal("App em modo serviço não deve ter mainWindow")
	}
	if a.systemTray != nil {
		t.Fatal("App em modo serviço não deve ter systemTray")
	}
	if !a.RuntimeFlags.ServiceMode {
		t.Fatal("runtimeFlags.ServiceMode deve estar true")
	}
}

// TestEmitEventHeadlessNoPanic garante que EmitEvent sem UI não panica
// (repassa ao IPC que, sem servidor/cliente, é no-op).
func TestEmitEventHeadlessNoPanic(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	a.EmitEvent("test:event", map[string]any{"k": "v"})
}

// TestRunCoreContextCancellation valida que RunCore aceita um ctx cancelado
// sem bloquear indefinidamente (o staged startup respeita ctx.Done).
//
// NOTA: roda apenas quando DISCOVERY_INTEGRATION_TESTS=1 — RunCore abre o
// discovery.db real em ProgramData (side effect de ambiente) e inicia o
// IPC server no named pipe. O guardrail de "não bloquear com ctx cancelado"
// é melhor verificado no smoke do serviço (checklist manual, Fase E.2).
func TestRunCoreContextCancellation(t *testing.T) {
	if os.Getenv("DISCOVERY_INTEGRATION_TESTS") != "1" {
		t.Skip("pula: RunCore tem side effects de ambiente (DB em ProgramData + named pipe); defina DISCOVERY_INTEGRATION_TESTS=1")
	}
	a := NewApp(AppStartupOptions{ServiceMode: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancela imediatamente — core não deve travar
	// RunCore dispara goroutines staged que saem cedo com ctx cancelado.
	_ = a.RunCore(ctx)
}
