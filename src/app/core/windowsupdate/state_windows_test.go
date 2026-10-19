//go:build windows

package windowsupdate

// Sondas reais desta maquina (registro/servico/log). Sao o que o agente roda a
// cada ~5 min; aqui validamos que nao travam e devolvem dados coerentes.

import (
	"testing"
	"time"
)

func TestProbeLocalStateReturnsData(t *testing.T) {
	state := ProbeLocalState()
	if !state.Supported {
		t.Fatal("Windows deveria ser suportado")
	}
	if state.ServiceState == "" || state.ServiceState == "unknown" {
		t.Logf("serviceState=%q", state.ServiceState)
	}
	if state.QFEInstalledCount == 0 {
		t.Logf("nenhuma KB vista no CBS (ambiente sem packages)")
	}
	t.Logf("state=%+v", state)
	if state.CheckedAt.IsZero() {
		t.Fatal("CheckedAt deve ser preenchido")
	}
	if WindowsUpdateStateSignature(state) == "" {
		t.Fatal("assinatura vazia")
	}
}

func TestProbePendingRebootDoesNotPanic(t *testing.T) {
	info := ProbePendingReboot()
	if info.CheckedAt.IsZero() {
		t.Fatal("CheckedAt deve ser preenchido")
	}
	t.Logf("reboot pending=%t reasons=%v pendingFileRename=%+v", info.Pending, info.Reasons, pendingFileRenameOperations())
	if info.Pending != (len(info.Reasons) > 0) {
		t.Fatalf("Pending=%t incoerente com reasons=%v", info.Pending, info.Reasons)
	}
}

func TestProbeStateToleratesMissingContext(t *testing.T) {
	client := New(DefaultOptions())
	state := client.ProbeState(nil)
	if !state.Supported {
		t.Fatal("ProbeState deveria reportar suporte no Windows")
	}
}

func TestReadWindowsUpdateLogActivityRealFile(t *testing.T) {
	activity := readWindowsUpdateLogActivity()
	t.Logf("logActivity=%+v", activity)
	if activity.LastInstallSuccess != nil && activity.LastInstallSuccess.After(time.Now().Add(time.Minute)) {
		t.Fatal("instalacao no futuro")
	}
}

func TestEvaluatePendingFileRenameFromRegistry(t *testing.T) {
	info := pendingFileRenameOperations()
	t.Logf("PendingFileRenameOperations: pending=%t operations=%d sources=%v", info.Pending, info.Operations, info.Sources)
	if info.Pending != (info.Operations > 0) {
		t.Fatalf("incoerente: %+v", info)
	}
}
