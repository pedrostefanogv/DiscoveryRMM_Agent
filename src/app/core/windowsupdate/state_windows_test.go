//go:build windows

package windowsupdate

// Sondas reais desta maquina (registro/servico/log). Sao o que o agente roda a
// cada ~5 min; aqui validamos que nao travam e devolvem dados coerentes.

import (
	"errors"
	"strconv"
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

// A enumeracao CBS e o item mais caro da sonda de 5 min: o cache precisa
// poupar a leitura dentro do TTL e refazer a leitura quando expira ou quando e
// invalidado explicitamente.
func TestInstalledKBsCacheTTL(t *testing.T) {
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	now := base
	previous := windowsUpdateNow
	windowsUpdateNow = func() time.Time { return now }
	t.Cleanup(func() {
		windowsUpdateNow = previous
		enumerateInstalledKBs = readInstalledKBsFromRegistry
		InvalidateInstalledKBsCache()
	})

	reads := 0
	enumerateInstalledKBs = func() ([]string, error) {
		reads++
		return []string{"KB" + strconv.Itoa(reads)}, nil
	}
	InvalidateInstalledKBsCache()

	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB1" {
		t.Fatalf("primeira leitura deveria enumerar: %v", got)
	}
	if reads != 1 {
		t.Fatalf("reads=%d want 1", reads)
	}

	// Dentro do TTL: vem do cache.
	now = base.Add(59 * time.Minute)
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB1" {
		t.Fatalf("dentro do TTL deveria usar o cache: %v", got)
	}
	if reads != 1 {
		t.Fatalf("reads=%d want 1 (cache)", reads)
	}

	// TTL expirado: enumera de novo.
	now = base.Add(61 * time.Minute)
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB2" {
		t.Fatalf("TTL expirado deveria reler: %v", got)
	}

	// Invalidate explícito descarta o cache.
	InvalidateInstalledKBsCache()
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB3" {
		t.Fatalf("Invalidate deveria descartar o cache: %v", got)
	}
}

// Falha de leitura (CBS indisponível no servicing) NÃO pode virar "nenhuma KB"
// cacheada por 1h: devolve o último valor conhecido e tenta de novo depois.
func TestInstalledKBsFailureIsNotCached(t *testing.T) {
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	previous := windowsUpdateNow
	windowsUpdateNow = func() time.Time { return base }
	t.Cleanup(func() {
		windowsUpdateNow = previous
		enumerateInstalledKBs = readInstalledKBsFromRegistry
		InvalidateInstalledKBsCache()
	})

	enumerateInstalledKBs = func() ([]string, error) { return []string{"KB5000001"}, nil }
	InvalidateInstalledKBsCache()
	if got := installedKBsFromRegistry(); len(got) != 1 {
		t.Fatalf("leitura ok=%v", got)
	}

	// Agora a leitura falha: mantém o último valor conhecido...
	enumerateInstalledKBs = func() ([]string, error) { return nil, errors.New("cbs indisponivel") }
	InvalidateInstalledKBsCache()
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB5000001" {
		t.Fatalf("falha deveria devolver o último valor conhecido: %v", got)
	}

	// ...e NÃO grava cache: a próxima chamada tenta de novo (e vê o novo valor).
	enumerateInstalledKBs = func() ([]string, error) { return []string{"KB5000002"}, nil }
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB5000002" {
		t.Fatalf("após falha, a próxima leitura deveria reler: %v", got)
	}
}

// Invalidate durante a enumeração (scan concorrente) não pode ser desfeito pela
// publicação da leitura antiga ("lost update" deixava o cache envenenado por 1h).
func TestInstalledKBsInvalidateDuringReadWins(t *testing.T) {
	previous := windowsUpdateNow
	windowsUpdateNow = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		windowsUpdateNow = previous
		enumerateInstalledKBs = readInstalledKBsFromRegistry
		InvalidateInstalledKBsCache()
	})

	InvalidateInstalledKBsCache()
	enumerateInstalledKBs = func() ([]string, error) {
		// Simula o scan invalidando no meio da enumeração (leitura lenta).
		InvalidateInstalledKBsCache()
		return []string{"KB_STALE"}, nil
	}
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB_STALE" {
		t.Fatalf("primeira chamada devolve o que leu: %v", got)
	}

	// A invalidacao concorrente venceu: nada foi cacheado.
	enumerateInstalledKBs = func() ([]string, error) { return []string{"KB_FRESH"}, nil }
	if got := installedKBsFromRegistry(); len(got) != 1 || got[0] != "KB_FRESH" {
		t.Fatalf("cache não pode ter sido envenenado pela leitura antiga: %v", got)
	}
}

func TestRebootPendingChangedInvalidatesOnlyOnTransition(t *testing.T) {
	InvalidateInstalledKBsCache()
	installedKBsMu.Lock()
	lastRebootPending.known = false
	installedKBsMu.Unlock()

	if !rebootPendingChanged(false) {
		t.Fatal("primeira leitura conta como mudanca")
	}
	if rebootPendingChanged(false) {
		t.Fatal("mesmo valor nao e mudanca")
	}
	if !rebootPendingChanged(true) {
		t.Fatal("transicao false->true e mudanca")
	}
	if rebootPendingChanged(true) {
		t.Fatal("mesmo valor nao e mudanca")
	}
}

func TestEvaluatePendingFileRenameFromRegistry(t *testing.T) {
	info := pendingFileRenameOperations()
	t.Logf("PendingFileRenameOperations: pending=%t operations=%d sources=%v", info.Pending, info.Operations, info.Sources)
	if info.Pending != (info.Operations > 0) {
		t.Fatalf("incoerente: %+v", info)
	}
}
