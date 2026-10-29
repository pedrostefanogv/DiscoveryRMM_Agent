package inventory

// Testes do envelope de Windows Update: o status/errorCode e a sonda de
// estado precisam SEMPRE chegar ao servidor, mesmo quando o scan falhou — era
// exatamente esse silencio que deixava a aba de atualizacoes vazia (servico
// desabilitado) ou congelada por ate 6h (download/reboot sem mudar payload).

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"discovery/app/core/windowsupdate"
)

func sampleScanResult() *windowsupdate.ScanResult {
	installed := time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)
	return &windowsupdate.ScanResult{
		Updates: []windowsupdate.Update{
			{UpdateKey: "kb:KB5127690", KBID: "KB5127690", Title: "Atualizacao KB5127690", IsInstalled: true, Source: windowsupdate.SourceQFE},
		},
		PendingCount:   0,
		InstalledCount: 1,
		SkippedCount:   2,
		SkippedReasons: map[string]int{windowsupdate.SkipReasonDriver: 2},
		PendingReboot: windowsupdate.RebootInfo{
			Pending:   true,
			Reasons:   []string{windowsupdate.RebootReasonFileRename},
			CheckedAt: installed,
		},
		State: windowsupdate.WindowsUpdateState{
			Supported:          true,
			ServiceState:       "running",
			WuOnline:           true,
			RebootPending:      true,
			RebootReasons:      []string{windowsupdate.RebootReasonFileRename},
			LastInstallSuccess: &installed,
			QFEInstalledCount:  4,
			CheckedAt:          installed,
		},
		StartedAt:  installed,
		DurationMs: 15000,
		Source:     windowsupdate.SourceWUA,
	}
}

func TestBuildWindowsUpdateEnvelopeOK(t *testing.T) {
	result := sampleScanResult()
	envelope := buildWindowsUpdateEnvelope("agent-1", result, "")
	if envelope.Scan.Status != windowsupdate.ScanStatusOK {
		t.Fatalf("status=%q", envelope.Scan.Status)
	}
	if envelope.Scan.ErrorCode != "" {
		t.Fatalf("errorCode=%q", envelope.Scan.ErrorCode)
	}
	if !envelope.PendingReboot.Pending {
		t.Fatal("reboot pendente perdido")
	}
	if !envelope.State.RebootPending {
		t.Fatal("state.rebootPending deve refletir a sonda do scan")
	}
	if envelope.State.Signature == "" {
		t.Fatal("assinatura ausente")
	}
	if envelope.Scan.SkippedReason[windowsupdate.SkipReasonDriver] != 2 {
		t.Fatalf("skippedReasons=%v", envelope.Scan.SkippedReason)
	}
	// O campo "state" e opcional para a API, mas precisa existir no JSON.
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["state"]; !ok {
		t.Fatal("campo state ausente no payload")
	}
}

