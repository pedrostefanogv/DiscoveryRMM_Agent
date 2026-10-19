package windowsupdate

import (
	"testing"
	"time"
)

// Caso REAL da maquina 019faa6f: PendingFileRenameOperations tinha 5 entradas
// de arquivos do OneDrive (formato nativo "\??\C:\...", com pares
// origem/destino e destino vazio). A sonda anterior devolvia Pending=false e
// o servidor nunca mostrava "Reinicio pendente".
func TestEvaluatePendingFileRenameRealMachine(t *testing.T) {
	values := []string{
		"\\??\\C:\\Program Files\\Microsoft OneDrive\\OneDrive.exe",
		"",
		"\\??\\C:\\Program Files\\Microsoft OneDrive\\FileSyncShell64.dll",
		"",
		"\\??\\C:\\Program Files\\Microsoft OneDrive\\OneDriveSetup.exe",
		"",
		"\\??\\C:\\Program Files\\Microsoft OneDrive\\update.zip",
		"",
		"\\??\\C:\\Program Files\\Microsoft OneDrive\\Update.exe",
		"",
	}
	info := EvaluatePendingFileRename(values)
	if !info.Pending {
		t.Fatalf("Pending=false — 5 entradas reais deveriam indicar reinicio pendente: %+v", info)
	}
	if info.Operations != 5 {
		t.Fatalf("Operations=%d want 5", info.Operations)
	}
}

func TestEvaluatePendingFileRenameTable(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{"origem nativa com destino vazio", []string{"\\??\\C:\\Windows\\Temp\\a.sys", ""}, true},
		{"valor real deste PC: OneDrive com marcador *1", []string{
			"*1\\??\\C:\\Program Files\\Microsoft OneDrive\\Update\\OneDriveSetup.exe",
			"",
			"*2\\??\\C:\\Program Files\\Microsoft OneDrive\\StandaloneUpdater\\OneDriveSetup.exe",
			"",
			"*1\\??\\C:\\Program Files\\Microsoft OneDrive\\26.189.0928.0007\\FileCoAuthLib64.dll",
			"",
		}, true},
		{"entrada unica sem destino", []string{"\\??\\C:\\Windows\\System32\\drivers\\ndis.sys"}, true},
		{"numero de sessao antes do caminho", []string{"1" + "\\??\\C:\\Windows\\Temp\\patched.exe", ""}, true},
		{"prefixo SystemRoot", []string{"\\SystemRoot\\System32\\x.dll", ""}, true},
		{"marcador de delete MoveFileEx", []string{"!C:\\Windows\\Temp\\pending.sys", ""}, true},
		{"pares completos", []string{"C:\\a.exe", "C:\\b.exe", "C:\\c.exe", "C:\\d.exe"}, true},
		{"somente vazios", []string{"", "", "   ", ""}, false},
		{"lista vazia", []string{}, false},
		{"nil", nil, false},
		{"somente numeros de sessao", []string{"0", "1", "2", "3"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePendingFileRename(tc.values)
			if got.Pending != tc.want {
				t.Fatalf("Pending=%v want %v (info=%+v)", got.Pending, tc.want, got)
			}
		})
	}
}

func TestRebootReasonsSetDedupAndOrder(t *testing.T) {
	got := RebootReasonsSet(RebootReasonFileRename, RebootReasonWindowsUpdate, RebootReasonFileRename, "")
	want := []string{RebootReasonWindowsUpdate, RebootReasonFileRename}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordem=%v want %v", got, want)
		}
	}
}

// A assinatura do estado e o gatilho do scan sob demanda: precisa ser estavel
// para estados equivalentes e sensivel a mudancas reais (reboot, download,
// ultima instalacao, servico, KBs).
func TestWindowsUpdateStateSignature(t *testing.T) {
	base := WindowsUpdateState{
		Supported:    true,
		ServiceState: "running",
		WuOnline:     true,
		CheckedAt:    time.Unix(1, 0),
	}
	signature := WindowsUpdateStateSignature(base)
	if signature == "" {
		t.Fatal("assinatura vazia")
	}

	// CheckedAt (hora da sonda) NAO pode mudar a assinatura.
	same := base
	same.CheckedAt = time.Unix(999, 0)
	if WindowsUpdateStateSignature(same) != signature {
		t.Fatal("CheckedAt nao pode alterar a assinatura")
	}

	// Ordem dos motivos de reboot nao altera a assinatura.
	oriented := base
	oriented.RebootPending = true
	oriented.RebootReasons = []string{RebootReasonCBS, RebootReasonWindowsUpdate}
	reversed := base
	reversed.RebootPending = true
	reversed.RebootReasons = []string{RebootReasonWindowsUpdate, RebootReasonCBS}
	if WindowsUpdateStateSignature(oriented) != WindowsUpdateStateSignature(reversed) {
		t.Fatal("ordem dos motivos nao pode alterar a assinatura")
	}

	installedAt := time.Unix(1792000000, 0).UTC()
	changes := map[string]WindowsUpdateState{
		"reboot":             {Supported: true, ServiceState: "running", WuOnline: true, RebootPending: true, RebootReasons: []string{RebootReasonFileRename}},
		"download":           {Supported: true, ServiceState: "running", WuOnline: true, DownloadInProgress: true},
		"lastInstallSuccess": {Supported: true, ServiceState: "running", WuOnline: true, LastInstallSuccess: &installedAt},
		"serviceState":       {Supported: true, ServiceState: "disabled", WuOnline: true},
		"qfes":               {Supported: true, ServiceState: "running", WuOnline: true, QFEInstalledCount: 5, RecentQFEs: []string{"KB5127839"}},
		"policy":             {Supported: true, ServiceState: "running", WuOnline: true, PolicyManaged: true, WSUSServer: "https://wsus.local"},
		"wuOnline":           {Supported: true, ServiceState: "running", WuOnline: false},
		"supported":          {Supported: false, ServiceState: "running", WuOnline: true},
	}
	seen := map[string]string{signature: "base"}
	for name, state := range changes {
		changed := WindowsUpdateStateSignature(state)
		if changed == signature {
			t.Fatalf("mudanca em %q nao alterou a assinatura", name)
		}
		if other, ok := seen[changed]; ok {
			t.Fatalf("estados %q e %q colidem", name, other)
		}
		seen[changed] = name
	}
}