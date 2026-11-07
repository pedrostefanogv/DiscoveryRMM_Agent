//go:build windows

package automation

import (
	"strings"
	"testing"
)

// -- F5: consistencia de AD (repadmin / dcdiag / w32tm) --

// TestParseRepadminShowRepl_DetectaFalhas cobre o formato real do
// 'repadmin /showrepl', que marca falha com codigo entre parenteses.
func TestParseRepadminShowRepl_DetectaFalhas(t *testing.T) {
	raw := `repadmin running command /showrepl against server localhost
	
	==== INBOUND NEIGHBORS ====
	
	DC=corp,DC=local
	    Default-First-Site-NameDC02 via RPC
	        DSA object GUID: 11111111-2222-3333-4444-555555555555
	        Last attempt @ 2026-11-06 12:00:00 failed, result 8524 (0x214c):
	            The DSA operation is unable to proceed because of a DNS lookup failure.
	        Last success @ 2026-11-05 12:00:00.
	    3 consecutive failures.`

	falhas := parseRepadminShowRepl(raw)
	if len(falhas) == 0 {
		t.Fatal("deveria detectar a falha de replicacao (result 8524)")
	}
	// A linha de SUCESSO nunca pode virar falha: isso geraria alerta falso
	// num AD saudavel.
	for _, f := range falhas {
		if strings.Contains(f, "Last success") {
			t.Errorf("linha de sucesso classificada como falha: %q", f)
		}
	}
}

// TestParseRepadminShowRepl_SemFalhas garante que um AD saudavel devolve lista
// vazia -- o caso mais comum e o que NAO pode gerar alerta.
func TestParseRepadminShowRepl_SemFalhas(t *testing.T) {
	raw := `==== INBOUND NEIGHBORS ====
	DC=corp,DC=local
	    Default-First-Site-NameDC02 via RPC
	        Last attempt @ 2026-11-06 12:00:00 was successful.
	        Last success @ 2026-11-06 12:00:00.
	    0 consecutive failures.`

	if falhas := parseRepadminShowRepl(raw); len(falhas) != 0 {
		t.Errorf("AD saudavel nao pode gerar falha: %v", falhas)
	}
}

// TestParseRepadminShowRepl_ResumoZeroNaoEhFalha: contadores '0 failures' nao
// podem ser lidos como falha so por conterem a palavra.
func TestParseRepadminShowRepl_ResumoZeroNaoEhFalha(t *testing.T) {
	for _, raw := range []string{"0 failures", "Total failures: 0", "0 errors", "No replication errors"} {
		t.Run(raw, func(t *testing.T) {
			if falhas := parseRepadminShowRepl(raw); len(falhas) != 0 {
				t.Errorf("%q nao pode virar falha: %v", raw, falhas)
			}
		})
	}
}

// TestParseRepadminShowRepl_LimitaMensagens garante o teto de sanidade: um AD
// muito quebrado nao pode produzir payload ilimitado.
func TestParseRepadminShowRepl_LimitaMensagens(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < maxADFailureMessages+25; i++ {
		sb.WriteString("Last attempt failed, result 8524 (0x214c): linha ")
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	falhas := parseRepadminShowRepl(sb.String())
	if len(falhas) > maxADFailureMessages {
		t.Errorf("esperado no maximo %d mensagens, veio %d", maxADFailureMessages, len(falhas))
	}
}

// TestParseDcdiagOutput cobre o formato 'DC01 failed test <Nome>' e o
// fallback pelo teste em curso.
func TestParseDcdiagOutput(t *testing.T) {
	raw := `Directory Server Diagnosis
	
	   Starting test: Connectivity
	      ......................... DC01 passed test Connectivity
	   Starting test: Replications
	      ......................... DC01 failed test Replications
	   Starting test: Services
	      ......................... DC01 failed test Services`

	falhos := parseDcdiagOutput(raw)
	if len(falhos) != 2 {
		t.Fatalf("esperado 2 testes falhos, obtido %d: %v", len(falhos), falhos)
	}
	if falhos[0] != "Replications" || falhos[1] != "Services" {
		t.Errorf("nomes de teste errados: %v", falhos)
	}
}

// TestParseDcdiagOutput_SemFalhas e o caso saudavel.
func TestParseDcdiagOutput_SemFalhas(t *testing.T) {
	raw := `   Starting test: Connectivity
	      ......................... DC01 passed test Connectivity`
	if falhos := parseDcdiagOutput(raw); len(falhos) != 0 {
		t.Errorf("nenhum teste falhou, esperado lista vazia: %v", falhos)
	}
}

// TestParseW32tmStatus cobre desvio positivo, negativo e a fonte de tempo.
func TestParseW32tmStatus(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantOffset float64
		wantOK     bool
	}{
		{"desvio positivo", "Phase Offset: 0.0012345s", 0.0012345, true},
		{"desvio negativo", "Phase Offset: -2.5s", -2.5, true},
		{"clock offset", "Clock Offset: 15.75s", 15.75, true},
		{"virgula decimal", "Phase Offset: 1,5s", 1.5, true},
		{"sem offset", "Source: time.windows.com", 0, false},
		{"vazio", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offset, _, ok := parseW32tmStatus(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, esperado %v", ok, tc.wantOK)
			}
			if ok && offset != tc.wantOffset {
				t.Errorf("offset = %v, esperado %v", offset, tc.wantOffset)
			}
		})
	}
}

