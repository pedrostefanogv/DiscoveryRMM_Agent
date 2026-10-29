package windowsupdate

import (
	"strings"
	"testing"
)

// TestPolicyRegistryPathsAreWellFormed protege contra o bug historico em que os
// caminhos de politica foram escritos sem separador ("SOFTWAREPolicies..."), o
// que fazia applyPolicy operar numa chave inexistente.
func TestPolicyRegistryPathsAreWellFormed(t *testing.T) {
	for name, path := range map[string]string{
		"WU": regPathWUPolicies,
		"AU": regPathWUPoliciesAU,
	} {
		if !strings.HasPrefix(path, `SOFTWARE\`) {
			t.Errorf("%s: caminho %q nao comeca com SOFTWARE\\", name, path)
		}
		if strings.Contains(path, "SOFTWAREPolicies") {
			t.Errorf("%s: caminho %q tem segmentos concatenados sem separador", name, path)
		}
		if !strings.Contains(path, `\Policies\Microsoft\Windows\`) {
			t.Errorf("%s: caminho %q nao aponta para a arvore de Policies", name, path)
		}
		if strings.HasSuffix(path, `\`) || strings.HasSuffix(path, "/") {
			t.Errorf("%s: caminho %q termina com separador", name, path)
		}
	}

	if !strings.HasSuffix(regPathWUPolicies, `\WindowsUpdate`) {
		t.Errorf("regPathWUPolicies deveria terminar em WindowsUpdate: %q", regPathWUPolicies)
	}
	if regPathWUPoliciesAU != regPathWUPolicies+`\AU` {
		t.Errorf("regPathWUPoliciesAU deveria ser filho de regPathWUPolicies: %q vs %q", regPathWUPoliciesAU, regPathWUPolicies)
	}
}

func TestPolicyScopePath(t *testing.T) {
	if got, ok := policyScopePath(policyScopeAU); !ok || got != regPathWUPoliciesAU {
		t.Fatalf("policyScopePath(AU) = %q,%v", got, ok)
	}
	if got, ok := policyScopePath(policyScopeWU); !ok || got != regPathWUPolicies {
		t.Fatalf("policyScopePath(WU) = %q,%v", got, ok)
	}
	if _, ok := policyScopePath("nope"); ok {
		t.Fatal("policyScopePath deveria recusar escopo desconhecido")
	}
}
