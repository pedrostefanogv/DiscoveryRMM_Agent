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

	"golang.org/x/sys/windows"

	"discovery/app/core/automation"
	"discovery/app/core/processutil"
	"discovery/app/core/selfupdate"
	"discovery/app/core/services"
	p2pmeta "discovery/app/p2pmeta"
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
	logf     func(format string, args ...any)
}

func newAutomationPackageManagerRouter(app *App, fallback *services.AppsService) *automationPackageManagerRouter {
	return &automationPackageManagerRouter{
		app:      app,
		fallback: fallback,
		logf: func(format string, args ...any) {
			app.logs.append("[automation][p2p] " + fmt.Sprintf(format, args...))
		},
	}
}

func (m *automationPackageManagerRouter) Install(ctx context.Context, id string) (string, error) {
	// Switches silenciosos do catálogo da loja (silent → silentWithProgress).
	// Usados tanto no instalador local (P2P) quanto no winget direto (--custom).
	catSilent, catSilentWithProgress := m.catalogSilentSwitches(id)

	if !m.shouldUseP2PForWingetInstall() {
		return m.fallback.InstallWithSwitches(ctx, id, catSilent, catSilentWithProgress)
	}

	output, p2pErr := m.installViaP2P(ctx, id)
	if p2pErr == nil {
		return output, nil
	}
	if errors.Is(p2pErr, errInstallerExec) {
		// O instalador já está em disco — re-baixar seria desperdício de banda.
		// Vai direto ao winget install, que aplica as flags corretas do manifesto.
		m.logf("[automation][p2p] instalador adquirido mas execução falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), p2pErr)
		fallbackOut, fallbackErr := m.fallback.InstallWithSwitches(ctx, id, catSilent, catSilentWithProgress)
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

	fallbackOut, fallbackErr := m.fallback.InstallWithSwitches(ctx, id, catSilent, catSilentWithProgress)
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

	artifact, peerIDs, err := m.resolveArtifactSources(ctx, packageID)
	if err != nil {
		return "", err
	}

	if len(peerIDs) == 0 {
		// Artifact disponível apenas localmente (cache).
		m.logf("[automation][p2p] usando artifact local artifact=%s", artifact)
	} else {
		// Swarm quando houver múltiplos peers com o artifact: distribui os
		// chunks entre todos, aumentando throughput e resiliência. Com um
		// único peer, o swarm degrada para o mesmo caminho chunked single-peer.
		swarmTried := false
		if len(peerIDs) > 1 {
			if avail := m.app.p2pCoord.FindArtifactPeersByID(artifact); avail.Found && len(avail.PeerAgentIDs) > 1 {
				m.logf("[automation][p2p] baixando via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
				if _, err := m.app.p2pCoord.DownloadArtifactSwarm(ctx, artifact); err != nil {
					m.logf("[automation][p2p] swarm falhou, tentando peers individualmente artifact=%s motivo=%v", artifact, err)
				} else {
					m.logf("[automation][p2p] artifact baixado via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
					swarmTried = true
				}
			}
		}
		if !swarmTried {
			// Tenta cada peer candidato em ordem. Cache stale (peer não tem
			// mais o arquivo) invalida a entrada e segue para o próximo peer —
			// só cai no fallback winget depois de esgotar todos os peers.
			var lastErr error
			downloaded := false
			for i, peerID := range peerIDs {
				if _, err := m.app.p2pCoord.DownloadArtifactFromPeer(ctx, artifact, peerID); err != nil {
					lastErr = err
					m.logf("[automation][p2p] download falhou via peer=%s (%d/%d), tentando próximo artifact=%s motivo=%v", peerID, i+1, len(peerIDs), artifact, err)
					continue
				}
				m.logf("[automation][p2p] artifact baixado via peer=%s artifact=%s", peerID, artifact)
				downloaded = true
				break
			}
			if !downloaded {
				return "", fmt.Errorf("falha ao baixar artifact de %d peer(s): %w", len(peerIDs), lastErr)
			}
		}
	}

	artifactPath := filepath.Join(m.app.p2pTempDir(), artifact)
	catSilent, catSilentWithProgress := m.catalogSilentSwitches(packageID)
	output, err := runLocalInstallerWithSwitches(ctx, artifactPath, catSilent, catSilentWithProgress)
	if err != nil {
		// Erro tipado: o artifact está em disco, apenas a execução falhou.
		// O caller NÃO deve re-baixar — deve ir direto ao winget install.
		return "", fmt.Errorf("%w: %v", errInstallerExec, err)
	}
	return output, nil
}

// resolveArtifactSources localiza o artifact do pacote e retorna TODOS os
// peers que o anunciam (ordenados), além do nome do arquivo. Tenta primeiro o
// cache local, depois o índice de artifacts dos peers (gossip/live).
// Retorna peerIDs vazio quando o artifact está disponível apenas localmente.
func (m *automationPackageManagerRouter) resolveArtifactSources(ctx context.Context, packageID string) (artifactName string, peerIDs []string, err error) {
	// artifactID exato: "winget:<packageId>" — lookup deterministico, nao fuzzy.
	artifactLookupID := "winget:" + normalizePackageLookupKey(packageID)
	if artifactLookupID == "winget:" {
		return "", nil, fmt.Errorf("packageId invalido: %s", packageID)
	}

	// 1. Tenta local primeiro (cache)
	artifacts, listErr := m.app.ListP2PArtifacts()
	if listErr == nil {
		for _, a := range artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				return a.ArtifactName, nil, nil
			}
		}
	}

	// 2. Busca em TODOS os peers via gossip (não apenas o primeiro match).
	m.app.p2pCoord.RefreshPeerArtifactIndex(ctx, "automation-install")
	index := m.app.GetP2PPeerArtifactIndex()
	for _, peer := range index {
		for _, a := range peer.Artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				peerIDs = append(peerIDs, strings.TrimSpace(peer.PeerAgentID))
				if artifactName == "" {
					artifactName = a.ArtifactName
				}
				break
			}
		}
	}
	if artifactName != "" && len(peerIDs) > 0 {
		return artifactName, peerIDs, nil
	}

	return "", nil, fmt.Errorf("nenhum artifact P2P encontrado para packageId=%s (artifactID=%s)", packageID, artifactLookupID)
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
		catSilent, catSilentWithProgress := m.catalogSilentSwitches(packageID)
		output, err := runLocalInstallerWithSwitches(ctx, existing, catSilent, catSilentWithProgress)
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

	// 3b. Extrair a versão real do instalador (nome do arquivo ou output do
	// winget upgrade) para versionar o artifact no P2P — permite que outros
	// agents comparem "versão disponível na rede" vs "versão instalada" e
	// evitem loops de update por catálogo defasado.
	installerVersion := automation.VersionFromInstallerFilename(filepath.Base(installerPath))
	if installerVersion == "" {
		if wc := m.fallback.Winget(); wc != nil {
			if upOut, upErr := wc.ListUpgradable(ctx); upErr == nil {
				installerVersion = automation.FindVersionInOutput(upOut, packageID, "available")
			}
		}
	}
	if installerVersion != "" {
		m.logf("[automation][p2p] versão real do instalador: %s packageId=%s", installerVersion, packageID)
	}

	// 4. Publicar no cache P2P (com versão quando conhecida)
	var published p2pmeta.ArtifactView
	var pubErr error
	if installerVersion != "" {
		published, pubErr = m.app.p2pCoord.PublishFileWithIDAndVersion(installerPath, artifactID, installerVersion)
	} else {
		published, pubErr = m.app.p2pCoord.PublishFileWithID(installerPath, artifactID)
	}
	if pubErr != nil {
		m.logf("[automation][p2p] aviso: falha ao publicar artifact no cache P2P: %v (instalacao continua)", pubErr)
		// Fallback: instalar direto do tmpDir se a publicação falhar.
		// Erro tipado: instalador em disco, execução falhou → caller vai
		// direto ao winget install sem re-baixar.
		catSilent, catSilentWithProgress := m.catalogSilentSwitches(packageID)
		output, err := runLocalInstallerWithSwitches(ctx, installerPath, catSilent, catSilentWithProgress)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errInstallerExec, err)
		}
		return output, nil
	}
	m.logf("[automation][p2p] artifact publicado no cache P2P artifactID=%s artifactName=%s", artifactID, published.ArtifactName)

	// 5. Instalar a partir da cópia persistente no P2P_Temp (não do tmpDir efêmero)
	p2pInstallerPath := filepath.Join(m.app.p2pTempDir(), published.ArtifactName)
	catSilent, catSilentWithProgress := m.catalogSilentSwitches(packageID)
	output, err := runLocalInstallerWithSwitches(ctx, p2pInstallerPath, catSilent, catSilentWithProgress)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errInstallerExec, err)
	}
	return output, nil
}

