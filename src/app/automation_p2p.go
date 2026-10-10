package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/automation"
	"discovery/app/core/platform"
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
			app.Logs.Append("[automation][p2p] " + fmt.Sprintf(format, args...))
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
		// Instalador saiu 0 — mas stub/online pode sair sem instalar. Confirma o
		// estado real; se não instalou, cai para o winget (escopo correto).
		if m.verifyLocalInstallResult(ctx, id, "install") {
			return output, nil
		}
		m.logf("[automation][p2p] instalador local concluiu sem instalar o pacote, fallback para winget direto packageId=%s", strings.TrimSpace(id))
	} else if errors.Is(p2pErr, errInstallerExec) {
		// O instalador já está em disco — re-baixar seria desperdício de banda.
		// Vai direto ao winget install, que aplica as flags corretas do manifesto.
		m.logf("[automation][p2p] instalador adquirido mas execução falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), p2pErr)
	} else {
		m.logf("[automation][p2p] artifact nao encontrado na rede P2P, tentando download+cache packageId=%s", strings.TrimSpace(id))

		output, dlErr := m.downloadAndCacheForP2P(ctx, id)
		if dlErr == nil {
			if m.verifyLocalInstallResult(ctx, id, "install") {
				return output, nil
			}
			m.logf("[automation][p2p] instalador baixado concluiu sem instalar o pacote, fallback para winget direto packageId=%s", strings.TrimSpace(id))
		} else {
			m.logf("[automation][p2p] download+cache falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), dlErr)
		}
	}

	fallbackOut, fallbackErr := m.fallback.InstallWithSwitches(ctx, id, catSilent, catSilentWithProgress)
	if fallbackErr != nil {
		return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; winget=%w", p2pErr, fallbackErr)
	}
	return fallbackOut, nil
}

func (m *automationPackageManagerRouter) Uninstall(ctx context.Context, id string) (string, error) {
	return m.fallback.Uninstall(ctx, id)
}

func (m *automationPackageManagerRouter) Upgrade(ctx context.Context, id string) (string, error) {
	// Switches silenciosos do catálogo também no upgrade (paridade com o install):
	// o winget anexa via --custom e o guard de MSI evita switches exe-only.
	catSilent, catSilentWithProgress := m.catalogSilentSwitches(id)

	if !m.shouldUseP2PForWingetInstall() {
		return m.fallback.UpgradeWithSwitches(ctx, id, catSilent, catSilentWithProgress)
	}

	output, p2pErr := m.installViaP2P(ctx, id)
	if p2pErr == nil {
		if m.verifyLocalInstallResult(ctx, id, "upgrade") {
			return output, nil
		}
		m.logf("[automation][p2p] instalador local concluiu sem atualizar o pacote, fallback para winget direto packageId=%s", strings.TrimSpace(id))
	} else if errors.Is(p2pErr, errInstallerExec) {
		m.logf("[automation][p2p] instalador adquirido mas execução falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), p2pErr)
	} else {
		m.logf("[automation][p2p] artifact nao encontrado na rede P2P, tentando download+cache packageId=%s", strings.TrimSpace(id))

		output, dlErr := m.downloadAndCacheForP2P(ctx, id)
		if dlErr == nil {
			if m.verifyLocalInstallResult(ctx, id, "upgrade") {
				return output, nil
			}
			m.logf("[automation][p2p] instalador baixado concluiu sem atualizar o pacote, fallback para winget direto packageId=%s", strings.TrimSpace(id))
		} else {
			m.logf("[automation][p2p] download+cache falhou, fallback para winget direto packageId=%s motivo=%v", strings.TrimSpace(id), dlErr)
		}
	}

	fallbackOut, fallbackErr := m.fallback.UpgradeWithSwitches(ctx, id, catSilent, catSilentWithProgress)
	if fallbackErr != nil {
		return fallbackOut, fmt.Errorf("p2p e winget falharam: p2p=%v; winget=%w", p2pErr, fallbackErr)
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

// ListInstalledRich/ListUpgradableRich expõem o JSON do winget (Ids completos)
// para a decisão de instalação (automation.richPackageLister). A saída crua da
// UI/MCP continua usando ListInstalled/ListUpgradable (tabela).
func (m *automationPackageManagerRouter) ListInstalledRich(ctx context.Context) (string, error) {
	return m.fallback.ListInstalledRich(ctx)
}

func (m *automationPackageManagerRouter) ListUpgradableRich(ctx context.Context) (string, error) {
	return m.fallback.ListUpgradableRich(ctx)
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
	if m.app.P2PCoord == nil {
		return false
	}
	return true
}

// p2pReadinessWaitTimeout é o teto de espera pelo discovery inicial do P2P
// antes de prosseguir com o fluxo normal (fallback winget preservado).
// O probe de startup varre a LAN em segundos; 45s cobre redes grandes
// (probe de /24 inteiro) e hosts lentos sem atrasar tasks indefinidamente.
const p2pReadinessWaitTimeout = 45 * time.Second

// waitForP2PReadiness bloqueia até o discovery inicial do P2P concluir
// (primeiro lan-probe terminado OU primeiro peer confirmado), respeitando
// o ctx do caller e o timeout global. Retorna false quando o prazo
// expirou — o caller prossegue com o comportamento normal (o resolve pode
// ainda encontrar artifacts via gossip/index já populado, ou cair no
// fallback winget, exatamente como antes).
func (m *automationPackageManagerRouter) waitForP2PReadiness(ctx context.Context) bool {
	if m == nil || m.app == nil || m.app.P2PCoord == nil {
		return true // sem coordinator: nada a esperar, não bloqueia
	}
	readyCh := m.app.P2PCoord.ReadyCh()
	select {
	case <-readyCh:
		return true
	default:
		m.logf("[automation][p2p] aguardando discovery inicial do P2P (teto %s)", p2pReadinessWaitTimeout)
	}
	timer := time.NewTimer(p2pReadinessWaitTimeout)
	defer timer.Stop()
	select {
	case <-readyCh:
		m.logf("[automation][p2p] discovery inicial concluído, consultando rede P2P")
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (m *automationPackageManagerRouter) installViaP2P(ctx context.Context, packageID string) (string, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("packageId vazio")
	}

	// Warmup: se o P2P ainda não concluiu o discovery inicial (comum no
	// startup do agent, quando a automação dispara antes do lan-probe),
	// espera com timeout antes de resolver fontes. Sem isso, o resolve
	// reporta "artifact não encontrado" enquanto os peers da LAN ainda
	// não foram descobertos — e o fallback baixa da internet
	// desnecessariamente (mesmos MB que peers vizinhos têm em cache).
	if !m.waitForP2PReadiness(ctx) {
		m.logf("[automation][p2p] P2P discovery não concluiu no prazo, prosseguindo sem espera packageId=%s", packageID)
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
			if avail := m.app.P2PCoord.FindArtifactPeersByID(artifact); avail.Found && len(avail.PeerAgentIDs) > 1 {
				m.logf("[automation][p2p] baixando via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
				if _, err := m.app.P2PCoord.DownloadArtifactSwarm(ctx, artifact); err != nil {
					m.logf("[automation][p2p] swarm falhou, tentando peers individualmente artifact=%s motivo=%v", artifact, err)
				} else {
					m.logf("[automation][p2p] artifact baixado via swarm peers=%d artifact=%s", len(avail.PeerAgentIDs), artifact)
					swarmTried = true
				}
			}
		}
		if !swarmTried {
			// Ordena fontes por score (peer ativo + conectado via libp2p +
			// anúncio fresco primeiro). Assim, entre os agentes que possuem
			// o mesmo artifact, a transferência prefere o melhor provedor.
			if lookupID := "winget:" + normalizePackageLookupKey(packageID); lookupID != "winget:" {
				if scored := m.app.P2PCoord.PeersWithArtifactScored(lookupID); len(scored) > 0 {
					ordered := make([]string, 0, len(scored))
					for _, cand := range scored {
						for _, id := range peerIDs {
							if strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(cand.AgentID)) {
								ordered = append(ordered, id)
								break
							}
						}
					}
					if len(ordered) == len(peerIDs) {
						peerIDs = ordered
						m.logf("[automation][p2p] fontes ordenadas por score artifact=%s melhor_peer=%s score=%.2f", artifact, peerIDs[0], scored[0].Score)
					}
				}
			}
			// Tenta cada peer candidato em ordem. Cache stale (peer não tem
			// mais o arquivo) invalida a entrada e segue para o próximo peer —
			// só cai no fallback winget depois de esgotar todos os peers.
			var lastErr error
			downloaded := false
			for i, peerID := range peerIDs {
				if _, err := m.app.P2PCoord.DownloadArtifactFromPeer(ctx, artifact, peerID); err != nil {
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

	// Persiste o ID lógico no sidecar .meta do artifact baixado por P2P: sem
	// isso, ListArtifacts o anuncia como "name:<arquivo>" e o re-seed (e a
	// própria automação) o trata como ausente, re-disparando o download.
	if len(peerIDs) > 0 {
		if coord := m.app.P2PCoord; coord != nil {
			coord.RememberArtifactIdentity(artifact, "winget:"+normalizePackageLookupKey(packageID))
		}
	}

	artifactPath := filepath.Join(m.app.p2pTempDir(), artifact)
	catSilent, catSilentWithProgress, catInstallerType := m.catalogInstallerInfo(packageID)
	output, err := runLocalInstallerFull(ctx, artifactPath, catSilent, catSilentWithProgress, catInstallerType)
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
	m.app.P2PCoord.RefreshPeerArtifactIndex(ctx, "automation-install")
	// Índice do cache (sem rede): GetP2PPeerArtifactIndex faz fetch live de
	// TODOS os peers (5s cada) e não deve rodar a cada execução de tarefa.
	index := m.app.GetP2PPeerArtifactIndexCached()
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
		// O ID lógico local pode divergir do anunciado quando o sidecar .meta
		// não existe (artifact recebido via download P2P). Se já temos o
		// arquivo em disco com o mesmo nome, instalamos direto sem re-baixar.
		if existing := m.findLocalArtifactByName(artifactName); existing != "" {
			m.logf("[automation][p2p] artifact já presente no cache local (por nome), instalando sem re-baixar artifact=%s", artifactName)
			return artifactName, nil, nil
		}
		return artifactName, peerIDs, nil
	}

	return "", nil, fmt.Errorf("nenhum artifact P2P encontrado para packageId=%s (artifactID=%s)", packageID, artifactLookupID)
}

// PreloadPackageForP2P faz fetch proativo de um instalador: baixa via winget
// download e publica no cache P2P — SEM instalar. Usado quando o servidor
// pede pré-carga de packageIds (policy-sync → PreloadPackageIds), para que
// sites em deploy tenham os instaladores já na rede P2P quando as tasks de
// instalação executarem. Não retorna erro ao caller (best-effort, só log):
// falha aqui não deve afetar o ciclo de automação.
func (m *automationPackageManagerRouter) PreloadPackageForP2P(ctx context.Context, packageID string) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" || m.app.P2PCoord == nil {
		return
	}
	artifactID := "winget:" + normalizePackageLookupKey(packageID)
	if artifactID == "winget:" {
		return
	}

	// Já temos o artifact localmente — nada a fazer.
	if existing := m.findLocalArtifactByID(artifactID); existing != "" {
		m.logf("[automation][p2p] preload: artifact já presente localmente packageId=%s", packageID)
		return
	}

	// Já existe na rede? Baixa dos peers (swarm) em vez da internet.
	m.app.P2PCoord.RefreshPeerArtifactIndex(ctx, "preload")
	index := m.app.GetP2PPeerArtifactIndex()
	for _, peer := range index {
		for _, a := range peer.Artifacts {
			if !strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactID) {
				continue
			}
			artifactName := strings.TrimSpace(a.ArtifactName)
			if artifactName == "" {
				continue
			}
			if scored := m.app.P2PCoord.PeersWithArtifactScored(artifactID); len(scored) > 0 {
				if _, err := m.app.P2PCoord.DownloadArtifactSwarm(ctx, artifactName); err == nil {
					m.logf("[automation][p2p] preload: artifact baixado da rede packageId=%s artifact=%s", packageID, artifactName)
					return
				}
				// Swarm falhou — tenta peers individualmente na ordem de score.
				downloaded := false
				for _, cand := range scored {
					if _, err := m.app.P2PCoord.DownloadArtifactFromPeer(ctx, artifactName, cand.AgentID); err == nil {
						m.logf("[automation][p2p] preload: artifact baixado via peer packageId=%s peer=%s", packageID, cand.AgentID)
						downloaded = true
						break
					}
				}
				if downloaded {
					return
				}
			}
		}
	}

	// Não existe na rede: baixa via winget download e publica (vira seed).
	wingetClient := m.fallback.Winget()
	if wingetClient == nil {
		m.logf("[automation][p2p] preload: winget client indisponivel packageId=%s", packageID)
		return
	}
	// Diretório temporário dentro de %WINDIR%/Temp/Discovery (convenção do
	// agente) — não na temp genérica do processo (os.TempDir vira
	// C:/Windows/Temp quando o agente roda como SYSTEM).
	tmpBase, err := platform.EnsureTempDir()
	if err != nil {
		m.logf("[automation][p2p] preload: falha ao preparar diretorio temporario packageId=%s: %v", packageID, err)
		return
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "p2p-winget-preload-*")
	if err != nil {
		m.logf("[automation][p2p] preload: falha ao criar diretorio temporario packageId=%s: %v", packageID, err)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			m.logf("[automation][p2p] preload: aviso: falha ao limpar diretorio temporario %s: %v", tmpDir, removeErr)
		}
	}()

	m.logf("[automation][p2p] preload: baixando via winget download packageId=%s", packageID)
	if _, err := wingetClient.Download(ctx, packageID, tmpDir); err != nil {
		m.logf("[automation][p2p] preload: winget download falhou packageId=%s: %v", packageID, err)
		return
	}
	installerPath, err := findInstallerInDir(tmpDir)
	if err != nil {
		m.logf("[automation][p2p] preload: instalador nao encontrado packageId=%s: %v", packageID, err)
		return
	}

	installerVersion := automation.VersionFromInstallerFilename(filepath.Base(installerPath))
	var published p2pmeta.ArtifactView
	var pubErr error
	if installerVersion != "" {
		published, pubErr = m.app.P2PCoord.PublishFileWithIDAndVersion(installerPath, artifactID, installerVersion)
	} else {
		published, pubErr = m.app.P2PCoord.PublishFileWithID(installerPath, artifactID)
	}
	if pubErr != nil {
		m.logf("[automation][p2p] preload: falha ao publicar packageId=%s: %v", packageID, pubErr)
		return
	}
	m.logf("[automation][p2p] preload: artifact publicado no cache P2P packageId=%s artifactID=%s artifactName=%s", packageID, artifactID, published.ArtifactName)
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
		catSilent, catSilentWithProgress, catInstallerType := m.catalogInstallerInfo(packageID)
		output, err := runLocalInstallerFull(ctx, existing, catSilent, catSilentWithProgress, catInstallerType)
		if err != nil {
			// Erro tipado: artifact em disco, execução falhou. O caller
			// (Install/Upgrade) vai direto ao winget install sem re-baixar.
			return "", fmt.Errorf("%w: %v", errInstallerExec, err)
		}
		m.pruneArtifactAfterInstall(filepath.Base(existing))
		return output, nil
	}

	wingetClient := m.fallback.Winget()
	if wingetClient == nil {
		return "", fmt.Errorf("winget client indisponivel")
	}

	// 1. Criar diretório temporário dentro de %WINDIR%/Temp/Discovery
	// (convenção do agente — nunca a temp genérica do processo).
	tmpBase, err := platform.EnsureTempDir()
	if err != nil {
		return "", fmt.Errorf("falha ao preparar diretorio temporario: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "p2p-winget-download-*")
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
		published, pubErr = m.app.P2PCoord.PublishFileWithIDAndVersion(installerPath, artifactID, installerVersion)
	} else {
		published, pubErr = m.app.P2PCoord.PublishFileWithID(installerPath, artifactID)
	}
	if pubErr != nil {
		m.logf("[automation][p2p] aviso: falha ao publicar artifact no cache P2P: %v (instalacao continua)", pubErr)
		// Fallback: instalar direto do tmpDir se a publicação falhar.
		// Erro tipado: instalador em disco, execução falhou → caller vai
		// direto ao winget install sem re-baixar.
		catSilent, catSilentWithProgress, catInstallerType := m.catalogInstallerInfo(packageID)
		output, err := runLocalInstallerFull(ctx, installerPath, catSilent, catSilentWithProgress, catInstallerType)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errInstallerExec, err)
		}
		return output, nil
	}
	m.logf("[automation][p2p] artifact publicado no cache P2P artifactID=%s artifactName=%s", artifactID, published.ArtifactName)

	// 5. Instalar a partir da cópia persistente no P2P_Temp (não do tmpDir efêmero)
	p2pInstallerPath := filepath.Join(m.app.p2pTempDir(), published.ArtifactName)
	catSilent, catSilentWithProgress, catInstallerType := m.catalogInstallerInfo(packageID)
	output, err := runLocalInstallerFull(ctx, p2pInstallerPath, catSilent, catSilentWithProgress, catInstallerType)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errInstallerExec, err)
	}
	m.pruneArtifactAfterInstall(published.ArtifactName)
	return output, nil
}

