package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"discovery/app/core/buildinfo"
)

func TestDownloadFromURL_DownloadsAndReturnsSHA256(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/download/agent" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "mdz_token_123" },
		GetAgentID: func() string { return "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" },
		TempDir:    t.TempDir(),
	}

	downloadURL := apiServer.URL + "/api/v1/download/agent"
	path, sha, err := updater.downloadFromURL(context.Background(), downloadURL)
	if err != nil {
		t.Fatalf("downloadFromURL: %v", err)
	}
	defer os.Remove(path)

	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}
	if filepath.Ext(path) != ".exe" {
		t.Fatalf("download path = %q, want .exe suffix", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded payload mismatch: got %q want %q", string(data), string(payload))
	}
}

func TestDownloadFromCacheOrPublic_UsesPublicEndpoint(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/download/agent" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "mdz_token_123" },
		GetAgentID: func() string { return "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" },
		TempDir:    t.TempDir(),
	}

	path, sha, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), "")
	if err != nil {
		t.Fatalf("downloadFromCacheOrPublic: %v", err)
	}
	defer os.Remove(path)

	if fromP2P {
		t.Fatalf("fromP2P = true, want false (HTTP fallback)")
	}

	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded payload mismatch: got %q want %q", string(data), string(payload))
	}
}

func TestResumePendingInstallReport_ClearsStateOnVersionMatch(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "1.2.5"
	defer func() {
		buildinfo.Version = previousVersion
	}()

	releaseID := "rel-3"
	tempDir := t.TempDir()
	updater := &Updater{
		TempDir: tempDir,
	}
	if err := updater.persistPendingInstallState(pendingInstallState{
		ReleaseID:      &releaseID,
		CurrentVersion: "1.2.4",
		TargetVersion:  "1.2.5",
		CorrelationID:  "corr-1",
		RecordedAtUTC:  "2026-04-19T12:00:00Z",
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}

	updater.ResumePendingInstallReport(context.Background())

	if _, err := os.Stat(filepath.Join(tempDir, pendingInstallFile)); !os.IsNotExist(err) {
		t.Fatalf("expected pending install file to be removed, stat err=%v", err)
	}
}

func TestWindowBusy(t *testing.T) {
	// Sem callback: nunca adia.
	u := &Updater{}
	if u.windowBusy() {
		t.Fatal("windowBusy = true sem CanInstallNow configurado")
	}

	// Callback retorna true (janela minimizada/oculta): não adia.
	allowed := &Updater{CanInstallNow: func() bool { return true }}
	if allowed.windowBusy() {
		t.Fatal("windowBusy = true com CanInstallNow retornando true")
	}

	// Callback retorna false (janela aberta em uso): adia.
	busy := &Updater{CanInstallNow: func() bool { return false }}
	if !busy.windowBusy() {
		t.Fatal("windowBusy = false com CanInstallNow retornando false")
	}
}

func TestCheckAndUpdate_DeferredWhenWindowBusy(t *testing.T) {
	updater := &Updater{
		GetToken:      func() string { return "token" },
		GetAgentID:    func() string { return "agent-1" },
		CanInstallNow: func() bool { return false }, // janela aberta em uso
		Logf:          func(string, ...any) {},
	}

	err := updater.CheckAndUpdate(context.Background(), false)
	if !errors.Is(err, ErrWindowBusy) {
		t.Fatalf("err = %v, want ErrWindowBusy", err)
	}
}

func TestCheckAndUpdate_ForceBypassesWindowBusy(t *testing.T) {
	// force=true ignora o gate de janela (comando explícito do servidor).
	// Sem token válido o check falha antes de qualquer rede, mas NÃO deve
	// retornar ErrWindowBusy — prova que o gate foi ignorado.
	updater := &Updater{
		GetToken:      func() string { return "" },
		GetAgentID:    func() string { return "agent-1" },
		CanInstallNow: func() bool { return false },
		Logf:          func(string, ...any) {},
	}

	err := updater.CheckAndUpdate(context.Background(), true)
	if errors.Is(err, ErrWindowBusy) {
		t.Fatal("force=true não deveria ser bloqueado pelo gate de janela")
	}
}

func TestCompareVersions_SemverSegments(t *testing.T) {
	if got := compareVersions("1.10.0", "1.2.9"); got <= 0 {
		t.Fatalf("compareVersions should treat 1.10.0 > 1.2.9, got %d", got)
	}
	if got := compareVersions("1.2.0", "1.2.0"); got != 0 {
		t.Fatalf("compareVersions equality = %d", got)
	}
	if got := compareVersions("1.2.0", "1.2.1"); got >= 0 {
		t.Fatalf("compareVersions should treat 1.2.0 < 1.2.1, got %d", got)
	}
}

func TestNormalizeArtifactType_ToleratesInstallerCasing(t *testing.T) {
	if got := normalizeArtifactType(""); got != "Installer" {
		t.Fatalf("normalizeArtifactType(\"\") = %q", got)
	}
	if got := normalizeArtifactType("installer"); got != "Installer" {
		t.Fatalf("normalizeArtifactType(lowercase) = %q", got)
	}
	if got := normalizeArtifactType("Installer"); got != "Installer" {
		t.Fatalf("normalizeArtifactType(canonical) = %q", got)
	}
	if got := normalizeArtifactType("PortableZip"); got != "PortableZip" {
		t.Fatalf("normalizeArtifactType(custom) = %q", got)
	}
}

func TestParseNSISLogTimestamp_ZeroPadded(t *testing.T) {
	// Formato com zero-padding (2 dígitos).
	ts, ok := parseNSISLogTimestamp("2026-08-05 09:03:02 Instalacao concluida com sucesso")
	if !ok {
		t.Fatalf("parseNSISLogTimestamp(zero-padded) = not ok")
	}
	if ts.Year() != 2026 || ts.Month() != 8 || ts.Day() != 5 ||
		ts.Hour() != 9 || ts.Minute() != 3 || ts.Second() != 2 {
		t.Fatalf("parseNSISLogTimestamp(zero-padded) = %v", ts)
	}
}

func TestParseNSISLogTimestamp_NoZeroPadding(t *testing.T) {
	// O NSIS ${GetTime} "" "L" NÃO faz zero-padding: "2026-8-5 9:3:2".
	ts, ok := parseNSISLogTimestamp("2026-8-5 9:3:2 Instalacao concluida com sucesso")
	if !ok {
		t.Fatalf("parseNSISLogTimestamp(no-padding) = not ok")
	}
	if ts.Year() != 2026 || ts.Month() != 8 || ts.Day() != 5 ||
		ts.Hour() != 9 || ts.Minute() != 3 || ts.Second() != 2 {
		t.Fatalf("parseNSISLogTimestamp(no-padding) = %v", ts)
	}
}

func TestParseNSISLogTimestamp_Invalid(t *testing.T) {
	cases := []string{
		"",
		"===== Discovery Agent Installer Log =====",
		"2026-13-05 09:03:02 invalido", // mês 13
		"2026-08-32 09:03:02 invalido", // dia 32
		"2026-08-05 25:03:02 invalido", // hora 25
		"texto sem timestamp",
	}
	for _, c := range cases {
		if _, ok := parseNSISLogTimestamp(c); ok {
			t.Fatalf("parseNSISLogTimestamp(%q) = ok, want not ok", c)
		}
	}
}

func TestCorrelateInstallerLog_FindsSuccessMarker(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "installer.log")

	// recordedAt em UTC; o NSIS loga em hora local. Usamos o fuso local para
	// gerar timestamps que caem dentro da janela de correlação.
	recordedAt := time.Now().Add(-2 * time.Minute)
	content := "===== Discovery Agent Installer Log =====\r\n" +
		recordedAt.Add(30*time.Second).Format("2006-1-2 15:4:5") + " wails.files concluido com sucesso\r\n" +
		recordedAt.Add(60*time.Second).Format("2006-1-2 15:4:5") + " Instalacao concluida com sucesso\r\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	updater := &Updater{InstallerLogPath: logPath}
	foundSuccess, foundError := updater.correlateInstallerLog(recordedAt.UTC().Format(time.RFC3339))
	if !foundSuccess {
		t.Fatalf("correlateInstallerLog: foundSuccess = false, want true")
	}
	if foundError {
		t.Fatalf("correlateInstallerLog: foundError = true, want false")
	}
}