// catalogSilentSwitches busca os switches silenciosos do pacote no cache da
// loja de aplicativos (silent → silentWithProgress). Retorna strings vazias
// quando o pacote não está no catálogo ou não tem switches — nesse caso o
// instalador cai na cascata heurística (comportamento anterior).
// Nunca falha: erro de cache/loja é tratado como "sem switches".
func (m *automationPackageManagerRouter) catalogSilentSwitches(packageID string) (string, string) {
	if m.app == nil || m.app.appStoreSvc == nil {
		return "", ""
	}
	item, err := m.app.appStoreSvc.ResolveAllowedPackage(m.app.ctx, packageID)
	if err != nil {
		// Pacote fora da política/loja: sem switches, usa cascata heurística.
		return "", ""
	}
	silent := strings.TrimSpace(item.SilentCommand)
	silentWithProgress := strings.TrimSpace(item.SilentWithProgress)
	if silent == "" {
		silent = silentWithProgress
	}
	if silent != "" {
		m.logf("[automation][p2p] switches do catálogo disponíveis packageId=%s silent=%q silentWithProgress=%q", packageID, silent, silentWithProgress)
	}
	return silent, silentWithProgress
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
// catalogSilent contém os switches vindos do catálogo da loja (campo "silent"),
// que têm PRIORIDADE sobre a detecção heurística — elimina o "adivinhar".
// Tenta uma cascata de flags conhecidas para .exe (NSIS, Inno Setup, WiX Burn,
// InstallShield) até que uma tenha sucesso, logando qual funcionou.
func runLocalInstaller(ctx context.Context, artifactPath string) (string, error) {
	return runLocalInstallerWithSwitches(ctx, artifactPath, "", "")
}

// runLocalInstallerWithSwitches executa o instalador local (.msi/.exe) usando
// os switches do catálogo como primeira tentativa (silent → silentWithProgress),
// com fallback para a cascata heurística quando não houver switches ou falharem.
func runLocalInstallerWithSwitches(ctx context.Context, artifactPath, silent, silentWithProgress string) (string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", fmt.Errorf("artifact path vazio")
	}
	ext := strings.ToLower(filepath.Ext(artifactPath))
	timeout := 20 * time.Minute
	switch ext {
	case ".msi":
		// MSI: switches do catálogo substituem o padrão /qn /norestart quando presentes.
		if sw := firstNonEmpty(silent, silentWithProgress); sw != "" {
			return executeHiddenProcess(ctx, timeout, "msiexec", append([]string{"/i", artifactPath}, splitInstallerSwitches(sw)...))
		}
		return executeHiddenProcess(ctx, timeout, "msiexec", []string{"/i", artifactPath, "/qn", "/norestart"})
	case ".exe":
		return runExeInstallerSilent(ctx, timeout, artifactPath, silent, silentWithProgress)
	default:
		return "", fmt.Errorf("formato de instalador não suportado para P2P: %s", ext)
	}
}

