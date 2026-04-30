package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/internal/platform"
	"discovery/internal/processutil"
)

// MeshCentralInstallInfo holds the install parameters returned by the server.
type MeshCentralInstallInfo struct {
	GroupName                 string `json:"groupName"`
	MeshID                    string `json:"meshId"`
	InstallURL                string `json:"installUrl"`
	InstallMode               string `json:"installMode"`
	WindowsCommandBackground  string `json:"windowsCommandBackground"`
	WindowsCommandInteractive string `json:"windowsCommandInteractive"`
	WindowsCommand            string `json:"windowsCommand"`
	LinuxCommandBackground    string `json:"linuxCommandBackground"`
	LinuxCommandInteractive   string `json:"linuxCommandInteractive"`
	LinuxCommand              string `json:"linuxCommand"`
}

type meshCentralState struct {
	Installed            bool   `json:"installed"`
	NodeIDKnown          bool   `json:"node-id-known"`
	MeshCentralNodeID    string `json:"mesh-central-node-id"`
	LastInstallAttempt   string `json:"last-install-attempt"`
	LastInstallError     string `json:"last-install-error"`
	LastSuccessfulReport string `json:"last-successful-report"`
	DisabledUntilRefresh bool   `json:"disabled-until-refresh"`
	RepairRequired       bool   `json:"repair-required"`
}

const (
	meshInstallEndpointPath = "/api/v1/agent-auth/me/support/meshcentral/install"
	meshStateCachePrefix    = "meshcentral_state:"
	meshInstallAttempts     = 4
)

var meshNodeIDPattern = regexp.MustCompile(`(?i)nodeid\s*=\s*([^\r\n]+)`)

func (a *App) ensureMeshCentralInstalled(ctx context.Context, reason string, resetPause bool) {
	if runtime.GOOS != "windows" {
		return
	}
	if a == nil {
		return
	}
	if !a.meshEnsureRunning.CompareAndSwap(false, true) {
		a.meshLogf("fluxo ja em execucao; ignorando gatilho=%s", strings.TrimSpace(reason))
		return
	}
	defer a.meshEnsureRunning.Store(false)

	cfg := a.GetAgentConfiguration()
	if cfg.MeshCentralEnabledEffective == nil || !*cfg.MeshCentralEnabledEffective {
		a.meshLogf("meshCentralEnabledEffective=false; ignorando gatilho=%s", strings.TrimSpace(reason))
		return
	}

	state := a.loadMeshCentralState()
	if resetPause && state.DisabledUntilRefresh {
		state.DisabledUntilRefresh = false
		a.saveMeshCentralState(state)
	}
	if state.DisabledUntilRefresh {
		a.meshLogf("instalacao pausada ate proximo refresh de configuracao")
		return
	}

	installed := a.isMeshAgentInstalledLocal()
	servicePresent, serviceHealthy := a.meshServiceStatus(ctx)
	nodeID := a.resolveMeshCentralNodeIDLocal()

	state.Installed = installed
	state.MeshCentralNodeID = normalizeMeshCentralNodeID(nodeID)
	state.NodeIDKnown = strings.TrimSpace(state.MeshCentralNodeID) != ""
	state.RepairRequired = installed && (!servicePresent || !serviceHealthy)
	a.saveMeshCentralState(state)

	if installed && serviceHealthy && state.NodeIDKnown {
		a.meshLogf("mesh ja instalado e saudavel; node id conhecido")
		a.markMeshCentralInstalled()
		return
	}

	if installed && serviceHealthy && !state.NodeIDKnown {
		a.meshLogf("mesh instalado, sem node id local; aguardando hardware report sem reinstalacao")
		a.markMeshCentralInstalled()
		return
	}

	state.LastInstallAttempt = time.Now().UTC().Format(time.RFC3339)
	a.saveMeshCentralState(state)

	info, statusCode, err := a.fetchMeshInstallInfoWithRetry(ctx)
	if err != nil {
		state.LastInstallError = strings.TrimSpace(err.Error())
		switch statusCode {
		case http.StatusForbidden, http.StatusLocked:
			state.DisabledUntilRefresh = true
			a.meshLogf("install endpoint retornou %d; fluxo pausado ate proximo refresh", statusCode)
		case http.StatusServiceUnavailable:
			a.meshLogf("install endpoint indisponivel (503) apos retries")
		default:
			a.meshLogf("falha ao obter parametros de instalacao: %v", err)
		}
		a.saveMeshCentralState(state)
		return
	}

	if err := a.runMeshCentralInstall(ctx, info); err != nil {
		state.LastInstallError = strings.TrimSpace(err.Error())
		state.RepairRequired = true
		a.saveMeshCentralState(state)
		a.meshLogf("erro na instalacao/reparo: %v", err)
		return
	}

	if err := a.waitMeshServiceHealthy(ctx, 2*time.Minute); err != nil {
		state.LastInstallError = err.Error()
		state.RepairRequired = true
		a.saveMeshCentralState(state)
		a.meshLogf("servico MeshCentral nao ficou saudavel apos instalacao: %v", err)
		return
	}

	nodeID = a.waitMeshNodeID(ctx, 90*time.Second)
	state.Installed = true
	state.MeshCentralNodeID = normalizeMeshCentralNodeID(nodeID)
	state.NodeIDKnown = strings.TrimSpace(state.MeshCentralNodeID) != ""
	state.LastInstallError = ""
	state.RepairRequired = false
	a.saveMeshCentralState(state)
	a.markMeshCentralInstalled()
	a.meshLogf("instalacao/reparo concluido")
}

