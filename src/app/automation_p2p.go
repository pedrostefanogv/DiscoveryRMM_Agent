package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/processutil"
	"discovery/app/core/services"
)

// errInstallerExec indica que o artifact foi adquirido com sucesso (download
// P2P ou cache local) mas a execução do instalador falhou. É distinto de
// "artifact não encontrado": nesse caso NÃO devemos re-baixar nada — o
// instalador já está em disco — e o fallback correto é o winget install
// (que conhece as flags silenciosas corretas do manifesto do pacote).
var errInstallerExec = errors.New("installer execution failed")

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

	output, p2pErr := m.installViaP2P(ctx, id)
	if p2pErr == nil {
		return output, nil
	}
	if errors.Is(p2pErr, errInstallerExec) {
		// O instalador já está em disco — re-baixar seria desperdício de banda.
		// Vai direto ao winget install, que aplica as flags corretas do manifesto.
		m.logf("[automation][p2p] instalador adquirido mas execução falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), p2pErr)
		fallbackOut, fallbackErr := m.fallback.Install(ctx, id)
		if fallbackErr != nil {
			return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; winget=%w", p2pErr, fallbackErr)
		}
		return fallbackOut, nil
	}
	m.logf("[automation][p2p] artifact nao encontrado na rede P2P, tentando download+cache packageId=%s", strings.TrimSpace(id))

	output, dlErr := m.downloadAndCacheForP2P(ctx, id)
	if dlErr == nil {
		return output, nil
	}
	m.logf("[automation][p2p] download+cache falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), dlErr)

	fallbackOut, fallbackErr := m.fallback.Install(ctx, id)
	if fallbackErr != nil {
		return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; download+cache=%v; winget=%w", p2pErr, dlErr, fallbackErr)
	}
	return fallbackOut, nil
}

func (m *automationPackageManagerRouter) Uninstall(ctx context.Context, id string) (string, error) {
	return m.fallback.Uninstall(ctx, id)
}

func (m *automationPackageManagerRouter) Upgrade(ctx context.Context, id string) (string, error) {
	if !m.shouldUseP2PForWingetInstall() {
		return m.fallback.Upgrade(ctx, id)
	}

	output, p2pErr := m.installViaP2P(ctx, id)
	if p2pErr == nil {
		return output, nil
	}
	if errors.Is(p2pErr, errInstallerExec) {
		m.logf("[automation][p2p] instalador adquirido mas execução falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), p2pErr)
		fallbackOut, fallbackErr := m.fallback.Upgrade(ctx, id)
		if fallbackErr != nil {
			return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; winget=%w", p2pErr, fallbackErr)
		}
		return fallbackOut, nil
	}
	m.logf("[automation][p2p] artifact nao encontrado na rede P2P, tentando download+cache packageId=%s", strings.TrimSpace(id))

	output, dlErr := m.downloadAndCacheForP2P(ctx, id)
	if dlErr == nil {
		return output, nil
	}
	m.logf("[automation][p2p] download+cache falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), dlErr)

	fallbackOut, fallbackErr := m.fallback.Upgrade(ctx, id)
	if fallbackErr != nil {
		return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; download+cache=%v; winget=%w", p2pErr, dlErr, fallbackErr)
	}
	return fallbackOut, nil
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
	if !m.app.GetDebugConfig().P2PWingetInstallEnabled() {
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
		// Swarm quando houver múltiplos peers com o artifact: distribui os
		// chunks entre todos, aumentando throughput e resiliência. Com um
		// único peer, o swarm degrada para o mesmo caminho chunked single-peer.
		if avail := m.app.p2pCoord.FindArtifactPeers(artifact); avail.Found && len(avail.PeerAgentIDs) > 1 {
			m.logf("[automation][p2p] baixando via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
			if _, err := m.app.p2pCoord.DownloadArtifactSwarm(ctx, artifact); err != nil {
				return "", fmt.Errorf("falha ao baixar artifact via swarm (%d peers): %w", len(avail.PeerAgentIDs), err)
			}
			m.logf("[automation][p2p] artifact baixado via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
		} else {
			if _, err := m.app.p2pCoord.DownloadArtifactFromPeer(ctx, artifact, peerID); err != nil {
				return "", fmt.Errorf("falha ao baixar artifact de peer %s: %w", peerID, err)
			}
			m.logf("[automation][p2p] artifact baixado via peer=%s artifact=%s", peerID, artifact)
		}
	} else {
		m.logf("[automation][p2p] usando artifact local artifact=%s", artifact)
	}

	artifactPath := filepath.Join(m.app.p2pTempDir(), artifact)
	output, err := runLocalInstaller(ctx, artifactPath)
	if err != nil {
		// Erro tipado: o artifact está em disco, apenas a execução falhou.
		// O caller NÃO deve re-baixar — deve ir direto ao winget install.
		return "", fmt.Errorf("%w: %v", errInstallerExec, err)
	}
	return output, nil
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

// downloadAndCacheForP2P baixa o instalador via winget download, publica no cache
// P2P e executa a instalação a partir do arquivo local. Isso garante que o primeiro
// agente que instalar um app se torne seed automaticamente para os demais.
func (m *automationPackageManagerRouter) downloadAndCacheForP2P(ctx context.Context, packageID string) (string, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("packageId vazio")
	}

	artifactID := "winget:" + normalizePackageLookupKey(packageID)
	if artifactID == "winget:" {
		return "", fmt.Errorf("packageId invalido: %s", packageID)
	}

	// Guarda anti-tempestade: se o artifact já existe no cache local com
	// manifest válido, instala direto dele SEM republicar. Republicar o mesmo
	// arquivo durante transferências de outros peers causa corrida
	// (manifest substituído no meio do download → checksum divergente) e
	// gera tempestade de publish/pull na rede quando a instalação falha e o
	// ciclo se repete.
	if existing := m.findLocalArtifactByID(artifactID); existing != "" {
		m.logf("[automation][p2p] artifact já presente no cache local, instalando sem republicar artifactID=%s artifact=%s", artifactID, existing)
		output, err := runLocalInstaller(ctx, existing)
		if err != nil {
			// Erro tipado: artifact em disco, execução falhou. O caller
			// (Install/Upgrade) vai direto ao winget install sem re-baixar.
			return "", fmt.Errorf("%w: %v", errInstallerExec, err)
		}
		return output, nil
	}

	wingetClient := m.fallback.Winget()
	if wingetClient == nil {
		return "", fmt.Errorf("winget client indisponivel")
	}

	// 1. Criar diretório temporário para o download
	tmpDir, err := os.MkdirTemp("", "p2p-winget-download-*")
	if err != nil {
		return "", fmt.Errorf("falha ao criar diretorio temporario: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			m.logf("[automation][p2p] aviso: falha ao limpar diretorio temporario %s: %v", tmpDir, removeErr)
		}
	}()

	// 2. Baixar o instalador via winget download
	m.logf("[automation][p2p] baixando instalador via winget download packageId=%s", packageID)
	if _, err := wingetClient.Download(ctx, packageID, tmpDir); err != nil {
		return "", fmt.Errorf("winget download falhou: %w", err)
	}

	// 3. Encontrar o instalador (.exe ou .msi) no diretório de download
	installerPath, err := findInstallerInDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("instalador nao encontrado no diretorio de download: %w", err)
	}
	m.logf("[automation][p2p] instalador encontrado: %s", installerPath)

	// 4. Publicar no cache P2P para que outros agentes possam baixar
	published, pubErr := m.app.p2pCoord.PublishFileWithID(installerPath, artifactID)
	if pubErr != nil {
		m.logf("[automation][p2p] aviso: falha ao publicar artifact no cache P2P: %v (instalacao continua)", pubErr)
		// Fallback: instalar direto do tmpDir se a publicação falhar.
		// Erro tipado: instalador em disco, execução falhou → caller vai
		// direto ao winget install sem re-baixar.
		output, err := runLocalInstaller(ctx, installerPath)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errInstallerExec, err)
		}
		return output, nil
	}
	m.logf("[automation][p2p] artifact publicado no cache P2P artifactID=%s artifactName=%s", artifactID, published.ArtifactName)

	// 5. Instalar a partir da cópia persistente no P2P_Temp (não do tmpDir efêmero)
	p2pInstallerPath := filepath.Join(m.app.p2pTempDir(), published.ArtifactName)
	output, err := runLocalInstaller(ctx, p2pInstallerPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errInstallerExec, err)
	}
	return output, nil
}

// findLocalArtifactByID procura um artifact no cache P2P local pelo artifactID
// e retorna o caminho completo do arquivo se ele existir com manifest válido.
// Retorna "" se não encontrado ou inválido.
func (m *automationPackageManagerRouter) findLocalArtifactByID(artifactID string) string {
	if m.app == nil || m.app.p2pCoord == nil {
		return ""
	}
	artifacts, err := m.app.ListP2PArtifacts()
	if err != nil {
		return ""
	}
	for _, a := range artifacts {
		if !strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactID) {
			continue
		}
		if !a.Available {
			continue
		}
		path := filepath.Join(m.app.p2pTempDir(), a.ArtifactName)
		if info, statErr := os.Stat(path); statErr != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		return path
	}
	return ""
}