// verifyLocalInstallResult confirma que o instalador local (cache P2P) realmente
// instalou/atualizou o pacote. Instaladores stub/online (ex.: BraveSilentSetup,
// instalação por usuário) saem com exit 0 — ou ficam travados como SYSTEM — sem
// instalar machine-scope. Nesses casos o router cai para o winget install, que
// conhece o escopo correto (machine) e roda elevado (o agent é SYSTEM; sem
// elevação, o UAC é acionado pelo caminho já existente).
// Sem dados confiáveis do winget NÃO se assume sucesso: reconsulta uma vez e,
// persistindo a indisponibilidade, retorna false para o caller cair no
// winget install (idempotente quando o pacote já está instalado). Assumir
// sucesso aqui mascarava instalações que nunca aconteceram.
func (m *automationPackageManagerRouter) verifyLocalInstallResult(ctx context.Context, packageID, operation string) bool {
	installedOut, installedErr := m.fallback.ListInstalled(ctx)
	upgradableOut, upgradableErr := m.fallback.ListUpgradable(ctx)
	if evidenceUnavailable(installedOut, installedErr, upgradableOut, upgradableErr, operation) {
		// O winget falha intermitentemente (0x8a150001) sob concorrência; uma
		// única falha não deve decidir se a instalação funcionou.
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return false
		}
		installedOut, installedErr = m.fallback.ListInstalled(ctx)
		upgradableOut, upgradableErr = m.fallback.ListUpgradable(ctx)
	}
	return localInstallVerified(packageID, installedOut, installedErr, upgradableOut, upgradableErr, operation)
}

