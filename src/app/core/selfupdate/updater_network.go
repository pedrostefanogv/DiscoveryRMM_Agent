package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"discovery/app/core/buildinfo"
	"discovery/app/core/errutil"
	"discovery/app/netutil"
)

func (u *Updater) ResumePendingInstallReport(ctx context.Context) {
	// Reseta o flag de instalação em andamento. Este flag previne execuções
	// concorrentes de CheckAndUpdate durante o mesmo ciclo de vida do processo.
	// No startup, o flag sempre começa false (zero value de atomic.Bool),
	// mas resetamos explicitamente para segurança em edge cases onde o NSIS
	// não conseguiu matar o processo.
	u.installing.Store(false)

	// Limpa downloads antigos na pasta de updates no startup.
	// Arquivos discovery-update-*.exe com mais de 6h são removidos,
	// exceto o arquivo referenciado no pending state ativo.
	u.cleanupOldDownloads()

	state, err := u.loadPendingInstallState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Sem pending em disco: garante que a versão alvo exposta ao
			// frontend também fique limpa (evita botão "Atualizar agora"
			// fantasma vindo de estado stale do processo anterior).
			u.pendingTargetVersion.Store("")
			return
		}
		u.logf("falha ao carregar estado pendente de install: %v", err)
		return
	}
	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	currentCommit := strings.TrimSpace(buildinfo.Commit)

	// M-fix loop: versão/commit EFETIVOS — com buildinfo "0.0.0"/"unknown" (build
	// sem -X buildinfo), usa o marker do último install confirmado. Sem isso o
	// estado pendente nunca resolvia (0.0.0 < 1.2.1 para sempre) e o update
	// reinstalava em loop.
	effVersion, effCommit := u.effectiveCurrent(currentVersion, currentCommit)

	// ── Correlação com installer.log do NSIS ──
	// Antes de decidir se o update falhou, verificamos o log do instalador
	// para confirmar se ele rodou e qual foi o resultado.
	if state.RecordedAtUTC != "" {
		foundSuccess, foundError := u.correlateInstallerLog(state.RecordedAtUTC)
		if foundSuccess {
			u.logf("estado pendente de install resolvido: installer.log confirma sucesso (recordedAt=%s)", state.RecordedAtUTC)
			// M-fix loop: grava o marker do install CONFIRMADO (versão/commit que
			// estão deployados) — é o que quebra o loop de reinstalação.
			u.saveInstalledMarker(state.TargetVersion, state.InstalledCommit)
			u.clearPendingInstallState()
			return
		}
		if foundError {
			u.logf("estado pendente de install: installer.log contem erros (recordedAt=%s)", state.RecordedAtUTC)
		}
	}

	// ── Early exit: target mais antigo que versão atual ──
	// Se o targetVersion registrado é inferior à versão atual, o estado pendente
	// foi criado por um check defeituoso (ex.: extractFileVersion do PE divergente
	// do buildinfo.Version real). Neste caso, limpa imediatamente sem reportar
	// falha — o agente já está em versão superior.
	if compareVersions(state.TargetVersion, effVersion) < 0 {
		u.logf("estado pendente de install descartado: target=%s inferior a eff=%s (estado invalido)",
			state.TargetVersion, effVersion)
		u.saveInstalledMarker(effVersion, effCommit)
		u.clearPendingInstallState()
		return
	}

	// ── Decisao baseada em commit (preferencial) ──
	// Se o estado pendente tem installedCommit, comparamos commits ao inves
	// de versoes. Isso funciona mesmo quando a versao nao muda (rebuilds em dev).
	if state.InstalledCommit != "" && effCommit != "" && !strings.EqualFold(effCommit, "unknown") {
		if !strings.EqualFold(effCommit, state.InstalledCommit) {
			u.logf("estado pendente de install resolvido: commit mudou de %s para %s (instalacao OK)",
				state.InstalledCommit, effCommit)
			u.saveInstalledMarker(state.TargetVersion, effCommit)
			u.clearPendingInstallState()
			return
		}
		// Mesmo commit → binario nao mudou → falhou
		u.logf("estado pendente de install: commit nao mudou (%s) — instalacao falhou? tentativas=%d/%d",
			effCommit, state.InstallAttempts, maxInstallAttempts)
		if state.InstallAttempts >= maxInstallAttempts {
			u.logf("estado pendente de install: maximo de %d tentativas excedido (commit=%s target=%s). Possivel loop de instalacao.",
				maxInstallAttempts, currentCommit, state.TargetVersion)
			u.clearPendingInstallState()
			u.reportInstallFailed(ctx, state, "install-loop: commit nao mudou apos "+strconv.Itoa(state.InstallAttempts)+" tentativas")
			return
		}
		// Mantem pending — o Run loop vai tentar novamente no proximo ciclo
		u.logf("estado pendente de install mantido: commit ainda %s target=%s (tentativa %d/%d)",
			currentCommit, state.TargetVersion, state.InstallAttempts, maxInstallAttempts)
		return
	}

	// ── Fallback: decisao baseada em versao (commit ausente ou unknown) ──
	if state.InstallAttempts >= maxInstallAttempts {
		u.logf("estado pendente de install: maximo de %d tentativas excedido — buildinfo.Version=%s target=%s",
			maxInstallAttempts, currentVersion, state.TargetVersion)
		u.clearPendingInstallState()
		u.reportInstallFailed(ctx, state, "install-loop: excedeu maximo de tentativas (fallback versao)")
		return
	}

	if compareVersions(effVersion, state.TargetVersion) < 0 {
		u.logf("estado pendente de install mantido: versao efetiva=%s target=%s tentativas=%d/%d",
			effVersion, state.TargetVersion, state.InstallAttempts, maxInstallAttempts)
		return
	}
	// M-fix loop: resolvido pela versão — grava o marker (commit pode ser unknown).
	u.logf("estado pendente de install resolvido: versao efetiva=%s >= target=%s (limpo)", effVersion, state.TargetVersion)
	u.saveInstalledMarker(state.TargetVersion, state.InstalledCommit)
	u.clearPendingInstallState()
}