// findInstallerInDir percorre recursivamente um diretório e retorna o caminho
// do primeiro arquivo .exe ou .msi encontrado.
func findInstallerInDir(dir string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".exe" || ext == ".msi" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("nenhum .exe ou .msi encontrado em %s", dir)
	}
	return found, nil
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

// runLocalInstaller executa o instalador local (.msi/.exe) com flags silenciosas.
// Tenta uma cascata de flags conhecidas para .exe (NSIS, Inno Setup, WiX Burn,
// InstallShield) até que uma tenha sucesso, logando qual funcionou.
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
		return runExeInstallerSilent(ctx, timeout, artifactPath)
	default:
		return "", fmt.Errorf("formato de instalador não suportado para P2P: %s", ext)
	}
}

// installerFlagCascade retorna a lista ordenada de conjuntos de flags silenciosos
// a tentar para um .exe. Para o tipo detectado, as flags do framework vêm
// PRIMEIRO, seguidas das demais da cascata completa — instaladores customizados
// às vezes contêm strings enganosas (ex.: "Nullsoft" embutido) que fazem a
// detecção errar, e a cascata garante cobertura dos outros frameworks.
func installerFlagCascade(kind installerType) [][]string {
	nsis := []string{"/S"}
	inno := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}
	wix := []string{"/quiet", "/norestart"}
	installshield := []string{"/s", "/v", "/qn"}

	// Ordem completa: do mais comum ao menos comum.
	full := [][]string{nsis, inno, wix, installshield}

	switch kind {
	case installerNSIS:
		return [][]string{nsis, inno, wix, installshield}
	case installerInno:
		return [][]string{inno, nsis, wix, installshield}
	case installerWiX:
		return [][]string{wix, nsis, inno, installshield}
	default:
		return full
	}
}

