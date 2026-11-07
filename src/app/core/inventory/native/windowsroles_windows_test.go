//go:build windows

package native

import (
	"encoding/json"
	"strings"
	"testing"
)

// mustJSON serializa o valor para JSON em teste. Falha o teste em vez de
// devolver erro: uma serialização quebrada É o defeito que estes testes
// existem para detectar.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal falhou: %v", err)
	}
	return string(encoded)
}

// containsSubstring é um atalho legível para os testes de contrato de payload.
func containsSubstring(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestParseWindowsFeatureJSON_Array cobre o caminho normal: o cmdlet devolve um
// array de features, cada uma com Name/DisplayName/Installed/InstallState.
func TestParseWindowsFeatureJSON_Array(t *testing.T) {
	raw := `[
  {"Name":"AD-Domain-Services","DisplayName":"Servicos de Dominio do Active Directory","Installed":true,"InstallState":"Installed"},
  {"Name":"DNS","DisplayName":"Servidor DNS","Installed":true,"InstallState":"Installed"},
  {"Name":"DHCP","DisplayName":"Servidor DHCP","Installed":false,"InstallState":"Available"}
]`

	roles := parseWindowsFeatureJSON(raw)
	if len(roles) != 3 {
		t.Fatalf("esperado 3 roles, obtido %d", len(roles))
	}

	// Ordenação determinística por Name (case-insensitive): AD, DHCP, DNS.
	wantOrder := []string{"AD-Domain-Services", "DHCP", "DNS"}
	for i, want := range wantOrder {
		if roles[i].Name != want {
			t.Errorf("posicao %d: Name = %q, esperado %q (ordem deve ser estavel)", i, roles[i].Name, want)
		}
	}
}

// TestParseWindowsFeatureJSON_SingleObject: com exatamente 1 feature o
// ConvertTo-Json emite um OBJETO, não um array. Sem tratar isso, uma máquina
// com uma única role reportaria zero.
func TestParseWindowsFeatureJSON_SingleObject(t *testing.T) {
	raw := `{"Name":"Hyper-V","DisplayName":"Hyper-V","Installed":true,"InstallState":"Installed"}`

	roles := parseWindowsFeatureJSON(raw)
	if len(roles) != 1 {
		t.Fatalf("esperado 1 role, obtido %d", len(roles))
	}
	if roles[0].Name != "Hyper-V" || !roles[0].Installed {
		t.Errorf("role inesperada: %+v", roles[0])
	}
}

// TestParseWindowsFeatureJSON_EmptyAndNull cobre as saídas degeneradas: vazio,
// "[]" e "null" devem virar lista vazia, nunca panic nem linha fantasma.
func TestParseWindowsFeatureJSON_EmptyAndNull(t *testing.T) {
	for _, raw := range []string{"", "   ", "[]", "null", "  []  ", "nao-e-json"} {
		t.Run(raw, func(t *testing.T) {
			if roles := parseWindowsFeatureJSON(raw); len(roles) != 0 {
				t.Errorf("parseWindowsFeatureJSON(%q) = %d roles, esperado 0", raw, len(roles))
			}
		})
	}
}

// TestParseWindowsFeatureJSON_SkipsNamelessAndFillsDisplay: o Name é a chave do
// inventário — linha sem Name é descartada. E feature sem DisplayName recebe o
// próprio Name como rótulo, em vez de aparecer em branco na UI.
func TestParseWindowsFeatureJSON_SkipsNamelessAndFillsDisplay(t *testing.T) {
	raw := `[
  {"Name":"","DisplayName":"Sem nome","Installed":true,"InstallState":"Installed"},
  {"Name":"IIS-WebServer","DisplayName":"","Installed":true,"InstallState":"Installed"},
  {"Name":"   ","DisplayName":"so espacos","Installed":false,"InstallState":"Available"}
]`

	roles := parseWindowsFeatureJSON(raw)
	if len(roles) != 1 {
		t.Fatalf("esperado 1 role (as sem Name sao descartadas), obtido %d", len(roles))
	}
	if roles[0].Name != "IIS-WebServer" {
		t.Errorf("Name = %q, esperado IIS-WebServer", roles[0].Name)
	}
	if roles[0].DisplayName != "IIS-WebServer" {
		t.Errorf("DisplayName = %q, esperado fallback para o Name", roles[0].DisplayName)
	}
}

// TestParseWindowsFeatureJSON_BoolCoercion: um $true serializado como string
// ("True") ou como número (1) NÃO pode virar "não instalada" silenciosamente —
// isso faria a role sumir do inventário sem nenhum erro visível.
func TestParseWindowsFeatureJSON_BoolCoercion(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"bool nativo true", `[{"Name":"A","Installed":true}]`, true},
		{"bool nativo false", `[{"Name":"A","Installed":false}]`, false},
		{"string True", `[{"Name":"A","Installed":"True"}]`, true},
		{"string true minusculo", `[{"Name":"A","Installed":"true"}]`, true},
		{"string FALSE", `[{"Name":"A","Installed":"FALSE"}]`, false},
		{"numero 1", `[{"Name":"A","Installed":1}]`, true},
		{"numero 0", `[{"Name":"A","Installed":0}]`, false},
		{"ausente", `[{"Name":"A"}]`, false},
		{"nulo", `[{"Name":"A","Installed":null}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roles := parseWindowsFeatureJSON(tc.raw)
			if len(roles) != 1 {
				t.Fatalf("esperado 1 role, obtido %d", len(roles))
			}
			if roles[0].Installed != tc.want {
				t.Errorf("Installed = %v, esperado %v", roles[0].Installed, tc.want)
			}
		})
	}
}

// TestParseWindowsFeatureJSON_RespectsLimit garante o teto de sanidade: uma
// saída anormalmente grande não pode virar payload sem limite.
func TestParseWindowsFeatureJSON_RespectsLimit(t *testing.T) {
	raw := "["
	for i := 0; i < maxWindowsRoles+50; i++ {
		if i > 0 {
			raw += ","
		}
		raw += `{"Name":"Role-` + padRoleIndex(i) + `","Installed":true}`
	}
	raw += "]"

	roles := parseWindowsFeatureJSON(raw)
	if len(roles) != maxWindowsRoles {
		t.Errorf("esperado teto de %d roles, obtido %d", maxWindowsRoles, len(roles))
	}
}

// padRoleIndex gera um sufixo numérico de largura fixa para os nomes sintéticos
// do teste de limite (mantém a ordem lexicográfica previsível).
func padRoleIndex(i int) string {
	s := ""
	for n := i; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// TestParseDismFeaturesOutput cobre o fallback DISM, cujo formato é textual e
// alinhado com padding — o parser precisa tolerar espaços irregulares.
func TestParseDismFeaturesOutput(t *testing.T) {
	raw := "Deployment Image Servicing and Management tool\r\n" +
		"Version: 10.0.20348.1\r\n" +
		"\r\n" +
		"Features listing for package : Microsoft-Windows-ServerCore-Package~31bf3856ad364e35~amd64~~10.0.20348.1\r\n" +
		"\r\n" +
		"Feature Name : AD-Domain-Services\r\n" +
		"State : Enabled\r\n" +
		"\r\n" +
		"Feature Name : IIS-WebServerRole\r\n" +
		"State : Disabled\r\n" +
		"\r\n" +
		"Feature Name :   FailoverCluster   \r\n" +
		"State :   Enabled   \r\n"

	roles := parseDismFeaturesOutput(raw)
	if len(roles) != 3 {
		t.Fatalf("esperado 3 features, obtido %d", len(roles))
	}

	byName := map[string]WindowsRole{}
	for _, r := range roles {
		byName[r.Name] = r
	}

	if r, ok := byName["AD-Domain-Services"]; !ok || !r.Installed {
		t.Errorf("AD-Domain-Services deveria estar instalada: %+v", r)
	}
	if r, ok := byName["IIS-WebServerRole"]; !ok || r.Installed {
		t.Errorf("IIS-WebServerRole esta Disabled e nao pode contar como instalada: %+v", r)
	}
	// Espaços em volta do nome/estado devem ser removidos pelo parser.
	if r, ok := byName["FailoverCluster"]; !ok || !r.Installed {
		t.Errorf("FailoverCluster deveria estar instalada apos trim: %+v", r)
	}

	// Ordenação estável também no caminho DISM.
	wantOrder := []string{"AD-Domain-Services", "FailoverCluster", "IIS-WebServerRole"}
	for i, want := range wantOrder {
		if roles[i].Name != want {
			t.Errorf("posicao %d: Name = %q, esperado %q", i, roles[i].Name, want)
		}
	}
}

// TestParseDismFeaturesOutput_IgnoresNoise garante que cabeçalhos, linhas de
// versão e saída vazia não produzam features fantasma.
func TestParseDismFeaturesOutput_IgnoresNoise(t *testing.T) {
	raw := "Deployment Image Servicing and Management tool\r\n" +
		"Version: 10.0.20348.1\r\n" +
		"\r\n" +
		"Error: 740\r\n" +
		"\r\n"

	if roles := parseDismFeaturesOutput(raw); len(roles) != 0 {
		t.Errorf("saida sem features deveria devolver 0, obtido %d: %+v", len(roles), roles)
	}
}

// TestParseDismFeaturesOutput_StateWithoutFeatureName: um "State :" solto (sem
// um "Feature Name :" anterior) não pode gerar uma role sem nome.
func TestParseDismFeaturesOutput_StateWithoutFeatureName(t *testing.T) {
	raw := "State : Enabled\r\nFeature Name : DNS\r\nState : Enabled\r\n"

	roles := parseDismFeaturesOutput(raw)
	if len(roles) != 1 {
		t.Fatalf("esperado 1 feature, obtido %d: %+v", len(roles), roles)
	}
	if roles[0].Name != "DNS" || !roles[0].Installed {
		t.Errorf("feature inesperada: %+v", roles[0])
	}
}

// TestSplitDismLine cobre o separador chave/valor com padding irregular.
func TestSplitDismLine(t *testing.T) {
	cases := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"Feature Name : DNS", "Feature Name", "DNS", true},
		{"Feature Name :   DNS  ", "Feature Name", "DNS", true},
		{"State:Enabled", "State", "Enabled", true},
		{"State :  Enabled", "State", "Enabled", true},
		{"sem dois pontos", "", "", false},
		{"Feature Name : ", "", "", false},
		{" : valor", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			key, value, ok := splitDismLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, esperado %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if key != tc.wantKey || value != tc.wantValue {
				t.Errorf("got (%q, %q), esperado (%q, %q)", key, value, tc.wantKey, tc.wantValue)
			}
		})
	}
}

// TestSortWindowsRolesByName_CaseInsensitiveAndStable garante que a ordenação
// ignora caixa (senão "ad-ds" e "AD-DS" ficariam em blocos separados) e que
// nomes distintos por caixa têm desempate determinístico.
func TestSortWindowsRolesByName_CaseInsensitiveAndStable(t *testing.T) {
	roles := []WindowsRole{
		{Name: "dns"},
		{Name: "AD-Domain-Services"},
		{Name: "DHCP"},
		{Name: "ad-ds"},
	}

	sortWindowsRolesByName(roles)

	want := []string{"AD-Domain-Services", "ad-ds", "DHCP", "dns"}
	for i, w := range want {
		if roles[i].Name != w {
			t.Errorf("posicao %d: %q, esperado %q", i, roles[i].Name, w)
		}
	}
}

// TestWindowsRolesResult_JSONShape trava o CONTRATO com o servidor: os nomes
// exatos dos campos JSON. Renomear qualquer um quebra o outro lado, então este
// teste existe para que a quebra seja detectada aqui e não em produção.
func TestWindowsRolesResult_JSONShape(t *testing.T) {
	res := WindowsRolesResult{
		Roles: []WindowsRole{{
			Name:         "DNS",
			DisplayName:  "Servidor DNS",
			Installed:    true,
			InstallState: "Installed",
		}},
		Source: "Get-WindowsFeature",
	}

	encoded := mustJSON(t, res)

	for _, want := range []string{
		`"roles"`, `"name"`, `"displayName"`, `"installed"`, `"installState"`, `"source"`,
		`"DNS"`, `"Servidor DNS"`, `"Get-WindowsFeature"`,
	} {
		if !containsSubstring(encoded, want) {
			t.Errorf("payload nao contem %s: %s", want, encoded)
		}
	}
}

// TestWindowsRolesResult_JSONOmitsErrorOnSuccess garante que o sucesso não
// emite "error" vazio (omitempty) — o servidor distingue sucesso de falha pela
// presença do campo.
func TestWindowsRolesResult_JSONOmitsErrorOnSuccess(t *testing.T) {
	encoded := mustJSON(t, WindowsRolesResult{Roles: []WindowsRole{{Name: "DNS"}}})
	if containsSubstring(encoded, `"error"`) {
		t.Errorf("sucesso nao deveria emitir campo error: %s", encoded)
	}
}