func (a *App) meshLogf(format string, args ...any) {
	a.logs.append("[mesh] " + fmt.Sprintf(format, args...))
}

// isMeshCentralInstalled returns true when the agent has already been installed
// in a previous run (flag persisted in config.json).
func (a *App) isMeshCentralInstalled() bool {
	inst, _, err := loadInstallerConfig()
	if err != nil {
		return false
	}
	return inst.MeshCentralInstalled
}

// markMeshCentralInstalled persists MeshCentralInstalled=true in config.json
// so that subsequent agent restarts skip the installation flow.
func (a *App) markMeshCentralInstalled() {
	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.meshLogf("aviso: nao foi possivel carregar config para marcar instalacao: %v", err)
		return
	}
	inst.MeshCentralInstalled = true
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.meshLogf("aviso: falha ao persistir flag de instalacao: %v", err)
		return
	}
	a.meshLogf("instalacao marcada como concluida no config.json")
}

// fetchMeshInstallInfo calls GET /api/v1/agent-auth/me/support/meshcentral/install
// and returns the install parameters from the server.
func (a *App) fetchMeshInstallInfo(ctx context.Context) (MeshCentralInstallInfo, int, error) {
	cfg := a.GetDebugConfig()
	scheme := strings.TrimSpace(cfg.ApiScheme)
	server := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)

	if scheme == "" || server == "" {
		return MeshCentralInstallInfo{}, 0, fmt.Errorf("servidor API nao configurado")
	}
	if token == "" || agentID == "" {
		return MeshCentralInstallInfo{}, 0, fmt.Errorf("credenciais do agente ausentes")
	}

	url := scheme + "://" + server + meshInstallEndpointPath
	a.meshLogf("consultando endpoint de instalacao: %s", url)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return MeshCentralInstallInfo{}, 0, fmt.Errorf("falha ao criar requisicao: %w", err)
	}
	netutil.SetAgentAuthHeaders(req, token)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return MeshCentralInstallInfo{}, 0, fmt.Errorf("falha na requisicao ao servidor: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return MeshCentralInstallInfo{}, resp.StatusCode, fmt.Errorf("falha ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := strings.TrimSpace(string(bytes.TrimSpace(body)))
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		return MeshCentralInstallInfo{}, resp.StatusCode, fmt.Errorf("servidor retornou HTTP %d: %s", resp.StatusCode, preview)
	}

	var info MeshCentralInstallInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return MeshCentralInstallInfo{}, http.StatusOK, fmt.Errorf("falha ao decodificar resposta de instalacao: %w", err)
	}

	if runtime.GOOS == "windows" && strings.TrimSpace(info.InstallURL) == "" && strings.TrimSpace(info.WindowsCommandBackground) == "" && strings.TrimSpace(info.WindowsCommandInteractive) == "" && strings.TrimSpace(info.WindowsCommand) == "" {
		return MeshCentralInstallInfo{}, http.StatusOK, fmt.Errorf("servidor nao forneceu installUrl/comandos para este agente Windows")
	}
	if runtime.GOOS != "windows" && strings.TrimSpace(info.InstallURL) == "" && strings.TrimSpace(info.LinuxCommandBackground) == "" && strings.TrimSpace(info.LinuxCommandInteractive) == "" && strings.TrimSpace(info.LinuxCommand) == "" {
		return MeshCentralInstallInfo{}, http.StatusOK, fmt.Errorf("servidor nao forneceu installUrl/comandos para este agente")
	}

	return info, http.StatusOK, nil
}