// runExeInstallerSilent executa um .exe tentando a cascata de flags silenciosas
// adequadas ao framework detectado (ou à cascata completa se desconhecido).
// Para tipos detectados, tenta primeiro as flags do framework e depois as demais
// da cascata completa — instaladores customizados às vezes contêm strings
// enganosas (ex.: "Nullsoft" embutido) que fazem a detecção errar.
// Retorna a saída do primeiro conjunto de flags que teve sucesso.
func runExeInstallerSilent(ctx context.Context, timeout time.Duration, artifactPath string) (string, error) {
	kind := detectInstallerType(artifactPath)
	cascade := installerFlagCascade(kind)

	var lastOutput string
	var lastErr error
	for i, args := range cascade {
		output, err := executeHiddenProcess(ctx, timeout, artifactPath, args)
		if err == nil {
			if i > 0 {
				// Loga qual conjunto de flags funcionou (facilita diagnóstico).
				return fmt.Sprintf("[p2p-installer] flags silenciosas bem-sucedidas: %s\n%s", strings.Join(args, " "), output), nil
			}
			return output, nil
		}
		lastOutput = output
		lastErr = err
	}
	return lastOutput, fmt.Errorf("nenhum conjunto de flags silenciosas funcionou (último: %v): %w", cascade[len(cascade)-1], lastErr)
}

// executeHiddenProcess executa um processo em background com janela oculta.
// Exit codes de SUCESSO com reboot pendente (3010 MSI/NSIS, 1641) são tratados
// como sucesso — a instalação foi concluída; tentar de novo com outras flags
// reinstalaria o produto desnecessariamente.
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
	// Exit code 3010 = ERROR_SUCCESS_REBOOT_REQUIRED; 1641 = reboot initiated.
	// Instalação bem-sucedida — não é falha.
	if exitErr, ok := err.(*exec.ExitError); ok && isRebootSuccessExitCode(exitErr.ExitCode()) {
		if text == "" {
			text = "instalação concluída (reboot pendente)"
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

// isRebootSuccessExitCode retorna true para exit codes que indicam instalação
// bem-sucedida com reboot pendente/iniciado.
func isRebootSuccessExitCode(code int) bool {
	return code == 3010 || code == 1641
}

// installerType classifica o tipo de instalador .exe para aplicar os flags
// silenciosos corretos. Cada framework de instalação tem sua própria sintaxe:
//
//	NSIS       → /S
//	Inno Setup → /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
//	WiX Burn   → /quiet /norestart
type installerType int

const (
	installerUnknown installerType = iota
	installerNSIS
	installerInno
	installerWiX
)

// detectInstallerType inspeciona os primeiros 64 KB do binário em busca de
// strings características de cada framework de instalação.
func detectInstallerType(path string) installerType {
	f, err := os.Open(path)
	if err != nil {
		return installerUnknown
	}
	defer f.Close()

	// Lê até 64 KB — suficiente para cobrir headers e stubs de todos os frameworks.
	buf := make([]byte, 64<<10)
	n, readErr := f.Read(buf)
	// io.EOF com n > 0 é sucesso: leu o arquivo inteiro (arquivo < 64 KB).
	if readErr != nil && readErr != io.EOF {
		return installerUnknown
	}
	if n == 0 {
		return installerUnknown
	}
	content := string(buf[:n])

	// NSIS: "Nullsoft" aparece no stub do instalador.
	if strings.Contains(content, "Nullsoft") {
		return installerNSIS
	}
	// Inno Setup: "Inno" + "Setup" aparecem no overlay.
	if strings.Contains(content, "Inno") && strings.Contains(content, "Setup") {
		return installerInno
	}
	// WiX Burn: "wixstdba" ou "WiX Burn" no bundle.
	if strings.Contains(content, "wixstdba") || strings.Contains(content, "WiX Burn") {
		return installerWiX
	}
	return installerUnknown
}

func (m *automationPackageManagerRouter) logf(format string, args ...any) {
	if m == nil || m.app == nil {
		return
	}
	m.app.logs.append(fmt.Sprintf(format, args...))
}
