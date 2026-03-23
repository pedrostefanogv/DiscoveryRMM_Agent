package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/internal/processutil"
)

// MeshCentralInstallInfo holds the install parameters returned by the server.
type MeshCentralInstallInfo struct {
	GroupName      string `json:"groupName"`
	MeshID         string `json:"meshId"`
	InstallURL     string `json:"installUrl"`
	InstallMode    string `json:"installMode"`
	WindowsCommand string `json:"windowsCommand"`
	LinuxCommand   string `json:"linuxCommand"`
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

// fetchMeshInstallInfo calls GET /api/agent-auth/me/support/meshcentral/install
// and returns the install parameters from the server.
func (a *App) fetchMeshInstallInfo(ctx context.Context) (MeshCentralInstallInfo, error) {
	cfg := a.GetDebugConfig()
	scheme := strings.TrimSpace(cfg.ApiScheme)
	server := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)

	if scheme == "" || server == "" {
		return MeshCentralInstallInfo{}, fmt.Errorf("servidor API nao configurado")
	}
	if token == "" {
		return MeshCentralInstallInfo{}, fmt.Errorf("token de autenticacao ausente")
	}

	url := scheme + "://" + server + "/api/agent-auth/me/support/meshcentral/install"
	a.meshLogf("consultando endpoint de instalacao: %s", url)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return MeshCentralInstallInfo{}, fmt.Errorf("falha ao criar requisicao: %w", err)
	}
	setAgentAuthHeaders(req, token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return MeshCentralInstallInfo{}, fmt.Errorf("falha na requisicao ao servidor: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return MeshCentralInstallInfo{}, fmt.Errorf("falha ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := strings.TrimSpace(string(bytes.TrimSpace(body)))
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		return MeshCentralInstallInfo{}, fmt.Errorf("servidor retornou HTTP %d: %s", resp.StatusCode, preview)
	}

	var info MeshCentralInstallInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return MeshCentralInstallInfo{}, fmt.Errorf("falha ao decodificar resposta de instalacao: %w", err)
	}

	if runtime.GOOS == "windows" && strings.TrimSpace(info.WindowsCommand) == "" {
		return MeshCentralInstallInfo{}, fmt.Errorf("servidor nao forneceu windowsCommand para este agente Windows")
	}
	if runtime.GOOS != "windows" && strings.TrimSpace(info.LinuxCommand) == "" {
		return MeshCentralInstallInfo{}, fmt.Errorf("servidor nao forneceu linuxCommand para este agente")
	}

	return info, nil
}

// runMeshCentralInstall executes the installation command provided by the server.
// On Windows uses cmd /C; on other platforms uses sh -c.
// stdout/stderr are captured and appended to the application log.
func (a *App) runMeshCentralInstall(ctx context.Context, info MeshCentralInstallInfo) error {
	installCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		command := strings.TrimSpace(info.WindowsCommand)
		a.meshLogf("executando instalacao Windows: %s", command)
		cmd = exec.CommandContext(installCtx, "cmd", "/C", command)
		processutil.HideWindow(cmd)
	} else {
		command := strings.TrimSpace(info.LinuxCommand)
		a.meshLogf("executando instalacao Linux/macOS: %s", command)
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

// triggerMeshCentralInstallIfNeeded orchestrates the full install flow:
//  1. Skip if already installed (flag in config.json)
//  2. Fetch install parameters from server
//  3. Execute the installer
//  4. Mark as installed on success
func (a *App) triggerMeshCentralInstallIfNeeded(ctx context.Context) {
	if a.isMeshCentralInstalled() {
		a.meshLogf("agent ja instalado, ignorando")
		return
	}

	a.meshLogf("meshCentralEnabledEffective=true — iniciando fluxo de instalacao")

	info, err := a.fetchMeshInstallInfo(ctx)
	if err != nil {
		a.meshLogf("falha ao obter parametros de instalacao: %v", err)
		return
	}

	a.meshLogf("parametros obtidos — groupName=%s meshId=%s mode=%s", info.GroupName, info.MeshID, info.InstallMode)

	if err := a.runMeshCentralInstall(ctx, info); err != nil {
		a.meshLogf("erro na instalacao: %v", err)
		return
	}

	a.meshLogf("instalacao concluida com sucesso")
	a.markMeshCentralInstalled()
}