func (a *App) fetchMeshInstallInfoWithRetry(ctx context.Context) (MeshCentralInstallInfo, int, error) {
	var lastErr error
	lastStatus := 0
	for attempt := 1; attempt <= meshInstallAttempts; attempt++ {
		info, status, err := a.fetchMeshInstallInfo(ctx)
		if err == nil {
			return info, status, nil
		}
		lastErr = err
		lastStatus = status
		retryable := status == 0 || status == http.StatusServiceUnavailable
		if !retryable || attempt == meshInstallAttempts {
			break
		}
		base := time.Duration(1<<uint(attempt-1)) * time.Second
		if base > 20*time.Second {
			base = 20 * time.Second
		}
		jitter := time.Duration(rand.New(rand.NewSource(time.Now().UnixNano())).Intn(350)) * time.Millisecond
		wait := base + jitter
		a.meshLogf("falha transitoria no install endpoint (tentativa %d/%d); retry em %s", attempt, meshInstallAttempts, wait)
		select {
		case <-ctx.Done():
			return MeshCentralInstallInfo{}, lastStatus, ctx.Err()
		case <-time.After(wait):
		}
	}
	return MeshCentralInstallInfo{}, lastStatus, lastErr
}

// runMeshCentralInstall executes the installation command provided by the server.
// On Windows uses cmd /C; on other platforms uses sh -c.
// stdout/stderr are captured and appended to the application log.
func (a *App) runMeshCentralInstall(ctx context.Context, info MeshCentralInstallInfo) error {
	if strings.TrimSpace(info.InstallURL) != "" {
		if err := a.runMeshCentralInstallURL(ctx, info.InstallURL); err == nil {
			return nil
		} else {
			a.meshLogf("installUrl falhou; aplicando fallback de comando")
		}
	}

	installCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		command := strings.TrimSpace(firstNonEmptyString(
			info.WindowsCommandBackground,
			info.WindowsCommandInteractive,
			info.WindowsCommand,
		))
		if command == "" {
			return fmt.Errorf("nenhum comando Windows disponivel para fallback")
		}
		a.meshLogf("executando fallback de instalacao via comando Windows")
		cmd = exec.CommandContext(installCtx, "cmd", "/C", command)
		processutil.HideWindow(cmd)
	} else {
		command := strings.TrimSpace(firstNonEmptyString(
			info.LinuxCommandBackground,
			info.LinuxCommandInteractive,
			info.LinuxCommand,
		))
		if command == "" {
			return fmt.Errorf("nenhum comando Linux disponivel para fallback")
		}
		a.meshLogf("executando fallback de instalacao Linux/macOS")
		cmd = exec.CommandContext(installCtx, "sh", "-c", command)
	}

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(outBuf.String())
		if len(output) > 500 {
			output = output[:500] + "..."
		}
		return fmt.Errorf("instalacao falhou (exit %v): %s", err, output)
	}

	output := strings.TrimSpace(outBuf.String())
	if len(output) > 500 {
		output = output[:500] + "..."
	}
	if output != "" {
		a.meshLogf("saida do instalador: %s", output)
	}
	return nil
}

