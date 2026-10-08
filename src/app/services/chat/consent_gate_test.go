package chat

import (
	"context"
	"strings"
	"testing"

	"discovery/app/core/consent"
	"discovery/app/core/mcp"
)

// Regressão: tools destrutivas eram executadas com a única "confirmação"
// sendo o parâmetro confirm=true preenchido pelo próprio LLM. O gate agora
// consulta o usuário ANTES da execução.
func newConsentTestRegistry(called *int) *mcp.Registry {
	reg := mcp.NewRegistry()
	reg.Register(mcp.Tool{
		Name: "install_package",
		Handler: func(_ context.Context, _ map[string]any) (any, error) {
			*called++
			return map[string]any{"ok": true}, nil
		},
	})
	return reg
}

func TestToolConsentGateDeniesAndDoesNotExecute(t *testing.T) {
	called := 0
	reg := newConsentTestRegistry(&called)
	var received consent.Request
	s := New(reg, Deps{
		RequestToolConsent: func(_ context.Context, req consent.Request) (bool, error) {
			received = req
			return false, nil
		},
	})

	out, err := s.mcpExecuteForChat(context.Background(), "install_package", `{"id":"X"}`)
	if err != nil {
		t.Fatalf("negativa não deveria virar erro: %v", err)
	}
	if called != 0 {
		t.Fatalf("tool executou SEM autorização (called=%d)", called)
	}
	if received.Action != "install_package" {
		t.Fatalf("pedido de consentimento errado: %#v", received)
	}
	if !strings.Contains(out, `"approved":false`) {
		t.Fatalf("resultado deveria trazer approved=false: %s", out)
	}
}

func TestToolConsentGateAllowsExecution(t *testing.T) {
	called := 0
	reg := newConsentTestRegistry(&called)
	s := New(reg, Deps{
		RequestToolConsent: func(_ context.Context, _ consent.Request) (bool, error) { return true, nil },
	})

	if _, err := s.mcpExecuteForChat(context.Background(), "install_package", `{"id":"X"}`); err != nil {
		t.Fatalf("execução autorizada falhou: %v", err)
	}
	if called != 1 {
		t.Fatalf("tool deveria executar após autorização (called=%d)", called)
	}
}

// Fail-closed: sem prompter configurado, uma tool que exige consentimento NÃO
// executa (não há como pedir autorização).
func TestToolConsentGateFailsClosedWithoutPrompter(t *testing.T) {
	called := 0
	reg := newConsentTestRegistry(&called)
	s := New(reg, Deps{})

	if _, err := s.mcpExecuteForChat(context.Background(), "install_package", `{"id":"X"}`); err == nil {
		t.Fatal("sem prompter a tool com consentimento deveria ser recusada")
	}
	if called != 0 {
		t.Fatalf("tool executou sem prompter (called=%d)", called)
	}
}

func TestToolConsentGateSkipsReadOnlyTools(t *testing.T) {
	called := 0
	reg := mcp.NewRegistry()
	reg.Register(mcp.Tool{
		Name: "get_inventory",
		Handler: func(_ context.Context, _ map[string]any) (any, error) {
			called++
			return map[string]any{}, nil
		},
	})
	asked := false
	s := New(reg, Deps{
		RequestToolConsent: func(_ context.Context, _ consent.Request) (bool, error) {
			asked = true
			return false, nil
		},
	})

	if _, err := s.mcpExecuteForChat(context.Background(), "get_inventory", ""); err != nil {
		t.Fatalf("tool de leitura falhou: %v", err)
	}
	if asked {
		t.Fatal("tool de leitura não deveria pedir consentimento")
	}
	if called != 1 {
		t.Fatalf("tool de leitura deveria executar (called=%d)", called)
	}
}

// A aprovação do usuário precisa satisfazer o confirm=true exigido pelos
// handlers destrutivos. Sem isso o Registry.Call recusava a chamada por
// "parametro obrigatorio 'confirm' ausente" DEPOIS da autorização — o LLM
// repetia a chamada e o usuário recebia um SEGUNDO pedido de consentimento.
func TestToolConsentGateInjectsConfirmAfterApproval(t *testing.T) {
	var gotConfirm any
	reg := mcp.NewRegistry()
	reg.Register(mcp.Tool{
		Name: "uninstall_package",
		Params: []mcp.ToolParam{
			{Name: "id", Type: "string", Required: true},
			{Name: "confirm", Type: "boolean", Required: true},
		},
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			gotConfirm = args["confirm"]
			return map[string]any{"ok": true}, nil
		},
	})
	s := New(reg, Deps{
		RequestToolConsent: func(_ context.Context, _ consent.Request) (bool, error) { return true, nil },
	})

	if _, err := s.mcpExecuteForChat(context.Background(), "uninstall_package", `{"id":"7zip.7zip"}`); err != nil {
		t.Fatalf("execução autorizada falhou: %v", err)
	}
	if gotConfirm != true {
		t.Fatalf("confirm deveria ser true após aprovação, got %#v", gotConfirm)
	}
}

// JSON malformado não pode ganhar confirm=true silencioso: o erro claro do
// Registry.Call deve continuar chegando ao LLM.
func TestToolConsentGateKeepsMalformedArgsError(t *testing.T) {
	called := 0
	reg := newConsentTestRegistry(&called)
	s := New(reg, Deps{
		RequestToolConsent: func(_ context.Context, _ consent.Request) (bool, error) { return true, nil },
	})

	if _, err := s.mcpExecuteForChat(context.Background(), "install_package", "{invalido"); err == nil {
		t.Fatal("args malformado deveria continuar devolvendo erro")
	}
	if called != 0 {
		t.Fatalf("handler não deveria rodar com args malformado (called=%d)", called)
	}
}