// evidenceUnavailable informa se falta evidência confiável do winget para
// verificar a operação pedida.
func evidenceUnavailable(installedOut string, installedErr error, upgradableOut string, upgradableErr error, operation string) bool {
	if installedErr != nil || strings.TrimSpace(installedOut) == "" {
		return true
	}
	// Só ERRO indica indisponibilidade no upgrade: `winget upgrade` sem
	// pendências legitimamente não lista nada, e tratar isso como
	// indisponibilidade forçava um subprocesso winget extra em toda execução
	// sem atualizações.
	if strings.TrimSpace(operation) == "upgrade" && upgradableErr != nil {
		return true
	}
	return false
}

// localInstallVerified é a decisão pura da verificação pós-instalação (testável
// sem App/P2P): confirma que o instalador local realmente instalou/atualizou.
func localInstallVerified(packageID, installedOut string, installedErr error, upgradableOut string, upgradableErr error, operation string) bool {
	// Sem evidência confiável do winget NÃO se assume sucesso: o caller cai
	// para o winget install (escopo machine, idempotente), que é a fonte
	// autoritativa. Assumir sucesso aqui mascarava instalações que nunca
	// aconteceram (stub sai 0 sem instalar + winget list indisponível).
	if installedErr != nil || strings.TrimSpace(installedOut) == "" {
		return false
	}
	switch strings.TrimSpace(operation) {
	case "install":
		return automation.IsPackageInOutput(installedOut, packageID)
	case "upgrade":
		if upgradableErr != nil {
			return false
		}
		// Lista VAZIA com exit 0 é evidência válida de "nada a atualizar"
		// (comportamento histórico). Ainda listado como atualizável → o upgrade
		// não teve efeito.
		return !automation.IsPackageInOutput(upgradableOut, packageID)
	default:
		return false
	}
}

