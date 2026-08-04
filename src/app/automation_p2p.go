package app

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/processutil"
	"discovery/app/core/services"
)

type automationPackageManagerRouter struct {
	app      *App
	fallback *services.AppsService
}

func newAutomationPackageManagerRouter(app *App, fallback *services.AppsService) *automationPackageManagerRouter {
	return &automationPackageManagerRouter{app: app, fallback: fallback}
}

func (m *automationPackageManagerRouter) Install(ctx context.Context, id string) (string, error) {
	if !m.shouldUseP2PForWingetInstall() {
		return m.fallback.Install(ctx, id)
	}

	output, err := m.installViaP2P(ctx, id)
	if err == nil {
		return output, nil
	}
	m.logf("[automation][p2p] fallback para winget install packageId=%s motivo=%v", strings.TrimSpace(id), err)
	fallbackOut, fallbackErr := m.fallback.Install(ctx, id)
	if fallbackErr != nil {
		return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; winget=%w", err, fallbackErr)
	}
	return fallbackOut, nil
}

func (m *automationPackageManagerRouter) Uninstall(ctx context.Context, id string) (string, error) {
	return m.fallback.Uninstall(ctx, id)
}

func (m *automationPackageManagerRouter) Upgrade(ctx context.Context, id string) (string, error) {
	return m.fallback.Upgrade(ctx, id)
}

func (m *automationPackageManagerRouter) UpgradeAll(ctx context.Context) (string, error) {
	return m.fallback.UpgradeAll(ctx)
}

func (m *automationPackageManagerRouter) ListInstalled(ctx context.Context) (string, error) {
	return m.fallback.ListInstalled(ctx)
}

func (m *automationPackageManagerRouter) ListUpgradable(ctx context.Context) (string, error) {
	return m.fallback.ListUpgradable(ctx)
}

func (m *automationPackageManagerRouter) shouldUseP2PForWingetInstall() bool {
	if m == nil || m.app == nil || m.fallback == nil {
		return false
	}
	cfg := m.app.GetDebugConfig()
	if !cfg.AutomationP2PWingetInstallEnabled {
		return false
	}
	if !m.app.runtimeFlags.DebugMode {
		return false
	}
	p2pCfg := m.app.GetP2PConfig()
	if !p2pCfg.Enabled {
		return false
	}
	if m.app.p2pCoord == nil {
		return false
	}
	return true
}

func (m *automationPackageManagerRouter) installViaP2P(ctx context.Context, packageID string) (string, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("packageId vazio")
	}

	artifact, peerID, err := m.resolveArtifactSource(ctx, packageID)
	if err != nil {
		return "", err
	}

	if peerID != "" {
		if _, err := m.app.p2pCoord.DownloadArtifactFromPeer(ctx, artifact, peerID); err != nil {
			return "", fmt.Errorf("falha ao baixar artifact de peer %s: %w", peerID, err)
		}
		m.logf("[automation][p2p] artifact baixado via peer=%s artifact=%s", peerID, artifact)
	} else {
		m.logf("[automation][p2p] usando artifact local artifact=%s", artifact)
	}

	artifactPath := filepath.Join(m.app.p2pTempDir(), artifact)
	return runLocalInstaller(ctx, artifactPath)
}

func (m *automationPackageManagerRouter) resolveArtifactSource(ctx context.Context, packageID string) (artifactName string, sourcePeerID string, err error) {
	// artifactID exato: "winget:<packageId>" — lookup deterministico, nao fuzzy.
	artifactLookupID := "winget:" + normalizePackageLookupKey(packageID)
	if artifactLookupID == "winget:" {
		return "", "", fmt.Errorf("packageId invalido: %s", packageID)
	}

	// 1. Tenta local primeiro (cache)
	artifacts, listErr := m.app.ListP2PArtifacts()
	if listErr == nil {
		for _, a := range artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				return a.ArtifactName, "", nil
			}
		}
	}

	// 2. Busca em peers via gossip
	m.app.p2pCoord.RefreshPeerArtifactIndex(ctx, "automation-install")
	index := m.app.GetP2PPeerArtifactIndex()
	for _, peer := range index {
		for _, a := range peer.Artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				return a.ArtifactName, strings.TrimSpace(peer.PeerAgentID), nil
			}
		}
	}

	return "", "", fmt.Errorf("nenhum artifact P2P encontrado para packageId=%s (artifactID=%s)", packageID, artifactLookupID)
}

func normalizePackageLookupKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func runLocalInstaller(ctx context.Context, artifactPath string) (string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", fmt.Errorf("artifact path vazio")
	}
	ext := strings.ToLower(filepath.Ext(artifactPath))
	timeout := 20 * time.Minute
	switch ext {
	case ".msi":
		return executeHiddenProcess(ctx, timeout, "msiexec", []string{"/i", artifactPath, "/qn", "/norestart"})
	case ".exe":
		return executeHiddenProcess(ctx, timeout, artifactPath, []string{"/quiet", "/norestart"})
	default:
		return "", fmt.Errorf("formato de instalador não suportado para P2P: %s", ext)
	}
}

func executeHiddenProcess(parent context.Context, timeout time.Duration, executable string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, args...)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err == nil {
		if text == "" {
			text = "instalação concluída"
		}
		return text, nil
	}
	if text == "" {
		text = err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timeout na execução do instalador")
	}
	return text, err
}

func (m *automationPackageManagerRouter) logf(format string, args ...any) {
	if m == nil || m.app == nil {
		return
	}
	m.app.logs.append(fmt.Sprintf(format, args...))
}