func TestCorrelateInstallerLog_NoMarker(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "installer.log")

	recordedAt := time.Now().Add(-2 * time.Minute)
	content := "===== Discovery Agent Installer Log =====\r\n" +
		recordedAt.Add(30*time.Second).Format("2006-1-2 15:4:5") + " wails.files concluido com sucesso\r\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	updater := &Updater{InstallerLogPath: logPath}
	foundSuccess, _ := updater.correlateInstallerLog(recordedAt.UTC().Format(time.RFC3339))
	if foundSuccess {
		t.Fatalf("correlateInstallerLog: foundSuccess = true, want false")
	}
}

func TestCleanupOldDownloads_PreservesPendingInstaller(t *testing.T) {
	tempDir := t.TempDir()

	// Cria um instalador pendente (referenciado no pending state) e um antigo.
	pendingExe := filepath.Join(tempDir, "selfupdate-aaa.exe")
	oldExe := filepath.Join(tempDir, "selfupdate-bbb.exe")
	if err := os.WriteFile(pendingExe, []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile pending: %v", err)
	}
	if err := os.WriteFile(oldExe, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}

	// Envelhece o arquivo antigo além do cutoff.
	cutoff := time.Now().Add(-cleanupOldDownloadsAge - time.Hour)
	if err := os.Chtimes(oldExe, cutoff, cutoff); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}

	updater := &Updater{TempDir: tempDir}
	if err := updater.persistPendingInstallState(pendingInstallState{
		CurrentVersion: "1.2.4",
		TargetVersion:  "1.2.5",
		InstallerPath:  pendingExe,
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}

	updater.cleanupOldDownloads()

	if _, err := os.Stat(pendingExe); err != nil {
		t.Fatalf("pending installer foi removido pelo cleanup: %v", err)
	}
	if _, err := os.Stat(oldExe); !os.IsNotExist(err) {
		t.Fatalf("old installer deveria ter sido removido, stat err=%v", err)
	}
}