// AgentVersionInfo representa a resposta do endpoint /api/v1/download/agent/version.
type AgentVersionInfo struct {
	Version    string `json:"version"`
	CommitHash string `json:"commitHash"`
	Sha256     string `json:"sha256"`
}

// fetchAgentVersion consulta o endpoint /api/v1/download/agent/version
// para obter (version, commitHash, sha256) do build atual no servidor.
// Retorna erro se o endpoint nao existir (404) — o caller faz fallback.
func (u *Updater) fetchAgentVersion(ctx context.Context) (AgentVersionInfo, error) {
	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent/version"
	ctxReq, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return AgentVersionInfo{}, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return AgentVersionInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024))
		return AgentVersionInfo{}, fmt.Errorf("version endpoint status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info AgentVersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return AgentVersionInfo{}, fmt.Errorf("version endpoint decode: %w", err)
	}

	return info, nil
}

// fetchPublicSHA256 obtem o SHA256 do build atual sem baixar o binario.
// Usa o endpoint /api/v1/download/agent/sha256 que retorna text/plain.
func (u *Updater) fetchPublicSHA256(ctx context.Context) (string, error) {
	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent/sha256"
	ctxReq, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024))
		return "", fmt.Errorf("sha256 endpoint status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	shaBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(shaBytes))
	if len(sha) != 64 {
		return "", fmt.Errorf("sha256 invalido: tamanho=%d esperado=64", len(sha))
	}
	return strings.ToLower(sha), nil
}

