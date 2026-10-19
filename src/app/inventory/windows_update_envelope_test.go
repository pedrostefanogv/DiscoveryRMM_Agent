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