func (a *App) runMeshCentralInstallURL(ctx context.Context, installURL string) error {
	url := strings.TrimSpace(installURL)
	if url == "" {
		return fmt.Errorf("installUrl vazio")
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("installUrl direto implementado apenas para Windows")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("falha ao criar download do installUrl: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("falha ao baixar installUrl: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("installUrl retornou HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpFile, err := os.CreateTemp("", "meshcentral-agent-*.exe")
	if err != nil {
		return fmt.Errorf("falha ao criar arquivo temporario do instalador: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmpFile, io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmpFile.Close()
		return fmt.Errorf("falha ao salvar instalador MeshCentral: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("falha ao finalizar instalador MeshCentral: %w", err)
	}

	installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer installCancel()

	cmd := exec.CommandContext(installCtx, tmpPath)
	processutil.HideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		preview := strings.TrimSpace(out.String())
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		return fmt.Errorf("instalador via installUrl falhou: %w (%s)", err, preview)
	}
	return nil
}

func (a *App) waitMeshServiceHealthy(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, running := a.meshServiceStatus(ctx)
		if running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timeout aguardando servico MeshCentral ativo")
}

func (a *App) waitMeshNodeID(ctx context.Context, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id := normalizeMeshCentralNodeID(a.resolveMeshCentralNodeIDLocal()); id != "" {
			return id
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(2 * time.Second):
		}
	}
	return ""
}

func (a *App) meshServiceStatus(ctx context.Context) (bool, bool) {
	if runtime.GOOS != "windows" {
		return false, false
	}
	serviceNames := []string{"Mesh Agent", "MeshAgent", "meshagent"}
	present := false
	for _, name := range serviceNames {
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cmd := exec.CommandContext(queryCtx, "sc", "query", name)
		processutil.HideWindow(cmd)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			continue
		}
		present = true
		if strings.Contains(strings.ToUpper(string(out)), "RUNNING") {
			return true, true
		}
	}
	return present, false
}

func (a *App) isMeshAgentInstalledLocal() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	present, _ := a.meshServiceStatus(a.ctx)
	if present {
		return true
	}
	for _, p := range meshInstallPathCandidates() {
		if fileExists(p) {
			return true
		}
	}
	return false
}

func meshInstallPathCandidates() []string {
	return platform.MeshAgentPathCandidates()
}

func (a *App) resolveMeshCentralNodeIDLocal() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, path := range meshNodeIDPathCandidates() {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		matches := meshNodeIDPattern.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			continue
		}
		if normalized := normalizeMeshCentralNodeID(matches[1]); normalized != "" {
			return normalized
		}
	}
	return ""
}

func meshNodeIDPathCandidates() []string {
	return platform.MeshNodeIDPathCandidates()
}

func normalizeMeshCentralNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.Trim(value, `"`)
	if strings.HasPrefix(strings.ToLower(value), "node/") {
		return value
	}
	if strings.HasPrefix(strings.ToLower(value), "node//") {
		return "node/" + strings.TrimPrefix(value, "node//")
	}
	if strings.ContainsAny(value, " \\t\r\n") {
		return ""
	}
	return "node/" + value
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (a *App) meshStateCacheKey() string {
	agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
	if agentID == "" {
		agentID = "local"
	}
	return meshStateCachePrefix + agentID
}

func (a *App) loadMeshCentralState() meshCentralState {
	state := meshCentralState{}
	if a.db == nil {
		return state
	}
	if ok, err := a.db.CacheGetJSON(a.meshStateCacheKey(), &state); err != nil {
		a.meshLogf("aviso: falha ao ler estado local: %v", err)
	} else if !ok {
		if a.isMeshCentralInstalled() {
			state.Installed = true
		}
	}
	return state
}

func (a *App) saveMeshCentralState(state meshCentralState) {
	if a.db == nil {
		return
	}
	if err := a.db.CacheSetJSON(a.meshStateCacheKey(), state, 0); err != nil {
		a.meshLogf("aviso: falha ao persistir estado local: %v", err)
	}
}

func (a *App) getMeshCentralNodeIDForReport() string {
	state := a.loadMeshCentralState()
	if node := normalizeMeshCentralNodeID(state.MeshCentralNodeID); node != "" {
		return node
	}
	node := normalizeMeshCentralNodeID(a.resolveMeshCentralNodeIDLocal())
	if node == "" {
		return ""
	}
	state.MeshCentralNodeID = node
	state.NodeIDKnown = true
	state.Installed = state.Installed || a.isMeshAgentInstalledLocal()
	a.saveMeshCentralState(state)
	return node
}

func (a *App) markMeshCentralReportSuccess(nodeID string) {
	node := normalizeMeshCentralNodeID(nodeID)
	if node == "" {
		return
	}
	state := a.loadMeshCentralState()
	state.MeshCentralNodeID = node
	state.NodeIDKnown = true
	state.LastSuccessfulReport = time.Now().UTC().Format(time.RFC3339)
	state.LastInstallError = ""
	a.saveMeshCentralState(state)
}

// triggerMeshCentralInstallIfNeeded orchestrates the full install flow:
//  1. Skip if already installed (flag in config.json)
//  2. Fetch install parameters from server
//  3. Execute the installer
//  4. Mark as installed on success
func (a *App) triggerMeshCentralInstallIfNeeded(ctx context.Context) {
	a.ensureMeshCentralInstalled(ctx, "legacy-trigger", false)
}
