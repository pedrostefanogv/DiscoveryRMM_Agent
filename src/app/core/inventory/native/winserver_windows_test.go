//go:build windows

package native

import "testing"

// TestNormalizeProductType cobre os valores canônicos do Windows, variações de
// caixa/espaço e entradas inválidas — a normalização nunca deve "adivinhar":
// qualquer coisa desconhecida vira "unknown".
func TestNormalizeProductType(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"workstation canônico", "WinNT", ProductTypeWorkstation},
		{"servidor membro canônico", "ServerNT", ProductTypeServerNT},
		{"controlador de domínio canônico", "LanmanNT", ProductTypeLanmanNT},
		{"caixa baixa", "winnt", ProductTypeWorkstation},
		{"caixa alta", "SERVERNT", ProductTypeServerNT},
		{"com espaços em volta", "  LanmanNT  ", ProductTypeLanmanNT},
		{"vazio", "", ProductTypeUnknown},
		{"só espaços", "   ", ProductTypeUnknown},
		{"valor inventado", "Server", ProductTypeUnknown},
		{"parecido mas errado", "WinNT5", ProductTypeUnknown},
		{"nulo textual", "null", ProductTypeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeProductType(tc.raw); got != tc.want {
				t.Errorf("normalizeProductType(%q) = %q, esperado %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestIsServerProductType garante que cliente NÃO é servidor e que membro e DC
// são — regra que o agente inteiro (inclusive windowsupdate) depende.
func TestIsServerProductType(t *testing.T) {
	cases := []struct {
		productType string
		want        bool
	}{
		{ProductTypeWorkstation, false},
		{ProductTypeServerNT, true},
		{ProductTypeLanmanNT, true},
		{ProductTypeUnknown, false},
		{"", false},
		{"servernot", false}, // comparar cru não é aceito: normalizar antes
	}
	for _, tc := range cases {
		t.Run(tc.productType, func(t *testing.T) {
			if got := isServerProductType(tc.productType); got != tc.want {
				t.Errorf("isServerProductType(%q) = %v, esperado %v", tc.productType, got, tc.want)
			}
		})
	}
}

// TestIsDomainControllerProductType: somente LanmanNT é DC.
func TestIsDomainControllerProductType(t *testing.T) {
	cases := []struct {
		productType string
		want        bool
	}{
		{ProductTypeWorkstation, false},
		{ProductTypeServerNT, false},
		{ProductTypeLanmanNT, true},
		{ProductTypeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.productType, func(t *testing.T) {
			if got := isDomainControllerProductType(tc.productType); got != tc.want {
				t.Errorf("isDomainControllerProductType(%q) = %v, esperado %v", tc.productType, got, tc.want)
			}
		})
	}
}

// TestResolveDomainState cobre a normalização Domain x Workgroup a partir do
// que o Win32_ComputerSystem devolve.
func TestResolveDomainState(t *testing.T) {
	cases := []struct {
		name string
		in   computerSystemState
		want domainState
	}{
		{
			name: "cliente em domínio",
			in:   computerSystemState{Domain: "corp.local", PartOfDomain: true},
			want: domainState{Domain: "corp.local", PartOfDomain: true},
		},
		{
			name: "cliente em domínio com workgroup preenchido",
			in:   computerSystemState{Domain: "corp.local", Workgroup: "WG", PartOfDomain: true},
			want: domainState{Domain: "corp.local", Workgroup: "WG", PartOfDomain: true},
		},
		{
			name: "cliente fora de domínio (WMI põe o grupo em Domain)",
			in:   computerSystemState{Domain: "WORKGROUP"},
			want: domainState{Workgroup: "WORKGROUP"},
		},
		{
			name: "fora de domínio com Workgroup explícito",
			in:   computerSystemState{Domain: "MEUGRUPO", Workgroup: "OUTRO"},
			want: domainState{Workgroup: "OUTRO"},
		},
		{
			name: "servidor membro em domínio",
			in:   computerSystemState{Domain: "empresa.com.br", PartOfDomain: true},
			want: domainState{Domain: "empresa.com.br", PartOfDomain: true},
		},
		{
			name: "sem dados (falha do WMI)",
			in:   computerSystemState{},
			want: domainState{},
		},
		{
			name: "espaços são aparados",
			in:   computerSystemState{Domain: "  corp.local  ", PartOfDomain: true},
			want: domainState{Domain: "corp.local", PartOfDomain: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDomainState(tc.in)
			if got != tc.want {
				t.Errorf("resolveDomainState(%+v) = %+v, esperado %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveDomainStateNuncaPublicaWorkgroupEmDomain garante a regra-chave:
// fora de domínio, Domain fica vazio (o servidor não deve confundir grupo de
// trabalho com domínio).
func TestResolveDomainStateNuncaPublicaWorkgroupEmDomain(t *testing.T) {
	got := resolveDomainState(computerSystemState{Domain: "WORKGROUP", Workgroup: "WORKGROUP"})
	if got.Domain != "" {
		t.Fatalf("Domain = %q, esperado vazio fora de domínio", got.Domain)
	}
	if got.Workgroup != "WORKGROUP" {
		t.Fatalf("Workgroup = %q, esperado WORKGROUP", got.Workgroup)
	}
	if got.PartOfDomain {
		t.Fatalf("PartOfDomain deveria ser false")
	}
}

// TestFillWindowsServerFields_NilSafe: o helper não pode estourar com ponteiro
// nulo (chamado no coletor com best-effort).
func TestFillWindowsServerFields_NilSafe(t *testing.T) {
	fillWindowsServerFields(nil) // não deve entrar em panic
}

// TestDetectProductType_NaoVazio em qualquer máquina: o valor lido do registro
// precisa ser um dos canônicos — nunca vazio.
func TestDetectProductType_NaoVazio(t *testing.T) {
	got := detectProductType()
	switch got {
	case ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown:
		// ok
	default:
		t.Fatalf("detectProductType() = %q, esperado um valor canônico", got)
	}
}
