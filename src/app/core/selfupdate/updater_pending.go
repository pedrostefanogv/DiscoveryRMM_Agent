package selfupdate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/errutil"
)

const (
	// cleanupOldDownloadsAge é a idade máxima de arquivos .exe baixados na
	// pasta de updates. Arquivos mais antigos são removidos no startup para
	// evitar acúmulo de instaladores (~30MB cada). Reduzido para 6h para
	// evitar acúmulo rápido durante loops de update.
	cleanupOldDownloadsAge = 6 * time.Hour

	// installerLogSuccessMarker é a string que o NSIS escreve no installer.log
	// quando a instalação é concluída com sucesso.
	installerLogSuccessMarker = "Instalacao concluida com sucesso"

	// installerLogErrorMarker é a string que o NSIS escreve no installer.log
	// em caso de erro.
	installerLogErrorMarker = "[ERROR]"

	// installerLogCorrelationWindow é a janela de tempo para buscar entradas
	// no installer.log após o recordedAt do pending state.
	installerLogCorrelationWindow = 10 * time.Minute
)

func (u *Updater) pendingInstallStatePath() string {
	if strings.TrimSpace(u.TempDir) == "" {
		return ""
	}
	return filepath.Join(u.TempDir, pendingInstallFile)
}

func (u *Updater) persistPendingInstallState(state pendingInstallState) error {
	path := u.pendingInstallStatePath()
	if path == "" {
		return errors.New("diretorio temporario de update nao configurado")
	}

	// Carrega estado existente para incrementar contador de tentativas.
	// Previne loop infinito quando buildinfo.Version nao reflete a versao real
	// (ex.: ldflags -X nao injetados no build, fica sempre "0.0.0").
	if existing, err := u.loadPendingInstallState(); err == nil {
		if existing.TargetVersion == state.TargetVersion && existing.CurrentVersion == state.CurrentVersion {
			state.InstallAttempts = existing.InstallAttempts + 1
			u.logf("estado pendente de install: tentativa %d/%d para target=%s current=%s",
				state.InstallAttempts, maxInstallAttempts, state.TargetVersion, state.CurrentVersion)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func (u *Updater) loadPendingInstallState() (pendingInstallState, error) {
	path := u.pendingInstallStatePath()
	if path == "" {
		return pendingInstallState{}, os.ErrNotExist
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return pendingInstallState{}, err
	}
	var state pendingInstallState
	if err := json.Unmarshal(body, &state); err != nil {
		return pendingInstallState{}, err
	}
	return state, nil
}

func (u *Updater) clearPendingInstallState() {
	path := u.pendingInstallStatePath()
	if path == "" {
		return
	}
	u.installing.Store(false) // libera trava de concorrência
	errutil.LogIfErr(os.Remove(path), "selfupdate: limpar estado de instalacao pendente")
}

// persistInstallerPID atualiza o pending state com o PID do instalador lançado.
// Best-effort: se não houver estado pendente ou falhar ao persistir, apenas loga.
func (u *Updater) persistInstallerPID(pid uint32) {
	if pid == 0 {
		return
	}
	state, err := u.loadPendingInstallState()
	if err != nil {
		return
	}
	if state.InstallerPID == pid {
		return
	}
	state.InstallerPID = pid
	if err := u.persistPendingInstallState(state); err != nil {
		u.logf("aviso: nao foi possivel persistir PID do instalador: %v", err)
	}
}

// hasPendingInstallRetry retorna true se existe um estado pendente de install
// com tentativas restantes (InstallAttempts < maxInstallAttempts). Nesse caso,
// o Run loop deve agendar um retry rápido (installRetryDelay) em vez de esperar
// o próximo ciclo periódico.
func (u *Updater) hasPendingInstallRetry() bool {
	state, err := u.loadPendingInstallState()
	if err != nil {
		return false
	}
	return state.InstallAttempts > 0 && state.InstallAttempts < maxInstallAttempts
}

// writeInstallerLogMarker escreve um marcador no installer.log do NSIS antes
// de lançar o instalador. Isso cria uma linha do tempo clara no log:
//   - "agente iniciou update às X" (aqui)
//   - "instalador iniciou às Y" (escrito pelo NSIS)
//   - "instalador concluiu às Z" (escrito pelo NSIS)
//
// O marcador usa o mesmo formato de timestamp do NSIS (hora local, sem
// zero-padding) para que o correlateInstallerLog consiga correlacionar.
// Best-effort: falhas ao escrever não bloqueiam o launch.
func (u *Updater) writeInstallerLogMarker(message string) {
	logPath := strings.TrimSpace(u.InstallerLogPath)
	if logPath == "" {
		return
	}
	now := time.Now()
	ts := fmt.Sprintf("%d-%d-%d %d:%d:%d", now.Year(), int(now.Month()), now.Day(), now.Hour(), now.Minute(), now.Second())
	line := ts + " [AGENT] " + message + "\r\n"

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		u.logf("aviso: nao foi possivel escrever marcador no installer.log: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		u.logf("aviso: nao foi possivel escrever marcador no installer.log: %v", err)
	}
}

// cleanupOldDownloads remove arquivos .exe baixados há mais de cleanupOldDownloadsAge.
// Evita acúmulo de instaladores (~30MB cada) na pasta de updates.
// É chamado no startup pelo ResumePendingInstallReport.
//
// IMPORTANTE: Não remove o arquivo referenciado no pending-install.json ativo,
// mesmo que ele seja antigo — isso garante que o retry funcione.
func (u *Updater) cleanupOldDownloads() {
	if strings.TrimSpace(u.TempDir) == "" {
		return
	}

	// Carrega pending state para proteger o arquivo ativo.
	var protectedPath string
	if state, err := u.loadPendingInstallState(); err == nil && state.InstallerPath != "" {
		protectedPath = filepath.Clean(state.InstallerPath)
	}

	entries, err := os.ReadDir(u.TempDir)
	if err != nil {
		if !os.IsNotExist(err) {
			u.logf("aviso: nao foi possivel ler pasta de updates para limpeza: %v", err)
		}
		return
	}

	cutoff := time.Now().Add(-cleanupOldDownloadsAge)
	removed := 0
	removedBytes := int64(0)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Limpa arquivos de download residuais (tanto legado discovery-update-*.exe
		// quanto o novo padrão canônico selfupdate-<sha256>.exe).
		// Não remove pending-install.json (gerenciado separadamente).
		isLegacy := strings.HasPrefix(name, "discovery-update-") && strings.HasSuffix(name, ".exe")
		isCanonical := strings.HasPrefix(name, "selfupdate-") && strings.HasSuffix(name, ".exe")
		if !isLegacy && !isCanonical {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Protege o arquivo referenciado no pending state ativo.
		fullPath := filepath.Join(u.TempDir, name)
		if protectedPath != "" && filepath.Clean(fullPath) == protectedPath {
			u.logf("cleanup: preservando instalador pendente: %s", name)
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(fullPath); err != nil {
				continue
			}
			removed++
			removedBytes += info.Size()
		}
	}

	if removed > 0 {
		u.logf("limpeza de updates antigos: %d arquivo(s) removido(s) (%.1f MB liberados)",
			removed, float64(removedBytes)/(1024*1024))
	}
}

// correlateInstallerLog busca no installer.log do NSIS por entradas posteriores
// ao recordedAt do pending state. Retorna true se encontrou o marcador de sucesso.
// Limita a 500 linhas recentes para evitar consumo excessivo de memória em logs grandes.
//
// O NSIS ${GetTime} "" "L" escreve timestamps em hora LOCAL sem zero-padding
// (ex.: "2026-8-5 9:3:2"), enquanto o recordedAt do pending state é UTC.
// Por isso:
//   - O recordedAt é convertido para hora local antes da comparação.
//   - O timestamp da linha é parseado com Sscanf tolerante a zero-padding.
func (u *Updater) correlateInstallerLog(recordedAtUTC string) (foundSuccess bool, foundError bool) {
	logPath := strings.TrimSpace(u.InstallerLogPath)
	if logPath == "" {
		return false, false
	}

	recordedAt, err := time.Parse(time.RFC3339, recordedAtUTC)
	if err != nil {
		return false, false
	}
	// NSIS loga em hora local; converte o recordedAt (UTC) para o mesmo fuso.
	recordedAtLocal := recordedAt.Local()
	windowEnd := recordedAtLocal.Add(installerLogCorrelationWindow)

	f, err := os.Open(logPath)
	if err != nil {
		return false, false
	}
	defer f.Close()

	// Lê o arquivo linha a linha, mantendo apenas as 500 linhas mais recentes
	// dentro da janela de correlação. O marcador de sucesso fica no FINAL do
	// log, então coletar as últimas linhas (não as primeiras) evita perdê-lo
	// quando o log tem muitas entradas na janela.
	const maxRecentLines = 500
	scanner := bufio.NewScanner(f)
	var recentLines []string
	for scanner.Scan() {
		line := scanner.Text()
		lineTime, ok := parseNSISLogTimestamp(line)
		if !ok {
			continue
		}
		if lineTime.After(recordedAtLocal) && lineTime.Before(windowEnd) {
			recentLines = append(recentLines, line)
			if len(recentLines) > maxRecentLines {
				// Descarta a mais antiga para manter apenas as recentes.
				recentLines = recentLines[1:]
			}
		}
	}

	for _, line := range recentLines {
		if strings.Contains(line, installerLogSuccessMarker) {
			foundSuccess = true
		}
		if strings.Contains(line, installerLogErrorMarker) {
			foundError = true
		}
	}

	return foundSuccess, foundError
}

// parseNSISLogTimestamp extrai e parseia o timestamp do início de uma linha do
// installer.log. O NSIS ${GetTime} "" "L" produz "YYYY-M-D H:M:S" sem
// zero-padding (ex.: "2026-8-5 9:3:2"). Retorna a hora local e true em caso
// de sucesso.
func parseNSISLogTimestamp(line string) (time.Time, bool) {
	var year, month, day, hour, minute, second int
	n, err := fmt.Sscanf(line, "%d-%d-%d %d:%d:%d", &year, &month, &day, &hour, &minute, &second)
	if err != nil || n != 6 {
		return time.Time{}, false
	}
	if month < 1 || month > 12 || day < 1 || day > 31 ||
		hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local), true
}