// downloadPercent e o nome consumido pela API .NET: precisa sair no envelope
// com a porcentagem da sonda e ser omitido quando desconhecido (0).
func TestBuildWindowsUpdateEnvelopeDownloadPercent(t *testing.T) {
	result := sampleScanResult()
	result.State.DownloadInProgress = true
	result.State.DownloadProgressPercent = 42
	envelope := buildWindowsUpdateEnvelope("agent-1", result, "")
	if envelope.State.DownloadPercent == nil || *envelope.State.DownloadPercent != 42 {
		t.Fatalf("downloadPercent=%v want 42", envelope.State.DownloadPercent)
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State["downloadPercent"] != float64(42) {
		t.Fatalf("campo downloadPercent ausente/incorreto: %v", decoded.State["downloadPercent"])
	}

	// 0% REAL durante download => enviado (é informação válida).
	result.State.DownloadProgressPercent = 0
	body, err = json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	decoded.State = nil
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State["downloadPercent"] != float64(0) {
		t.Fatalf("downloadPercent=0 com download em andamento deveria ser enviado: %v", decoded.State["downloadPercent"])
	}

	// Sem download em andamento => ausente (não herda percentual antigo).
	result.State.DownloadInProgress = false
	result.State.DownloadProgressPercent = 42
	body, err = json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	decoded.State = nil
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.State["downloadPercent"]; ok {
		t.Fatal("downloadPercent não pode ser enviado quando não há download")
	}
}

// qfeInstalledCount: 0 KBs reais viaja; leitura falha do CBS fica AUSENTE
// (senão a API gravaria 0 e apagaria a contagem conhecida).
func TestBuildWindowsUpdateEnvelopeQfeInstalledCount(t *testing.T) {
	result := sampleScanResult()
	result.State.QFEInstalledCount = 0
	result.State.QFEInstalledCountKnown = true

	body, err := json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State["qfeInstalledCount"] != float64(0) {
		t.Fatalf("0 KBs reais deveria ser enviado: %v", decoded.State["qfeInstalledCount"])
	}

	result.State.QFEInstalledCountKnown = false
	body, err = json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	decoded.State = nil
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.State["qfeInstalledCount"]; ok {
		t.Fatal("leitura falha do CBS não pode enviar contagem (gravaria 0)")
	}
}

// maxDownloadSize (bug corrigido no WUA) precisa sair no JSON de updates.
func TestBuildWindowsUpdateEnvelopeMaxDownloadSize(t *testing.T) {
	result := sampleScanResult()
	result.Updates = append(result.Updates, windowsupdate.Update{
		UpdateKey:       "guid:11111111-1111-1111-1111-111111111111:1",
		Title:           "Cumulative Update",
		IsPending:       true,
		MaxDownloadSize: 512_000_000,
		Source:          windowsupdate.SourceWUA,
	})

	body, err := json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Updates []map[string]any `json:"updates"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, update := range decoded.Updates {
		if update["maxDownloadSize"] == float64(512_000_000) {
			found = true
		}
	}
	if !found {
		t.Fatalf("maxDownloadSize ausente no envelope: %v", decoded.Updates)
	}
}

// skippedCount vai SEMPRE (mesmo 0): com omitempty o servidor nao conseguia
// limpar um contador antigo ("3 ignorados" congelado no snapshot).
func TestBuildWindowsUpdateEnvelopeAlwaysSendsSkippedCount(t *testing.T) {
	result := sampleScanResult()
	result.SkippedCount = 0
	result.SkippedReasons = nil

	body, err := json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Scan map[string]any `json:"scan"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, ok := decoded.Scan["skippedCount"]; !ok || value != float64(0) {
		t.Fatalf("skippedCount=0 deveria ser enviado explicitamente: %v (presente=%v)", value, ok)
	}
	if _, ok := decoded.Scan["skippedReasons"]; ok {
		t.Fatal("skippedReasons vazio deveria ser omitido")
	}
}

// activeHoursStart/End vinham sendo lidos pela sonda e NAO subiam no envelope:
// a API e o site tinham o campo (probe.activeHours*) e nunca recebiam nada.
func TestBuildWindowsUpdateEnvelopeActiveHours(t *testing.T) {
	result := sampleScanResult()
	start, end := 8, 18
	result.State.ActiveHoursStart = &start
	result.State.ActiveHoursEnd = &end

	body, err := json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State["activeHoursStart"] != float64(8) || decoded.State["activeHoursEnd"] != float64(18) {
		t.Fatalf("activeHours ausente/incorreto no envelope: start=%v end=%v",
			decoded.State["activeHoursStart"], decoded.State["activeHoursEnd"])
	}

	// Sem politica definida: omitido (nao sai 0-0, que seria uma janela valida).
	result.State.ActiveHoursStart = nil
	result.State.ActiveHoursEnd = nil
	body, err = json.Marshal(buildWindowsUpdateEnvelope("agent-1", result, ""))
	if err != nil {
		t.Fatal(err)
	}
	decoded.State = nil
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.State["activeHoursStart"]; ok {
		t.Fatal("activeHoursStart sem politica deveria ser omitido")
	}
}

func TestBuildWindowsUpdateEnvelopeServiceDisabled(t *testing.T) {
	// Servico desabilitado: cliente devolve resultado parcial + ErrServiceDisabled.
	result := &windowsupdate.ScanResult{
		Updates:       []windowsupdate.Update{},
		Partial:       true,
		StartedAt:     time.Now().UTC(),
		State:         windowsupdate.WindowsUpdateState{Supported: true, ServiceState: "disabled", CheckedAt: time.Now().UTC()},
		PendingReboot: windowsupdate.RebootInfo{CheckedAt: time.Now().UTC()},
	}
	envelope := buildWindowsUpdateEnvelope("agent-1", result, ScanErrorCode(windowsupdate.ErrServiceDisabled))
	if envelope.Scan.Status != windowsupdate.ScanStatusError {
		t.Fatalf("status=%q want error", envelope.Scan.Status)
	}
	if envelope.Scan.ErrorCode != windowsupdate.ScanErrorServiceDisabled {
		t.Fatalf("errorCode=%q", envelope.Scan.ErrorCode)
	}
	if envelope.State.ServiceState != "disabled" {
		t.Fatalf("serviceState=%q", envelope.State.ServiceState)
	}
}

func TestBuildWindowsUpdateEnvelopeDerivesServiceDisabledWithoutError(t *testing.T) {
	result := sampleScanResult()
	result.State.ServiceState = "disabled"
	envelope := buildWindowsUpdateEnvelope("agent-1", result, "")
	if envelope.Scan.ErrorCode != windowsupdate.ScanErrorServiceDisabled {
		t.Fatalf("errorCode=%q", envelope.Scan.ErrorCode)
	}
	if envelope.Scan.Status != windowsupdate.ScanStatusError {
		t.Fatalf("status=%q", envelope.Scan.Status)
	}
}

func TestBuildWindowsUpdateEnvelopePartial(t *testing.T) {
	result := sampleScanResult()
	result.Partial = true
	envelope := buildWindowsUpdateEnvelope("agent-1", result, "")
	if envelope.Scan.Status != "partial" {
		t.Fatalf("status=%q want partial", envelope.Scan.Status)
	}
}

func TestBuildWindowsUpdateEnvelopeTimeout(t *testing.T) {
	envelope := buildWindowsUpdateEnvelope("agent-1", sampleScanResult(), windowsupdate.ScanErrorTimeout)
	if envelope.Scan.Status != windowsupdate.ScanStatusTimeout {
		t.Fatalf("status=%q", envelope.Scan.Status)
	}
}

func TestScanErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, windowsupdate.ScanErrorNone},
		{windowsupdate.ErrServiceDisabled, windowsupdate.ScanErrorServiceDisabled},
		{windowsupdate.ErrWuaUnavailable, windowsupdate.ScanErrorWuaUnavailable},
		{windowsupdate.ErrTimeout, windowsupdate.ScanErrorTimeout},
		{windowsupdate.ErrUnsupportedOS, windowsupdate.ScanErrorUnsupported},
		{errors.New("x"), windowsupdate.ScanErrorFailed},
	}
	for _, tc := range cases {
		if got := ScanErrorCode(tc.err); got != tc.want {
			t.Fatalf("ScanErrorCode(%v)=%q want %q", tc.err, got, tc.want)
		}
	}
}