// installerType identifica o framework do instalador .exe, usado para ordenar
// a cascata de flags silenciosas.
type installerType int

const (
	installerUnknown installerType = iota
	installerNSIS
	installerInno
	installerWiX
	installerInstallShield
)

// detectInstallerType faz uma detecção best-effort do framework do instalador
// lendo strings do binário. Instaladores customizados às vezes contêm strings
// enganosas (ex.: "Nullsoft" embutido num bundle WiX), por isso o resultado é
// apenas uma dica de ordenação — a cascata completa cobre os demais casos.
func detectInstallerType(artifactPath string) installerType {
	f, err := os.Open(artifactPath)
	if err != nil {
		return installerUnknown
	}
	defer f.Close()

	// Lê no máximo os primeiros 512KB — as assinaturas dos frameworks ficam
	// nas tabelas de strings do PE, tipicamente no início do arquivo.
	const maxScan = 512 * 1024
	buf := make([]byte, maxScan)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return installerUnknown
	}
	s := strings.ToLower(string(buf[:n]))

	switch {
	case strings.Contains(s, "nullsoft"):
		return installerNSIS
	case strings.Contains(s, "inno setup"):
		return installerInno
	case strings.Contains(s, "wixburn") || strings.Contains(s, "burn engine"):
		return installerWiX
	case strings.Contains(s, "installshield"):
		return installerInstallShield
	default:
		return installerUnknown
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

// runExeInstallerSilent executa um .exe tentando, nesta ordem:
//  1. switches silenciosos do catálogo (silent → silentWithProgress) — dados
//     oficiais do manifesto winget, sem adivinhação;
//  2. cascata heurística por framework detectado (NSIS/Inno/WiX/InstallShield);
//  3. cascata completa.
//
// Retorna a saída do primeiro conjunto de flags que teve sucesso.
func runExeInstallerSilent(ctx context.Context, timeout time.Duration, artifactPath, silent, silentWithProgress string) (string, error) {
	var lastOutput string
	var lastErr error

	// Prioridade 1: switches oficiais do catálogo (silent → silentWithProgress).
	// São divididos em campos antes de passar ao processo (ex.: "/S /PreventReboot=true").
	for _, candidate := range []struct {
		name string
		args string
	}{{"silent", silent}, {"silentWithProgress", silentWithProgress}} {
		sw := strings.TrimSpace(candidate.args)
		if sw == "" {
			continue
		}
		output, err := executeHiddenProcess(ctx, timeout, artifactPath, splitInstallerSwitches(sw))
		if err == nil {
			return fmt.Sprintf("[p2p-installer] switches do catálogo (%s) bem-sucedidos: %s\n%s", candidate.name, sw, output), nil
		}
		lastOutput, lastErr = output, err
	}

	// Fallback: cascata heurística por framework detectado (comportamento anterior).
	kind := detectInstallerType(artifactPath)
	cascade := installerFlagCascade(kind)

	for i, args := range cascade {
		output, err := executeHiddenProcess(ctx, timeout, artifactPath, args)
		if err == nil {
			if i > 0 {
				// Loga qual conjunto de flags funcionou (facilita diagnóstico).
				return fmt.Sprintf("[p2p-installer] flags heurísticas bem-sucedidas: %s\n%s", strings.Join(args, " "), output), nil
			}
			return output, nil
		}
		lastOutput = output
		lastErr = err
	}
	return lastOutput, fmt.Errorf("nenhum conjunto de flags silenciosas funcionou (último: %v): %w", cascade[len(cascade)-1], lastErr)
}

// splitInstallerSwitches divide a string de switches respeitando aspas
// (ex.: INSTALLDIR="C:/Program Files/App" vira um único argumento).
func splitInstallerSwitches(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote := false
	hasToken := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			hasToken = true
		case r == ' ' && !inQuote:
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args
}

// firstNonEmpty retorna a primeira string não vazia (após TrimSpace).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// executeHiddenProcess executa um processo em background com janela oculta.
// Exit codes de SUCESSO com reboot pendente (3010 MSI/NSIS, 1641) são tratados
// como sucesso — a instalação foi concluída; tentar de novo com outras flags
// reinstalaria o produto desnecessariamente.
//
// Fallback de elevação: se o CreateProcess falhar com ERROR_ELEVATION_REQUIRED
// ("A operação solicitada requer elevação"), o instalador exige admin e o
// agente não está elevado. Nesse caso tentamos ShellExecuteEx("runas") via
// selfupdate.LaunchInstallerElevated, que dispara o prompt UAC na sessão do
// usuário. Se o usuário aceitar, a instalação prossegue; se negar, retornamos
// o erro original (o router cai para o fallback winget).
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
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timeout na execução do instalador")
	}
	// ── Fallback de elevação (Windows) ──
	// fork/exec falhou porque o binário exige elevação e o agente não é admin.
	// Tentamos ShellExecuteEx("runas") — dispara UAC na sessão do usuário.
	if isElevationRequiredExecError(err) {
		if elevOut, elevErr := launchInstallerViaUAC(executable, args); elevErr == nil {
			return elevOut, nil
		}
		// UAC negado/indisponível: retorna o erro original para o router
		// seguir o fluxo de fallback (winget direto).
	}
	return text, err
}