// catalogSilentSwitches busca os switches silenciosos do pacote no cache da
// loja de aplicativos (silent → silentWithProgress). Retorna strings vazias
// quando o pacote não está no catálogo ou não tem switches — nesse caso o
// instalador cai na cascata heurística (comportamento anterior).
// Nunca falha: erro de cache/loja é tratado como "sem switches".
func (m *automationPackageManagerRouter) catalogSilentSwitches(packageID string) (string, string) {
	silent, silentWithProgress, _ := m.catalogInstallerInfo(packageID)
	return silent, silentWithProgress
}

// catalogInstallerInfo estende catalogSilentSwitches retornando também o
// InstallerType do manifesto winget para a arquitetura preferida (x64 → x86 →
// arm64 → arm → neutral). InstallerType permite decidir a estratégia de
// execução (msiexec vs exe vs portable) sem adivinhar pela extensão.
// Retorna "" quando o pacote não está no catálogo ou o manifesto não tem tipo.
func (m *automationPackageManagerRouter) catalogInstallerInfo(packageID string) (silent, silentWithProgress, installerType string) {
	if m.app == nil || m.app.AppStoreSvc == nil {
		return "", "", ""
	}
	item, err := m.app.AppStoreSvc.ResolveAllowedPackage(m.app.ctx, packageID)
	if err != nil {
		// Pacote fora da política/loja: sem switches, usa cascata heurística.
		return "", "", ""
	}
	silent = strings.TrimSpace(item.SilentCommand)
	silentWithProgress = strings.TrimSpace(item.SilentWithProgress)
	if silent == "" {
		silent = silentWithProgress
	}
	installerType = resolveInstallerTypeForHost(item.InstallerTypesByArch)
	if silent != "" || installerType != "" {
		m.logf("[automation][p2p] dados do catálogo packageId=%s silent=%q silentWithProgress=%q installerType=%q", packageID, silent, silentWithProgress, installerType)
	}
	return silent, silentWithProgress, installerType
}