// downloadFromCacheOrPublic tenta baixar o artifact via P2P (se peers disponiveis)
// e faz fallback para o endpoint publico /api/v1/download/agent.
//
// C1/C2 (integridade do self-update): o SHA256 publicado pelo servidor é
// OBRIGATÓRIO. Sem hash de referência nenhum download é iniciado:
//   - P2P é recusado (não há como validar o que um peer entrega);
//   - HTTP é recusado (não há como validar o que o servidor entrega).
// Nos dois caminhos o hash do arquivo é verificado após o download; divergência
// ou ausência abortam com remoção do arquivo temporário, sem instalar.
//
// Retorna o path do arquivo temporario, SHA256 calculado localmente e flag
// indicando se o download veio do P2P (true) ou HTTP (false).
func (u *Updater) downloadFromCacheOrPublic(ctx context.Context, expectedSHA256 string) (path string, sha string, fromP2P bool, err error) {
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))

	// ── Hash de referência obrigatório (C1/C2) ──
	if expectedSHA256 == "" {
		if fetched, fetchErr := u.fetchPublicSHA256(ctx); fetchErr == nil && fetched != "" {
			expectedSHA256 = fetched
			u.logf("[selfupdate] SHA256 obtido do servidor: %s (nao baixou binario)", shortHash(expectedSHA256))
		} else {
			u.logf("[selfupdate] SHA256 do servidor indisponivel: %v — recusando download sem hash de referencia", fetchErr)
			return "", "", false, fmt.Errorf("sha256 do servidor indisponivel (%v) — instalacao recusada sem hash de referencia", fetchErr)
		}
	}
	artifactID := "selfupdate:" + expectedSHA256

	// ── P2P-first ──
	// Tenta o artifactID canônico ("selfupdate:<sha256>", registrado via sidecar
	// .meta) e, como fallback, o ID derivado do nome do arquivo
	// ("name:selfupdate-<sha256>.exe") para compatibilidade com peers que
	// baixaram o update antes do registro de sidecar existir.
	if u.FindPeersByReleaseID != nil && u.DownloadFromPeer != nil {
		artifactIDs := []string{artifactID}
		legacyID := "name:selfupdate-" + expectedSHA256 + ".exe"
		if !strings.EqualFold(legacyID, artifactID) {
			artifactIDs = append(artifactIDs, legacyID)
		}
		for _, tryID := range artifactIDs {
			peers, findErr := u.FindPeersByReleaseID(ctx, tryID)
			if findErr != nil {
				u.logf("[selfupdate] consulta P2P falhou (nao-critico): %v", findErr)
			}
			if len(peers) == 0 {
				u.logf("[selfupdate] P2P: nenhum peer com artifactID=%s", tryID)
				continue
			}
			u.logf("[selfupdate] P2P: %d peer(s) encontrados para artifactID=%s", len(peers), tryID)
			for _, peerID := range peers {
				path, dlErr := u.DownloadFromPeer(ctx, tryID, peerID)
				if dlErr != nil {
					u.logf("[selfupdate] P2P download do peer %s falhou: %v", peerID, dlErr)
					continue
				}
				actual, shaErr := fileSHA256(path)
				if shaErr != nil {
					u.logf("[selfupdate] P2P sha256 do peer %s falhou: %v", peerID, shaErr)
					_ = os.Remove(path)
					continue
				}
				// C2: verificação incondicional — o caso expected=="" já foi
				// bloqueado no início da função; aqui nunca se aceita arquivo
				// sem conferência integral do hash.
				if !strings.EqualFold(actual, expectedSHA256) {
					u.logf("[selfupdate] P2P sha256 mismatch do peer %s: esperado=%s obtido=%s (stale)", peerID, shortHash(expectedSHA256), shortHash(actual))
					_ = os.Remove(path)
					continue
				}
				u.logf("[selfupdate] download P2P concluido: peer=%s artifactID=%s sha256=%s", peerID, tryID, shortHash(actual))
				return path, actual, true, nil
			}
			u.logf("[selfupdate] P2P exaurido (%d peers tentados) para artifactID=%s", len(peers), tryID)
		}
		u.logf("[selfupdate] P2P: nenhum peer com o artifact, usando HTTP")
	}

	// ── HTTP download do endpoint publico ──
	downloadURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent"
	path, sha, err = u.downloadFromURL(ctx, downloadURL)
	if err != nil {
		return "", "", false, err
	}
	// C1: verificação obrigatória do hash do download HTTP contra o hash
	// publicado pelo servidor. Divergência/ausência aborta com remoção.
	if verr := u.verifyInstallerSHA256(expectedSHA256, sha); verr != nil {
		u.logf("[selfupdate] %v — removendo download e abortando", verr)
		_ = os.Remove(path)
		return "", "", false, verr
	}
	return path, sha, false, nil
}

