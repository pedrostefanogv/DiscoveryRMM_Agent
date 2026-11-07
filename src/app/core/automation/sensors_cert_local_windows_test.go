//go:build windows

package automation

import (
	"strings"
	"testing"
	"time"
)

// -- F6: store de certificados LOCAL + bindings IIS --

// TestParseLocalCertStoreJSON_OrdenaPorUrgencia garante o invariante de que o
// canal primario depende: a lista sai ordenada do que vence PRIMEIRO. Se a
// ordem quebrar, cert_local_days_to_expiry apontaria um certificado saudavel
// enquanto outro vence amanha -- alerta silenciosamente errado.
func TestParseLocalCertStoreJSON_OrdenaPorUrgencia(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	raw := `[
		{"Store":"Root","Subject":"CN=CA longa","Issuer":"CN=CA","Thumbprint":"AA","NotAfter":"2029-01-01T00:00:00Z","HasPrivateKey":false},
		{"Store":"My","Subject":"CN=urgente","Issuer":"CN=CA","Thumbprint":"BB","NotAfter":"2026-11-20T12:00:00Z","HasPrivateKey":true},
		{"Store":"My","Subject":"CN=meio","Issuer":"CN=CA","Thumbprint":"CC","NotAfter":"2026-12-06T12:00:00Z","HasPrivateKey":true}
	]`

	certs := parseLocalCertStoreJSON(raw, now)
	if len(certs) != 3 {
		t.Fatalf("esperado 3 certificados, obtido %d", len(certs))
	}
	if certs[0].Subject != "CN=urgente" {
		t.Errorf("primeiro deveria ser o mais urgente, veio %q", certs[0].Subject)
	}
	if certs[2].Subject != "CN=CA longa" {
		t.Errorf("ultimo deveria ter a validade mais longa, veio %q", certs[2].Subject)
	}
	if certs[0].DaysToExpiry != 14 {
		t.Errorf("DaysToExpiry = %v, esperado 14", certs[0].DaysToExpiry)
	}
}

// TestParseLocalCertStoreJSON_DescartaSemValidadeLegivel: um certificado cuja
// data nao pode ser lida e DESCARTADO, nao reportado como 0 dias. Publicar 0
// criaria alerta falso de 'vence hoje' para um dado que nao conhecemos.
func TestParseLocalCertStoreJSON_DescartaSemValidadeLegivel(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	raw := `[
		{"Store":"My","Subject":"CN=ok","Thumbprint":"AA","NotAfter":"2027-01-01T00:00:00Z"},
		{"Store":"My","Subject":"CN=ruim","Thumbprint":"BB","NotAfter":"nao-e-data"},
		{"Store":"My","Subject":"CN=vazio","Thumbprint":"CC","NotAfter":""}
	]`

	certs := parseLocalCertStoreJSON(raw, now)
	if len(certs) != 1 || certs[0].Subject != "CN=ok" {
		t.Fatalf("apenas o certificado com data legivel deveria sobrar: %+v", certs)
	}
}

// TestParseLocalCertStoreJSON_FormasDegeneradas cobre vazio, lista vazia, null,
// lixo e o OBJETO UNICO que o PowerShell emite quando ha exatamente 1 item.
func TestParseLocalCertStoreJSON_FormasDegeneradas(t *testing.T) {
	now := time.Now()
	for _, raw := range []string{"", "   ", "[]", "null", "lixo"} {
		t.Run(raw, func(t *testing.T) {
			if got := parseLocalCertStoreJSON(raw, now); len(got) != 0 {
				t.Errorf("parseLocalCertStoreJSON(%q) = %d, esperado 0", raw, len(got))
			}
		})
	}

	single := `{"Store":"My","Subject":"CN=solo","Thumbprint":"DD","NotAfter":"2027-01-01T00:00:00Z"}`
	if got := parseLocalCertStoreJSON(single, now); len(got) != 1 {
		t.Errorf("objeto unico deveria virar 1 certificado, veio %d", len(got))
	}
}