// A supressao por hash NAO pode esconder mudanca de status/reboot.
func TestWindowsUpdateUploadForcedOnStatusOrRebootChange(t *testing.T) {
	base := buildWindowsUpdateEnvelope("agent-1", sampleScanResult(), "")
	cached, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if windowsUpdateUploadForced(base, cached) {
		t.Fatal("envelope identico nao pode forcar upload")
	}

	// Reboot resolvido (maquina reiniciou) com o mesmo payload de KBs.
	noReboot := sampleScanResult()
	noReboot.PendingReboot = windowsupdate.RebootInfo{CheckedAt: noReboot.StartedAt}
	noReboot.State.RebootPending = false
	noReboot.State.RebootReasons = nil
	noReboot.State.LastInstallSuccess = nil
	envelope := buildWindowsUpdateEnvelope("agent-1", noReboot, "")
	if !windowsUpdateUploadForced(envelope, cached) {
		t.Fatal("mudanca de reboot deve forcar upload")
	}

	// Servico passou a "disabled": falha precisa ser reportada.
	disabled := sampleScanResult()
	disabled.State.ServiceState = "disabled"
	envelopeDisabled := buildWindowsUpdateEnvelope("agent-1", disabled, windowsupdate.ScanErrorServiceDisabled)
	if !windowsUpdateUploadForced(envelopeDisabled, cached) {
		t.Fatal("mudanca de status/errorCode deve forcar upload")
	}

	// Payload ilegivel: reenvia por seguranca.
	if !windowsUpdateUploadForced(base, []byte("{invalido")) {
		t.Fatal("cache ilegivel deve forcar upload")
	}
}

func TestSameReasons(t *testing.T) {
	if !sameReasons([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("mesmo conjunto de motivos deveria casar")
	}
	if sameReasons([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("conjuntos diferentes nao podem casar")
	}
	if !sameReasons(nil, nil) {
		t.Fatal("nil == nil")
	}
}