// isElevationRequiredExecError detecta falha de CreateProcess por exigência de
// elevação (ERROR_ELEVATION_REQUIRED 740 / "requer elevação" / "requested
// operation requires elevation"). Só faz sentido em Windows; em outros SO o
// erro nunca contém essas strings, então a checagem por texto é suficiente.
//
// NOTA: NÃO usamos match solto por "740" — qualquer mensagem contendo esse
// número (caminho, exit code de outra natureza, tamanho em bytes) dispararia
// um prompt UAC indevido. O texto do erro do Windows para o erro 740 é
// "A operação solicitada requer elevação" / "The requested operation requires
// elevation", que já é coberto pelos matches de texto abaixo.
func isElevationRequiredExecError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "requer elevação") ||
		strings.Contains(msg, "requires elevation") ||
		strings.Contains(msg, "elevation required") ||
		strings.Contains(msg, "elevação é necessária") ||
		strings.Contains(msg, "elevation is required")
}

// launchInstallerViaUAC lança o instalador com elevação UAC via
// ShellExecuteEx("runas") e aguarda a conclusão (com timeout) para capturar o
// exit code. Reaproveita LaunchInstallerElevated do selfupdate.
//
// O handle retornado é windows.Handle (raw). Não há Close/Wait nele — usamos
// WaitForSingleObject via syscall e fechamos com CloseHandle explicitamente.
func launchInstallerViaUAC(executable string, args []string) (string, error) {
	_, handle, err := selfupdate.LaunchInstallerElevated(executable, strings.Join(args, " "))
	if err != nil {
		return "", fmt.Errorf("elevação UAC falhou: %w", err)
	}
	defer windows.CloseHandle(handle)

	const waitTimeout = 20 * time.Minute
	event, err := windows.WaitForSingleObject(handle, uint32(waitTimeout.Milliseconds()))
	if err != nil {
		return "", fmt.Errorf("espera do instalador elevado falhou: %w", err)
	}
	const waitTimeoutConst = 0x00000102 // WAIT_TIMEOUT
	if event == waitTimeoutConst {
		return "", fmt.Errorf("timeout aguardando instalador elevado")
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", fmt.Errorf("obtendo exit code do instalador elevado: %w", err)
	}
	if exitCode == 0 || exitCode == 3010 || exitCode == 1641 {
		return "instalação concluída via UAC (elevação aceita)", nil
	}
	return "", fmt.Errorf("instalador elevado saiu com código %d", exitCode)
}