// resolveInstallerTypeForHost escolhe o InstallerType da arquitetura do host
// (mesma ordem de preferência do servidor: x64 → x86 → arm64 → arm → neutral).
func resolveInstallerTypeForHost(typesByArch map[string]string) string {
	if len(typesByArch) == 0 {
		return ""
	}
	archOrder := []string{"x64", "x86", "arm64", "arm", "neutral"}
	switch runtime.GOARCH {
	case "amd64":
		archOrder = []string{"x64", "x86", "arm64", "arm", "neutral"}
	case "386":
		archOrder = []string{"x86", "x64", "arm", "arm64", "neutral"}
	case "arm64":
		archOrder = []string{"arm64", "x64", "x86", "arm", "neutral"}
	case "arm":
		archOrder = []string{"arm", "arm64", "x86", "x64", "neutral"}
	}
	for _, arch := range archOrder {
		if t, ok := typesByArch[arch]; ok && strings.TrimSpace(t) != "" {
			return strings.ToLower(strings.TrimSpace(t))
		}
	}
	return ""
}

// pruneArtifactAfterInstall libera o instalador do P2P_Temp depois que a
// instalação concluiu com sucesso. Best-effort: nunca falha a instalação.
func (m *automationPackageManagerRouter) pruneArtifactAfterInstall(artifactName string) {
	if m == nil || m.app == nil || m.app.P2PCoord == nil {
		return
	}
	if m.app.P2PCoord.DeleteArtifactAfterInstall(artifactName) {
		m.logf("[automation][p2p] instalador removido do P2P_Temp apos instalacao artifact=%s", artifactName)
	}
}