// TestParseW32tmStatus_FonteDeTempo garante que a fonte e capturada e que
// 'Free-running System Clock' (sem fonte confiavel) e preservado como texto:
// e a causa raiz de um desvio de relogio.
func TestParseW32tmStatus_FonteDeTempo(t *testing.T) {
	raw := `Leap Indicator: 0(no warning)
	Stratum: 3 (secondary reference - syncd by (S)NTP)
	Source: time.windows.com,0x9
	Phase Offset: 0.0005000s`

	offset, source, ok := parseW32tmStatus(raw)
	if !ok {
		t.Fatal("deveria ler o offset")
	}
	if offset != 0.0005 {
		t.Errorf("offset = %v, esperado 0.0005", offset)
	}
	if !strings.Contains(source, "time.windows.com") {
		t.Errorf("fonte de tempo nao capturada: %q", source)
	}

	// Sem fonte confiavel: o texto deve ser preservado, nao descartado.
	_, semFonte, _ := parseW32tmStatus("Source: Free-running System Clock\nPhase Offset: 30s")
	if !strings.Contains(semFonte, "Free-running") {
		t.Errorf("fonte sem confianca deveria ser preservada: %q", semFonte)
	}
}

// TestBuildADConsistencyCollection_CanaisEColetaOK valida o contrato de canais
// e, principalmente, a distincao entre 'sem falhas' e 'nao perguntamos'.
func TestBuildADConsistencyCollection_CanaisEColetaOK(t *testing.T) {
	evidence := adConsistencyEvidence{
		ReplicationChecked:  true,
		ReplicationFailures: 0,
		DcdiagChecked:       true,
		TimeChecked:         true,
		TimeOffsetSeconds:   2.5,
	}
	collection := buildADConsistencyCollection(evidence)

	if got := collection.Channels[ChannelADCollectOK]; got != 1 {
		t.Errorf("ad_collect_ok = %v, esperado 1", got)
	}
	if got := collection.Channels[ChannelADReplicationOK]; got != 1 {
		t.Errorf("replication_ok = %v, esperado 1", got)
	}
	if got := collection.Channels[ChannelADDcdiagOK]; got != 1 {
		t.Errorf("dcdiag_ok = %v, esperado 1", got)
	}
	if got := collection.Channels[ChannelADClockOK]; got != 1 {
		t.Errorf("clock_ok = %v, esperado 1 (2.5s <= 15s)", got)
	}
	if got := collection.Channels[ChannelADTimeOffsetSeconds]; got != 2.5 {
		t.Errorf("time_offset_seconds = %v, esperado 2.5", got)
	}
}

