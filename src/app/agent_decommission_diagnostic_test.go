package app

import (
	"context"
	"strings"
	"testing"

	"discovery/app/core/database"
)

func newTestDecommissionApp(t *testing.T) *App {
	t.Helper()

	db := database.OpenForTest(t)

	app := &App{ctx: context.Background()}
	app.CoreAgent.DB = db
	return app
}

// Sem pedido gravado não há nada a reportar (o watchdog não deve poluir o log).
func TestDescribeAgentDecommission_SemPedido(t *testing.T) {
	app := newTestDecommissionApp(t)

	if got := app.describeAgentDecommission(); got != "" {
		t.Fatalf("esperado vazio sem pedido pendente, veio %q", got)
	}
}

// Pedido gravado e ainda não lançado: o log precisa dizer que existe um pedido
// pendente, senão o operador só vê "comandos=0" e não sabe se o comando chegou.
func TestDescribeAgentDecommission_PedidoPendente(t *testing.T) {
	app := newTestDecommissionApp(t)
	originalElevated := agentDecommissionIsElevated
	t.Cleanup(func() { agentDecommissionIsElevated = originalElevated })

	agentDecommissionIsElevated = func() bool { return true }
	app.markAgentDecommissionRequested("C:/Program Files/Discovery/uninstall.exe")

	got := app.describeAgentDecommission()
	if !strings.Contains(got, "pendente") {
		t.Fatalf("esperado estado pendente, veio %q", got)
	}
	if !strings.Contains(got, "uninstall.exe") {
		t.Fatalf("esperado o caminho do uninstaller, veio %q", got)
	}
	if !strings.Contains(got, "elevacao=true") {
		t.Fatalf("esperado o estado de elevacao, veio %q", got)
	}
}

// Uninstaller lançado mas a máquina continua instalada (ESTE processo vivo): é a
// falha mais confusa de diagnosticar, então precisa aparecer explicitamente.
func TestDescribeAgentDecommission_LancadoMasInstalado(t *testing.T) {
	app := newTestDecommissionApp(t)
	originalElevated := agentDecommissionIsElevated
	t.Cleanup(func() { agentDecommissionIsElevated = originalElevated })

	agentDecommissionIsElevated = func() bool { return true }
	app.markAgentDecommissionRequested("C:/Program Files/Discovery/uninstall.exe")
	app.markAgentDecommissionLaunched()

	got := app.describeAgentDecommission()
	if !strings.Contains(got, "continua instalada") {
		t.Fatalf("esperado aviso de desinstalacao nao concluida, veio %q", got)
	}
}

// App sem banco (ex.: UI companion) não pode entrar em panic.
func TestDescribeAgentDecommission_SemBanco(t *testing.T) {
	if got := (&App{}).describeAgentDecommission(); got != "" {
		t.Fatalf("esperado vazio sem banco, veio %q", got)
	}
}