// isRebootSuccessExitCode retorna true para exit codes que indicam instalação
// bem-sucedida com reboot pendente/iniciado.
func isRebootSuccessExitCode(code int) bool {
	return code == 3010 || code == 1641
}

// resolveP2PPackageVersion retorna a versão do artifact "winget:<packageId>"
// consultando (1) o cache local e (2) o índice de artifacts dos peers.
// Usado pela decisão versionada do executor (evitar loop por catálogo defasado).
// Retorna "" quando o artifact não existe ou não tem versão.
func (m *automationPackageManagerRouter) resolveP2PPackageVersion(packageID string) string {
	if m == nil || m.app == nil || m.app.p2pCoord == nil {
		return ""
	}
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return ""
	}
	artifactLookupID := "winget:" + normalizePackageLookupKey(packageID)

	// 1. Cache local.
	if artifacts, err := m.app.ListP2PArtifacts(); err == nil {
		for _, a := range artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				if v := strings.TrimSpace(a.Version); v != "" {
					return v
				}
			}
		}
	}

	// 2. Índice de peers (gossip/live). Pega a maior versão anunciada.
	best := ""
	for _, peer := range m.app.GetP2PPeerArtifactIndex() {
		for _, a := range peer.Artifacts {
			if !strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactLookupID) {
				continue
			}
			v := strings.TrimSpace(a.Version)
			if v == "" {
				continue
			}
			if best == "" || automation.CompareVersions(v, best) > 0 {
				best = v
			}
		}
	}
	return best
}