// TestNormalizarThumbprint cobre o LEFT-TO-RIGHT MARK (U+200E) que o PowerShell
// prefixa em strings de certificado. Sem normalizar, o cruzamento
// binding<->certificado falharia em silencio e TODO binding apareceria orfao.
func TestNormalizarThumbprint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ABC123", "ABC123"},
		{"abc123", "ABC123"},
		{"‎ABC123", "ABC123"},
		{"‏ABC123", "ABC123"},
		{"AB CD 12", "ABCD12"},
		{"AB\tCD\n12", "ABCD12"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizarThumbprint(tc.in); got != tc.want {
				t.Errorf("normalizarThumbprint(%q) = %q, esperado %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAlvoEhCertStoreLocal garante que a deteccao EXIGE marcador explicito:
// um host/IP/porta NUNCA pode cair no caminho local, senao sensores remotos ja
// configurados mudariam de significado sozinhos.
func TestAlvoEhCertStoreLocal(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{"", true},
		{"local", true},
		{"LOCAL", true},
		{"LocalMachine", true},
		{"  local  ", true},
		{"-", true},
		{".", true},
		{"self", true},
		{"10.0.0.5", false},
		{"host.interno.local", false},
		{"host:443", false},
		{"https://exemplo.com", false},
		{"localhost:8443", false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			if got := alvoEhCertStoreLocal(tc.target); got != tc.want {
				t.Errorf("alvoEhCertStoreLocal(%q) = %v, esperado %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestParseIISBindingsJSON cobre a extracao do nome do site a partir do
// ItemXPath e o descarte de binding sem thumbprint.
func TestParseIISBindingsJSON(t *testing.T) {
	raw := `[
		{"Site":"/system.applicationHost/sites/site[@name='Default Web Site']","Binding":"*:443:","Thumbprint":"‎ABC123"},
		{"Site":"/system.applicationHost/sites/site[@name='Intranet']","Binding":"*:8443:","Thumbprint":"DEF456"},
		{"Site":"/system.applicationHost/sites/site[@name='SemCert']","Binding":"*:80:","Thumbprint":""}
	]`

	bindings := parseIISBindingsJSON(raw)
	if len(bindings) != 2 {
		t.Fatalf("esperado 2 bindings (o sem thumbprint e descartado), obtido %d", len(bindings))
	}
	if bindings[0].Site != "Default Web Site" {
		t.Errorf("nome do site nao extraido do XPath: %q", bindings[0].Site)
	}
	if bindings[0].Thumbprint != "ABC123" {
		t.Errorf("thumbprint nao normalizado: %q", bindings[0].Thumbprint)
	}
	if bindings[1].Site != "Intranet" {
		t.Errorf("segundo site = %q", bindings[1].Site)
	}
}

// TestLimparItemXPath cobre o formato do IIS e entradas que nao sao XPath.
func TestLimparItemXPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/system.applicationHost/sites/site[@name='Default Web Site']", "Default Web Site"},
		{"texto solto", "texto solto"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := limparItemXPath(tc.in); got != tc.want {
				t.Errorf("limparItemXPath(%q) = %q, esperado %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildLocalCertificateCollection_CanaisECruzamentoIIS valida o contrato de
// canais e o cruzamento binding<->certificado, incluindo binding ORFAO.
func TestBuildLocalCertificateCollection_CanaisECruzamentoIIS(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	evidence := certLocalEvidence{
		StoreRead: true,
		Certificates: []localCertificate{
			{Store: "My", Subject: "CN=curto", Thumbprint: "AAA", DaysToExpiry: 5},
			{Store: "Root", Subject: "CN=longo", Thumbprint: "BBB", DaysToExpiry: 900},
		},
		IISBindings: []iisBinding{
			{Site: "Default", Binding: "*:443:", Thumbprint: "AAA"},
			{Site: "Orfao", Binding: "*:8443:", Thumbprint: "ZZZ"},
		},
	}

	collection := buildLocalCertificateCollection(evidence, now)

	if got := collection.Channels[ChannelCertLocalTotalCount]; got != 2 {
		t.Errorf("cert_local_total_count = %v, esperado 2", got)
	}
	if got := collection.Channels[ChannelCertLocalDaysToExpiry]; got != 5 {
		t.Errorf("cert_local_days_to_expiry = %v, esperado 5 (o mais urgente)", got)
	}
	if got := collection.Channels[ChannelCertLocalExpiringCount]; got != 1 {
		t.Errorf("cert_local_expiring_count = %v, esperado 1", got)
	}
	if got := collection.Channels[ChannelCertLocalOK]; got != 0 {
		t.Errorf("cert_local_ok = %v, esperado 0 (5 dias <= 14)", got)
	}
	if got := collection.Channels[ChannelCertIISBoundCount]; got != 2 {
		t.Errorf("cert_iis_bound_count = %v, esperado 2", got)
	}
	// O binding orfao nao entra no calculo: so o AAA casa com um certificado.
	if got := collection.Channels[ChannelCertIISDaysToExpiry]; got != 5 {
		t.Errorf("cert_iis_days_to_expiry = %v, esperado 5", got)
	}
	if got := collection.Channels[ChannelCertCollectOK]; got != 1 {
		t.Errorf("cert_collect_ok = %v, esperado 1", got)
	}

	mensagem := ""
	if collection.Measure.ServiceState != nil {
		mensagem = *collection.Measure.ServiceState
	}
	// A mensagem precisa deixar explicito que um binding aponta para um
	// certificado que NAO esta no store: e um problema diferente de "vence em
	// breve" e o tecnico precisa distinguir os dois.
	if !strings.Contains(mensagem, "ausente do store") {
		t.Errorf("a mensagem deveria citar o binding apontando para certificado ausente: %q", mensagem)
	}
}

// TestBuildLocalCertificateCollection_FalhaDeLeituraNaoMenteParaOK: quando o
// store NAO pode ser lido, cert_collect_ok tem de ser 0. Sem isso o servidor
// leria 'nenhum certificado' como 'esta tudo certo' -- o pior falso negativo
// possivel num sensor de expiracao.
func TestBuildLocalCertificateCollection_FalhaDeLeituraNaoMenteParaOK(t *testing.T) {
	evidence := certLocalEvidence{
		StoreRead: false,
		Errors:    []string{"cert store indisponivel: acesso negado"},
	}
	collection := buildLocalCertificateCollection(evidence, time.Now())

	if got := collection.Channels[ChannelCertCollectOK]; got != 0 {
		t.Errorf("cert_collect_ok = %v, esperado 0 quando a leitura falhou", got)
	}
	if _, existe := collection.Channels[ChannelCertLocalDaysToExpiry]; existe {
		t.Error("nao deve publicar dias quando nao leu o store")
	}
}

// TestCertificadoDoDER_EntradaInvalidaNaoPanica trava a garantia de SEGURANCA
// do F6 (somente leitura, sem chave privada) e a robustez da entrada ruim.
func TestCertificadoDoDER_EntradaInvalidaNaoPanica(t *testing.T) {
	if _, ok := certificadoDoDER([]byte("nao-e-der"), "My", time.Now()); ok {
		t.Error("DER invalido nao pode produzir certificado")
	}
	if _, ok := certificadoDoDER(nil, "My", time.Now()); ok {
		t.Error("entrada vazia nao pode produzir certificado")
	}
}

// TestChannelsF6_NomesExatos trava o CONTRATO com o servidor: estes sao os
// nomes de canal que o servidor recebe. Renomear quebra o outro lado.
func TestChannelsF6_NomesExatos(t *testing.T) {
	want := map[string]string{
		"cert_collect_ok":           ChannelCertCollectOK,
		"cert_local_total_count":    ChannelCertLocalTotalCount,
		"cert_local_days_to_expiry": ChannelCertLocalDaysToExpiry,
		"cert_local_expiring_count": ChannelCertLocalExpiringCount,
		"cert_local_ok":             ChannelCertLocalOK,
		"cert_iis_bound_count":      ChannelCertIISBoundCount,
		"cert_iis_days_to_expiry":   ChannelCertIISDaysToExpiry,
	}
	for esperado, atual := range want {
		if esperado != atual {
			t.Errorf("canal renomeado: esperado %q, constante vale %q", esperado, atual)
		}
	}
}