// shortHash retorna os primeiros 12 caracteres do hash de forma segura
// (evita panic de slicing quando o valor é vazio/curto — A9).
func shortHash(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// verifyInstallerSHA256 compara o hash calculado do instalador com o hash
// esperado publicado pelo servidor (C1/C2). Ausência de qualquer um dos
// lados ou divergência são erros fatais — o instalador nunca é executado
// sem verificação de integridade.
func (u *Updater) verifyInstallerSHA256(expected, actual string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if expected == "" {
		return errors.New("sha256 esperado ausente — instalador recusado sem verificação de integridade")
	}
	if actual == "" {
		return errors.New("sha256 do arquivo ausente — instalador recusado sem verificação de integridade")
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("sha256 divergente do servidor: esperado=%s obtido=%s", shortHash(expected), shortHash(actual))
	}
	return nil
}

// downloadFromURL faz o download do instalador a partir de uma URL.
// Retorna o caminho do arquivo temporario e o SHA256 do conteudo.
func (u *Updater) downloadFromURL(ctx context.Context, downloadURL string) (string, string, error) {
	if err := os.MkdirAll(u.TempDir, 0o755); err != nil {
		return "", "", err
	}

	path := filepath.Join(u.TempDir, fmt.Sprintf("discovery-update-%s.exe", uuid.NewString()))
	f, err := os.Create(path)
	if err != nil {
		return "", "", err
	}

	ctxDownload, cancel := context.WithDeadline(ctx, time.Now().Add(downloadDeadline))
	defer cancel()

	req, err := http.NewRequestWithContext(ctxDownload, http.MethodGet, downloadURL, nil)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos erro de request")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download temp")
		return "", "", err
	}

	// Endpoint publico (AllowAnonymous) — envia token apenas para consistencia/logging.
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token != "" && agentID != "" {
		_ = netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID)
	}

	u.logf("[selfupdate] baixando de %s", downloadURL)

	client := &http.Client{Timeout: downloadDeadline}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha HTTP")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo status != 200")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download status != 200")
		return "", "", fmt.Errorf("download status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(f, resp.Body, buf); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de copy")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de copy")
		return "", "", err
	}
	if err := f.Sync(); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de sync")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de sync")
		return "", "", err
	}
	if err := f.Close(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de close")
		return "", "", err
	}

	sha, err := fileSHA256(path)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha sha256")
		return "", "", err
	}

	// Renomeia para o nome canônico P2P (selfupdate-<sha256>.exe).
	// Isso coloca o arquivo no P2P_Temp com o nome que o gossip scanner
	// reconhece e registra no índice, sem precisar de cópia extra.
	canonicalPath := filepath.Join(u.TempDir, fmt.Sprintf("selfupdate-%s.exe", strings.ToLower(sha)))
	// Se o arquivo canônico já existe com o mesmo conteúdo (ex.: download
	// concorrente via InvalidateCh), reusa-o e remove o duplicado UUID.
	if existing, statErr := os.Stat(canonicalPath); statErr == nil && existing.Size() > 0 {
		if existingSHA, shaErr := fileSHA256(canonicalPath); shaErr == nil && strings.EqualFold(existingSHA, sha) {
			u.logf("[selfupdate] canonical ja existe com mesmo hash, reusando: %s", canonicalPath)
			_ = os.Remove(path) // remove o download duplicado UUID
			return canonicalPath, sha, nil
		}
		// Hash diferente — força remoção do antigo (stale).
		if err := os.Remove(canonicalPath); err != nil {
			u.logf("[selfupdate] aviso: nao foi possivel remover canonical stale: %v", err)
		}
	}
	if err := os.Rename(path, canonicalPath); err != nil {
		// Fallback: se rename falhar (ex.: cross-device ou arquivo em uso),
		// mantém path original. O conteúdo é válido e já foi verificado.
		u.logf("[selfupdate] aviso: rename para canonical falhou: %v — mantendo path original", err)
	} else {
		path = canonicalPath
	}

	u.logf("[selfupdate] download concluido: path=%s sha256=%s", path, shortHash(sha))
	u.incDownloadOK()
	return path, sha, nil
}

// reportEvent is a no-op — o endpoint /api/v1/agent-auth/me/update/report nao existe na API.
// Mantido como stub para nao quebrar chamadas existentes no codigo.
func (u *Updater) reportEvent(_ context.Context, _ string, _ reportOpts) {}

// reportInstallFailed limpa o estado pendente e loga a falha.
// Usado quando o loop de instalacao excede o maximo de tentativas.
func (u *Updater) reportInstallFailed(_ context.Context, state pendingInstallState, reason string) {
	u.reportEvent(context.Background(), "InstallFailed", reportOpts{
		ReleaseID:      state.ReleaseID,
		CurrentVersion: state.CurrentVersion,
		TargetVersion:  state.TargetVersion,
		Message:        reason,
		CorrelationID:  state.CorrelationID,
	})
}