// findLocalArtifactByID procura um artifact no cache P2P local pelo artifactID
// e retorna o caminho completo do arquivo se ele existir com manifest válido.
// Retorna "" se não encontrado ou inválido.
func (m *automationPackageManagerRouter) findLocalArtifactByID(artifactID string) string {
	if m.app == nil || m.app.P2PCoord == nil {
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

// findLocalArtifactByName é o equivalente de findLocalArtifactByID casando pelo
// nome do arquivo. Cobre artifacts cujo ID lógico local divergiu do anunciado
// (download P2P sem sidecar .meta), evitando re-download pela automação.
func (m *automationPackageManagerRouter) findLocalArtifactByName(artifactName string) string {
	if m == nil || m.app == nil || m.app.P2PCoord == nil {
		return ""
	}
	artifactName = strings.TrimSpace(artifactName)
	if artifactName == "" {
		return ""
	}
	artifacts, err := m.app.ListP2PArtifacts()
	if err != nil {
		return ""
	}
	for _, a := range artifacts {
		if !strings.EqualFold(strings.TrimSpace(a.ArtifactName), artifactName) {
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
// installerType (do manifesto winget: "wix", "burn", "msi", "nullsoft", "inno",
// "zip", "portable", ...) tem prioridade sobre a extensão do arquivo — um
// instalador "wix" empacotado como .exe precisa de msiexec, e "portable"/"zip"
// não são instaladores executáveis.
func runLocalInstallerWithSwitches(ctx context.Context, artifactPath, silent, silentWithProgress string) (string, error) {
	return runLocalInstallerFull(ctx, artifactPath, silent, silentWithProgress, "")
}

const (
	// localInstallerExeBudget é o orçamento total das tentativas de execução
	// direta de um .exe (catálogo + cascata heurística). Precisa caber: 1
	// tentativa completa do catálogo (5 min) + uma fatia da cascata, antes de
	// entregar ao `winget install`. Medido em campo: o stub do Brave não
	// retorna e consumia 5 min por conjunto de flags.
	localInstallerExeBudget = 6 * time.Minute
	// localInstallerMSIBudget cobre o msiexec (tentativa única de 10 min +
	// retry de UAC).
	localInstallerMSIBudget = 12 * time.Minute
)

func runLocalInstallerFull(ctx context.Context, artifactPath, silent, silentWithProgress, installerType string) (string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", fmt.Errorf("artifact path vazio")
	}
	ext := strings.ToLower(filepath.Ext(artifactPath))
	it := strings.ToLower(strings.TrimSpace(installerType))

	// InstallerType do manifesto tem prioridade sobre a extensão: decide a
	// família de instalador de forma confiável (ex.: 7-Zip x64 é "wix" — o
	// winget download entrega o .msi, mas um burn/baixado como .exe também
	// precisa de msiexec).
	isMSIFamily := ext == ".msi" ||
		it == "wix" || it == "msi" || it == "burn"
	isPortable := it == "zip" || it == "portable"

	// Prazo curto: instaladores stub/online (ex.: BraveSilentSetup, que instala
	// por usuário) FICAM TRAVADOS como SYSTEM sem instalar. Com prazo curto o
	// processo é morto (CommandContext) e o router cai para o winget install.
	// Falha de verificação pós-instalação também cai para o winget.
	timeout := 10 * time.Minute
	if !isMSIFamily {
		timeout = 5 * time.Minute
	}

	// Orçamento TOTAL das tentativas de execução direta. Cada conjunto de flags
	// consome o timeout inteiro quando o instalador trava (caso Brave medido: o
	// stub sobe "BraveUpdate" e não retorna; >8 min por tentativa, ~30 min até o
	// fallback). Sem teto global o agente fica dezenas de minutos sem instalar
	// nada antes de chegar ao `winget install`, que é o caminho autoritativo.
	// Orçamento esgotado → erro → o router cai para o winget.
	budget := localInstallerExeBudget
	if isMSIFamily {
		budget = localInstallerMSIBudget
	}
	ctx, cancelBudget := context.WithTimeout(ctx, budget)
	defer cancelBudget()

	switch {
	case isPortable:
		// Portable/zip não é instalável via execução direta — winget install
		// cuida da extração/portable install. Não desperdiça tentativa.
		return "", fmt.Errorf("instalador portable/zip não suportado para execução direta P2P (installerType=%q): use winget install", it)
	case isMSIFamily:
		// C6: switches do catálogo são para o instalador .exe original (ex.: /S
		// do NSIS) e NÃO são argumentos válidos do msiexec — aplicá-los a um
		// .msi causa "exit status 1" imediato. MSI sempre usa as flags nativas
		// do msiexec; propriedades extras do catálogo só entram se forem pares
		// KEY=VALUE (formato msiexec).
		args := []string{"/i", artifactPath, "/qn", "/norestart"}
		for _, sw := range splitInstallerSwitches(firstNonEmpty(silent, silentWithProgress)) {
			if isMSIExecArgument(sw) {
				args = append(args, sw)
			}
		}
		return executeHiddenProcess(ctx, timeout, "msiexec", args)
	case ext == ".exe":
		return runExeInstallerSilent(ctx, timeout, artifactPath, silent, silentWithProgress)
	default:
		return "", fmt.Errorf("formato de instalador não suportado para P2P: %s (installerType=%q)", ext, it)
	}
}

// isMSIExecArgument reporta se um switch do catálogo é um argumento válido do
// msiexec: opções nativas (/qn, /norestart, /l*v, etc.) ou propriedades
// KEY=VALUE. Switches de instaladores .exe (ex.: /S, /SILENT, /VERYSILENT)
// são rejeitados — não significam nada para msiexec e causam falha imediata.
func isMSIExecArgument(sw string) bool {
	sw = strings.TrimSpace(sw)
	if sw == "" {
		return false
	}
	lower := strings.ToLower(sw)
	if strings.HasPrefix(lower, "/") {
		// Opções nativas conhecidas do msiexec.
		native := []string{"/i", "/a", "/x", "/qn", "/qb", "/qr", "/qf", "/qn+", "/qb+", "/norestart", "/forcerestart", "/l", "/le", "/lw", "/li", "/lu", "/m", "/p", "/j", "/y", "/z", "/update"}
		for _, n := range native {
			if lower == n || strings.HasPrefix(lower, n+" ") || strings.HasPrefix(lower, n+"+") {
				return true
			}
		}
		return false
	}
	// Propriedades públicas no formato KEY=VALUE (ex.: INSTALLDIR="C:\x").
	return strings.Contains(sw, "=")
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

	// Conjuntos de flags já tentados (chave = args unidos). Sem esta dedup, quando
	// o catálogo traz silent == silentWithProgress (ex.: Brave: "/silent /install")
	// o MESMO comando roda 2x por estágio, e a cascata heurística repete sets já
	// tentados — multiplicando o tempo total de instalação.
	tried := make(map[string]bool)
	attempt := func(args []string) (string, bool) {
		key := strings.Join(args, "\x00")
		if tried[key] {
			return "", false
		}
		tried[key] = true
		output, err := executeHiddenProcess(ctx, timeout, artifactPath, args)
		if err == nil {
			return output, true
		}
		lastOutput, lastErr = output, err
		return "", false
	}

	// NOTA (evidência de campo 2026-10-10): existiu aqui uma tentativa prévia com
	// `--system-level` (escopo de máquina). Ela foi REMOVIDA: o manifesto do winget
	// do Brave (Brave.Brave 155.1.97.56) é `Scope: user`, NÃO declara
	// InstallerSwitches (o binário baixado é o `BraveBrowserStandaloneSilentSetup`,
	// já silencioso) e lista `invalidParameter` como retorno ESPERADO. Recebendo
	// `/silent /install --system-level`, esse stub sobe o filho "BraveUpdate" e
	// NÃO RETORNA quando executado como SYSTEM — nenhuma tentativa com escopo
	// inventado pelo agente pode instalar o que o próprio manifesto não suporta.
	// Em vez de adivinhar escopo, o orçamento de tempo (runLocalInstallerFull)
	// garante que o fallback `winget install` — autoritativo, que aplica os
	// switches do manifesto — seja alcançado em minutos, não em dezenas de minutos.
	for _, candidate := range []struct {
		name string
		args string
	}{{"silent", silent}, {"silentWithProgress", silentWithProgress}} {
		sw := strings.TrimSpace(candidate.args)
		if sw == "" {
			continue
		}
		if output, ok := attempt(splitInstallerSwitches(sw)); ok {
			return fmt.Sprintf("[p2p-installer] switches do catalogo (%s) bem-sucedidos: %s\n%s", candidate.name, sw, output), nil
		}
	}

	// Fallback: cascata heurística por framework detectado (comportamento anterior).
	kind := detectInstallerType(artifactPath)
	cascade := installerFlagCascade(kind)

	for i, args := range cascade {
		output, ok := attempt(args)
		if !ok {
			continue
		}
		if i > 0 {
			// Loga qual conjunto de flags funcionou (facilita diagnóstico).
			return fmt.Sprintf("[p2p-installer] flags heurísticas bem-sucedidas: %s\n%s", strings.Join(args, " "), output), nil
		}
		return output, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum conjunto de flags aplicavel")
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
	// Captura a saída manualmente (em vez de CombinedOutput) para poder atribuir o
	// processo a um Job Object antes do Wait: o exec.CommandContext mata apenas o
	// processo principal no timeout; instaladores stub deixam filhos vivos
	// (ex.: BraveUpdate), que só o Job encerra.
	var outputBuf bytes.Buffer
	cmd.Stdout = &outputBuf
	cmd.Stderr = &outputBuf
	if startErr := cmd.Start(); startErr != nil {
		// Falha de CreateProcess (ex.: ERROR_ELEVATION_REQUIRED 740 / binário sem
		// permissão): mantém o fallback de UAC do caminho antigo — sem isso o
		// instalador que exige admin deixava de acionar o prompt e falhava seco.
		if isElevationRequiredExecError(startErr) {
			if elevOut, elevErr := launchInstallerViaUAC(executable, args); elevErr == nil {
				return elevOut, nil
			}
		}
		return "", startErr
	}
	job, jobErr := processutil.NewJobObject()
	if jobErr == nil {
		if assignErr := job.Assign(cmd.Process); assignErr != nil {
			job.Close()
			job = nil
		}
	} else {
		job = nil
	}
	if job != nil {
		// Encerra a ÁRVORE assim que o contexto terminar. O exec.CommandContext
		// mata apenas o processo DIRETO; netos (ex.: BraveUpdate, criado pelo stub
		// do Brave) continuam vivos segurando os pipes de stdout/stderr, e
		// cmd.Wait() fica bloqueado até eles saírem — ou seja, o timeout e o
		// orçamento de execução NÃO eram respeitados (medido: comando de 30s com
		// deadline de 2s só retornou depois de 29s). Terminate concorrente fecha a
		// árvore e libera o Wait.
		stopWatch := make(chan struct{})
		var watcher sync.WaitGroup
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			select {
			case <-ctx.Done():
				_ = job.Terminate()
			case <-stopWatch:
			}
		}()
		// Garante que o watcher terminou antes de fechar o handle (evita corrida
		// entre Terminate e Close, que zera j.handle).
		defer func() {
			close(stopWatch)
			watcher.Wait()
			job.Close()
		}()
	}

	err := cmd.Wait()
	text := strings.TrimSpace(outputBuf.String())
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
	// Falha/timeout: encerra a ÁRVORE (processo principal + filhos). Sem isso o
	// stub travado deixava BraveUpdate e afins rodando depois do prazo.
	if job != nil {
		if termErr := job.Terminate(); termErr == nil {
			text = strings.TrimSpace(text + " [arvore do instalador encerrada]")
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timeout na execução do instalador")
	}
	// ── Fallback de elevação (Windows) ──
	// Caso 1: fork/exec falhou porque o binário exige elevação (erro 740).
	// Caso 2 (C8): msiexec iniciou mas falhou com código de "requer elevação"
	// (1730 ERROR_INSTALL_SOURCE_ABSENT? não — 1730 é outra coisa; usamos os
	// códigos MSI reais: 1603 com "requer elevação" no log, 1925/"you must be
	// admin", e principalmente o padrão observado: msiexec de Machine-scope sem
	// admin sai com 1603/1730 sem mensagem útil). O caso mais confiável é o
	// exit code 1603 combinado com agente não-elevado — tentamos UAC direto.
	if isElevationRequiredExecError(err) || isMSIElevationExitCode(executable, err) {
		if elevOut, elevErr := launchInstallerViaUAC(executable, args); elevErr == nil {
			return elevOut, nil
		}
		// UAC negado/indisponível: retorna o erro original para o router
		// seguir o fluxo de fallback (winget direto).
	}
	return text, err
}

// isMSIElevationExitCode detecta o padrão C8: msiexec executado por um agente
// não-elevado em instalação Machine-scope. O msiexec inicia normalmente (sem
// erro 740 de fork/exec), mas falha com exit codes de MSI que indicam falta de
// privilégio: 1603 (fatal, tipicamente sem admin), 1730 (ERROR_INSTALL_REMOTE
// _DISALLOWED? não — 1730 é "installation forbidden by policy"... na prática o
// par observado em campo é 1603/1619/1620). Sendo conservador: só tentamos UAC
// quando o executável é msiexec e o exit code é 1603 — o caso clássico de
// "instalação Machine-scope sem admin".
func isMSIElevationExitCode(executable string, err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(filepath.Base(executable)), "msiexec.exe") &&
		!strings.EqualFold(strings.TrimSpace(executable), "msiexec") {
		return false
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	// 1603 = ERROR_INSTALL_FAILURE (fatal durante a instalação). Sem admin,
	// instalações Machine-scope falham exatamente aqui.
	return exitErr.ExitCode() == 1603
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
	if m == nil || m.app == nil || m.app.P2PCoord == nil {
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