// TestBuildADConsistencyCollection_DesvioGrandeQuebraKerberos: acima de 15s o
// Kerberos recusa tickets, entao clock_ok precisa cair.
func TestBuildADConsistencyCollection_DesvioGrandeQuebraKerberos(t *testing.T) {
	collection := buildADConsistencyCollection(adConsistencyEvidence{
		TimeChecked:       true,
		TimeOffsetSeconds: -42.0,
	})
	if got := collection.Channels[ChannelADClockOK]; got != 0 {
		t.Errorf("clock_ok = %v, esperado 0 para desvio de -42s", got)
	}
	// O sinal negativo e EVIDENCIA (relogio atrasado) e deve ser preservado.
	if got := collection.Channels[ChannelADTimeOffsetSeconds]; got != -42 {
		t.Errorf("time_offset_seconds = %v, esperado -42 (sinal preservado)", got)
	}
}

// TestBuildADConsistencyCollection_FalhaTotalNaoEhSucesso: sem nenhum bloco
// coletado, ad_collect_ok tem de ser 0. O servidor precisa saber que a
// ausencia de falhas vem de NAO TER PERGUNTADO.
func TestBuildADConsistencyCollection_FalhaTotalNaoEhSucesso(t *testing.T) {
	collection := buildADConsistencyCollection(adConsistencyEvidence{
		Errors: []string{"repadmin indisponivel", "dcdiag indisponivel"},
	})
	if got := collection.Channels[ChannelADCollectOK]; got != 0 {
		t.Errorf("ad_collect_ok = %v, esperado 0 quando nada foi coletado", got)
	}
	if _, existe := collection.Channels[ChannelADReplicationOK]; existe {
		t.Error("nao deve publicar replication_ok sem ter lido o repadmin")
	}
}

// TestBuildADConsistencyCollection_ParcialPreservaEvidencia: perder o dcdiag
// NAO pode apagar a evidencia de replicacao que veio boa.
func TestBuildADConsistencyCollection_ParcialPreservaEvidencia(t *testing.T) {
	collection := buildADConsistencyCollection(adConsistencyEvidence{
		ReplicationChecked:  true,
		ReplicationFailures: 3,
		Errors:              []string{"dcdiag indisponivel: timeout"},
	})
	if got := collection.Channels[ChannelADCollectOK]; got != 1 {
		t.Errorf("ad_collect_ok = %v, esperado 1 (repadmin respondeu)", got)
	}
	if got := collection.Channels[ChannelADReplicationFailures]; got != 3 {
		t.Errorf("replication_failures = %v, esperado 3", got)
	}
	if got := collection.Channels[ChannelADReplicationOK]; got != 0 {
		t.Errorf("replication_ok = %v, esperado 0", got)
	}
}

// TestChannelsF5_NomesExatos trava o CONTRATO com o servidor.
func TestChannelsF5_NomesExatos(t *testing.T) {
	want := map[string]string{
		"ad_collect_ok":        ChannelADCollectOK,
		"replication_failures": ChannelADReplicationFailures,
		"replication_ok":       ChannelADReplicationOK,
		"dcdiag_failed_tests":  ChannelADDcdiagFailedTests,
		"dcdiag_ok":            ChannelADDcdiagOK,
		"time_offset_seconds":  ChannelADTimeOffsetSeconds,
		"clock_ok":             ChannelADClockOK,
	}
	for esperado, atual := range want {
		if esperado != atual {
			t.Errorf("canal renomeado: esperado %q, constante vale %q", esperado, atual)
		}
	}
}

// TestTruncarAD e TestBoolToFloat cobrem os utilitarios.
func TestTruncarAD(t *testing.T) {
	if got := truncarAD("abc", 10); got != "abc" {
		t.Errorf("texto curto nao deveria mudar: %q", got)
	}
	longo := truncarAD(strings.Repeat("x", 100), 10)
	if len(longo) != 10 {
		t.Errorf("truncado deveria ter 10 chars, tem %d", len(longo))
	}
	if !strings.HasSuffix(longo, "...") {
		t.Errorf("truncado deveria terminar em reticencias: %q", longo)
	}
}

func TestBoolToFloat(t *testing.T) {
	if boolToFloat(true) != 1 || boolToFloat(false) != 0 {
		t.Error("boolToFloat deve mapear true->1 e false->0")
	}
}