func TestHandleLaunchFailure_PreservesStateForRetry(t *testing.T) {
	tempDir := t.TempDir()
	updater := &Updater{TempDir: tempDir}

	// Persiste um estado pendente com 1 tentativa.
	if err := updater.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  "1.2.4",
		TargetVersion:   "1.2.5",
		CorrelationID:   "corr-1",
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: "abc",
		InstallerPath:   filepath.Join(tempDir, "selfupdate-aaa.exe"),
		InstallAttempts: 1,
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}

	err := updater.handleLaunchFailure(context.Background(), filepath.Join(tempDir, "selfupdate-aaa.exe"), "1.2.5", errors.New("launch boom"))
	if err == nil {
		t.Fatalf("handleLaunchFailure = nil, want error")
	}

	// O estado pendente deve ser preservado com contador incrementado.
	state, loadErr := updater.loadPendingInstallState()
	if loadErr != nil {
		t.Fatalf("loadPendingInstallState: %v", loadErr)
	}
	if state.InstallAttempts != 2 {
		t.Fatalf("InstallAttempts = %d, want 2", state.InstallAttempts)
	}
	if updater.LaunchFailCount() != 1 {
		t.Fatalf("LaunchFailCount = %d, want 1", updater.LaunchFailCount())
	}
}

func TestHandleLaunchFailure_ClearsAfterMaxAttempts(t *testing.T) {
	tempDir := t.TempDir()
	updater := &Updater{TempDir: tempDir}

	// Persiste um estado pendente já no limite de tentativas.
	if err := updater.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  "1.2.4",
		TargetVersion:   "1.2.5",
		CorrelationID:   "corr-1",
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: "abc",
		InstallerPath:   filepath.Join(tempDir, "selfupdate-aaa.exe"),
		InstallAttempts: maxInstallAttempts - 1,
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}

	_ = updater.handleLaunchFailure(context.Background(), filepath.Join(tempDir, "selfupdate-aaa.exe"), "1.2.5", errors.New("launch boom"))

	// Após atingir o máximo, o estado deve ser limpo.
	if _, err := os.Stat(filepath.Join(tempDir, pendingInstallFile)); !os.IsNotExist(err) {
		t.Fatalf("pending install file deveria ter sido removido apos max attempts, stat err=%v", err)
	}
}

func TestHasPendingInstallRetry(t *testing.T) {
	tempDir := t.TempDir()
	updater := &Updater{TempDir: tempDir}

	// Sem estado pendente → false.
	if updater.hasPendingInstallRetry() {
		t.Fatalf("hasPendingInstallRetry sem estado = true, want false")
	}

	// Estado com tentativas restantes → true.
	if err := updater.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  "1.2.4",
		TargetVersion:   "1.2.5",
		CorrelationID:   "corr-1",
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: "abc",
		InstallerPath:   filepath.Join(tempDir, "selfupdate-aaa.exe"),
		InstallAttempts: 1,
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}
	if !updater.hasPendingInstallRetry() {
		t.Fatalf("hasPendingInstallRetry com tentativas restantes = false, want true")
	}

	// Estado no limite → false. Usa target diferente para evitar que
	// persistPendingInstallState incremente o contador (que incrementa quando
	// target/current coincidem com o estado existente).
	if err := updater.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  "1.2.4",
		TargetVersion:   "1.2.6",
		CorrelationID:   "corr-2",
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: "abc",
		InstallerPath:   filepath.Join(tempDir, "selfupdate-bbb.exe"),
		InstallAttempts: maxInstallAttempts,
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}
	if updater.hasPendingInstallRetry() {
		t.Fatalf("hasPendingInstallRetry no limite = true, want false")
	}
}

func TestWriteInstallerLogMarker(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "installer.log")
	updater := &Updater{InstallerLogPath: logPath}

	updater.writeInstallerLogMarker("agente iniciou update para versao 1.2.5")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[AGENT]") {
		t.Fatalf("marcador nao contem [AGENT]: %q", content)
	}
	if !strings.Contains(content, "agente iniciou update para versao 1.2.5") {
		t.Fatalf("marcador nao contem a mensagem: %q", content)
	}
	// O timestamp deve ser parseável pelo parser NSIS (sem zero-padding).
	if _, ok := parseNSISLogTimestamp(content); !ok {
		t.Fatalf("timestamp do marcador nao parseavel: %q", content)
	}
}
