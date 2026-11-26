package app

// Descomissionamento remoto: o painel move o agente para a lixeira e envia o
// comando "decommissionagent"; o serviço (SYSTEM) lança o uninstaller NSIS
// silencioso e o agente desaparece da máquina.
//
// Fluxo:
//  1. gate de elevação — só o núcleo elevado (serviço SYSTEM) pode executar;
//  2. idempotência — marcador local no SQLite + guarda em memória, porque o
//     servidor reentrega comandos não confirmados;
//  3. resolução do uninstaller via registro do Windows (QuietUninstallString);
//  4. lançamento após um pequeno atraso, para o resultado do comando sair no
//     NATS antes de o uninstaller parar o serviço (sc stop) e apagar os
//     binários.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"discovery/app/core/platform"
)

const (
	// agentDecommissionRequestCacheKey guarda o pedido em andamento no SQLite
	// local (mesmo banco usado pelo outbox do decommission).
	agentDecommissionRequestCacheKey = "agent_decommission_request"

	// uninstallerFileName é o nome do uninstaller NSIS gravado pelo instalador.
	uninstallerFileName = "uninstall.exe"

	// agentDecommissionRequestTTL é o TTL do marcador. Sobrevive a reinícios e
	// atualizações; é apagado junto com o %ProgramData% no uninstall.
	agentDecommissionRequestTTL = 30 * 24 * time.Hour
)

// agentDecommissionLaunchDelay dá tempo para o resultado do comando subir no
// NATS antes de o uninstaller derrubar o serviço. É variável para os testes
// zerarem o atraso.
var agentDecommissionLaunchDelay = 5 * time.Second

// agentDecommissionIsElevated é injetável nos testes.
var agentDecommissionIsElevated = platform.IsElevated

// agentDecommissionResolveUninstaller é injetável nos testes.
var agentDecommissionResolveUninstaller = resolveAgentUninstaller

// validateUninstallerPathFn é injetável nos testes (aponta para
// validateUninstallerPath por padrão).
var validateUninstallerPathFn = validateUninstallerPath

// verifyUninstallerACLFn / trustedUninstallerRootsFn / untrustedUninstallerRootsFn /
// verifyUninstallerAuthenticodeFn são injetáveis nos testes.
var (
	verifyUninstallerACLFn          = platform.VerifyTrustedExecutableACL
	trustedUninstallerRootsFn       = defaultTrustedUninstallerRoots
	untrustedUninstallerRootsFn     = defaultUntrustedUninstallerRoots
	verifyUninstallerAuthenticodeFn = platform.VerifyAuthenticode
)

// launchAgentUninstaller lança o uninstaller e devolve o PID (injetável nos testes).
var launchAgentUninstaller = launchAgentUninstallerDetached

// agentDecommissionLaunching evita corrida entre reentregas do comando no
// mesmo processo (o marcador persistente cobre reinícios).
var agentDecommissionLaunching atomic.Bool

// agentDecommissionRequest é o marcador persistente do pedido.
type agentDecommissionRequest struct {
	RequestedAtUTC  string `json:"requestedAtUtc"`
	UninstallerPath string `json:"uninstallerPath"`
	// Launched é apenas diagnóstico (última tentativa que conseguiu subir o
	// uninstaller). NÃO bloqueia novas tentativas: se este processo está vivo,
	// a máquina continua instalada.
	Launched bool `json:"launched"`
}

// isAgentDecommissionCommandType aceita o valor wire e aliases defensivos.
func isAgentDecommissionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "decommissionagent", "agentdecommission", "decommission", "uninstallagent":
		return true
	default:
		return false
	}
}

// handleAgentDecommissionCommand processa o comando de descomissionamento.
// Retorna (handled, exitCode, output, errText) no contrato do agentconn.
//
// Um pedido por processo: o CAS em agentDecommissionLaunching impede que duas
// entregas concorrentes (trash seguido de purge, por exemplo) lancem o
// uninstaller duas vezes.
func (a *App) handleAgentDecommissionCommand(_ context.Context, payload any) (bool, int, string, string) {
	if !agentDecommissionIsElevated() {
		return true, 1, "", "descomissionamento remoto requer núcleo elevado (serviço SYSTEM)"
	}

	if !agentDecommissionLaunching.CompareAndSwap(false, true) {
		a.Logs.Append("[decommission] pedido duplicado ignorado (desinstalação já em andamento)")
		return true, 0, "desinstalação já em andamento", ""
	}

	// Apaga o cofre TPM antes de resolver/lançar o uninstaller (pedido remoto).
	// nil-safe e idempotente; erro apenas é logado.
	if a.DebugSvc != nil {
		if err := a.DebugSvc.EraseTPMVault("remote-decommission"); err != nil {
			a.Logs.Append("[decommission] falha ao apagar cofre TPM: " + err.Error())
		}
	}

	requestedBy, reason := parseAgentDecommissionPayload(payload)

	uninstallerPath, err := agentDecommissionResolveUninstaller()
	if err != nil {
		// Sem uninstaller não há o que fazer agora: libera a trava para que uma
		// nova entrega (reentrega, purge ou novo delete no painel) tente de novo.
		agentDecommissionLaunching.Store(false)
		a.Logs.Append("[decommission] " + err.Error())
		return true, 1, "", err.Error()
	}

	// Marca ANTES de agendar: se o processo morrer no atraso, o startup retoma
	// a desinstalação (resumeAgentDecommissionIfPending).
	a.markAgentDecommissionRequested(uninstallerPath)

	a.Logs.Append(fmt.Sprintf(
		"[decommission] pedido recebido (requestedBy=%q reason=%q) uninstaller=%s",
		requestedBy, reason, uninstallerPath))

	a.safeGo(func() {
		if agentDecommissionLaunchDelay > 0 {
			time.Sleep(agentDecommissionLaunchDelay)
		}
		if err := a.runAgentUninstaller(uninstallerPath); err != nil {
			a.Logs.Append("[decommission] falha ao lançar uninstaller: " + err.Error())
			return
		}
		a.markAgentDecommissionLaunched()
	})

	return true, 0, "desinstalação iniciada", ""
}

// runAgentUninstaller lança o uninstaller em modo silencioso e registra o PID.
// Em falha, libera a trava (agentDecommissionLaunching) para permitir nova
// tentativa: um uninstaller que não subiu deixa a máquina instalada.
func (a *App) runAgentUninstaller(uninstallerPath string) error {
	validated, err := validateUninstallerPathFn(uninstallerPath)
	if err != nil {
		agentDecommissionLaunching.Store(false)
		return fmt.Errorf("caminho do uninstaller rejeitado: %w", err)
	}
	pid, err := launchAgentUninstaller(validated)
	if err != nil {
		agentDecommissionLaunching.Store(false)
		return err
	}
	a.Logs.Append(fmt.Sprintf("[decommission] uninstaller iniciado (pid=%d): %s", pid, validated))
	return nil
}

// resumeAgentDecommissionIfPending retoma um pedido gravado em disco que não
// chegou a remover a máquina. Se ESTE processo está vivo, a desinstalação não
// terminou — ou o serviço caiu durante o atraso de resposta ao comando, ou o
// uninstaller falhou depois de subir. Por isso o marcador persistente não é
// usado como bloqueio permanente: ele é um pedido em aberto até a máquina ser
// removida (o uninstall apaga o %ProgramData% que contém este marcador).
func (a *App) resumeAgentDecommissionIfPending() {
	if a == nil || a.CoreAgent.DB == nil {
		return
	}

	var req agentDecommissionRequest
	ok, err := a.CoreAgent.DB.CacheGetJSON(agentDecommissionRequestCacheKey, &req)
	if err != nil || !ok {
		return
	}
	if strings.TrimSpace(req.UninstallerPath) == "" {
		return
	}
	validated, vErr := validateUninstallerPathFn(req.UninstallerPath)
	if vErr != nil {
		a.Logs.Append(fmt.Sprintf("[decommission] ALERTA DE SEGURANCA: pedido pendente rejeitado (%s): %v — removendo marcador", req.UninstallerPath, vErr))
		_ = a.CoreAgent.DB.CacheDelete(agentDecommissionRequestCacheKey)
		return
	}
	if !agentDecommissionLaunching.CompareAndSwap(false, true) {
		return
	}

	if a.DebugSvc != nil {
		if err := a.DebugSvc.EraseTPMVault("remote-decommission"); err != nil {
			a.Logs.Append("[decommission] falha ao apagar cofre TPM: " + err.Error())
		}
	}

	a.Logs.Append("[decommission] retomando desinstalação pendente: " + validated)
	a.safeGo(func() {
		if err := a.runAgentUninstaller(validated); err != nil {
			a.Logs.Append("[decommission] falha ao retomar uninstaller: " + err.Error())
			return
		}
		a.markAgentDecommissionLaunched()
	})
}

// describeAgentDecommission devolve um resumo do pedido de descomissionamento
// para o log de diagnóstico do watchdog. String vazia = nada pendente.
//
// Objetivo: tornar diagnosticável o caso "o painel mandou desinstalar e a máquina
// continua instalada". O log do host passa a dizer se o pedido chegou, se o
// uninstaller foi resolvido e se o lançamento falhou — em vez de o operador ver
// apenas "comandos=0" sem saber se o comando chegou.
func (a *App) describeAgentDecommission() string {
	if a == nil || a.CoreAgent.DB == nil {
		return ""
	}

	var req agentDecommissionRequest
	ok, err := a.CoreAgent.DB.CacheGetJSON(agentDecommissionRequestCacheKey, &req)
	if err != nil || !ok {
		return ""
	}
	if strings.TrimSpace(req.UninstallerPath) == "" {
		return ""
	}

	state := "pedido pendente de desinstalacao"
	if req.Launched {
		// Launched=true e ESTE processo vivo significa que o uninstaller subiu mas
		// nao removeu a maquina: a desinstalacao falhou depois de iniciar.
		state = "uninstaller lancado mas a maquina continua instalada (desinstalacao nao concluiu)"
	}

	requestedAt := strings.TrimSpace(req.RequestedAtUTC)
	return fmt.Sprintf("%s desde %s; uninstaller=%s; elevacao=%t",
		state, requestedAt, req.UninstallerPath, agentDecommissionIsElevated())
}

func (a *App) markAgentDecommissionRequested(uninstallerPath string) {
	if a == nil || a.CoreAgent.DB == nil {
		return
	}

	entry := agentDecommissionRequest{
		RequestedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		UninstallerPath: uninstallerPath,
	}
	if err := a.CoreAgent.DB.CacheSetJSON(agentDecommissionRequestCacheKey, entry, agentDecommissionRequestTTL); err != nil {
		a.Logs.Append("[decommission] falha ao gravar marcador local: " + err.Error())
	}
}

func (a *App) markAgentDecommissionLaunched() {
	if a == nil || a.CoreAgent.DB == nil {
		return
	}

	var entry agentDecommissionRequest
	ok, err := a.CoreAgent.DB.CacheGetJSON(agentDecommissionRequestCacheKey, &entry)
	if err != nil || !ok {
		return
	}
	entry.Launched = true
	if err := a.CoreAgent.DB.CacheSetJSON(agentDecommissionRequestCacheKey, entry, agentDecommissionRequestTTL); err != nil {
		a.Logs.Append("[decommission] falha ao atualizar marcador local: " + err.Error())
	}
}

// parseAgentDecommissionPayload lê os campos de auditoria opcionais.
func parseAgentDecommissionPayload(payload any) (string, string) {
	var raw map[string]any
	switch typed := payload.(type) {
	case map[string]any:
		raw = typed
	case string:
		_ = json.Unmarshal([]byte(typed), &raw)
	}
	if raw == nil {
		return "", ""
	}

	str := func(key string) string {
		if value, ok := raw[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	return str("requestedBy"), str("reason")
}

// resolveAgentUninstaller localiza o uninstaller NSIS: registro do Windows
// (fonte autoritativa) e, em fallback, o diretório do executável em uso.
func resolveAgentUninstaller() (string, error) {
	if path := readUninstallerFromRegistry(); strings.TrimSpace(path) != "" {
		if validated, err := validateUninstallerPath(path); err == nil {
			return validated, nil
		}
	}

	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), uninstallerFileName)
		if validated, err := validateUninstallerPath(candidate); err == nil {
			return validated, nil
		}
	}

	return "", fmt.Errorf("uninstaller não encontrado (registro HKLM Uninstall/Discovery.RMM e %s no diretório do agente)", uninstallerFileName)
}

// validateUninstallerPath exige caminho canônico (sem symlinks/junctions),
// arquivo regular chamado uninstall.exe, contido no diretório confiável de
// instalação (nunca ProgramData/Temp/UserProfile), com ACL administrativa e
// assinatura Authenticode consistente quando o próprio binário em execução é
// assinado.
func validateUninstallerPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("caminho do uninstaller vazio")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("caminho do uninstaller invalido (%s): %w", path, err)
	}
	absPath = filepath.Clean(absPath)

	linfo, err := os.Lstat(absPath)
	if err != nil {
		return "", fmt.Errorf("uninstaller não encontrado em %s: %w", absPath, err)
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("uninstaller não pode ser symlink: %s", absPath)
	}

	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("falha ao resolver caminho canônico do uninstaller %s: %w", absPath, err)
	}
	resolved = filepath.Clean(resolved)
	if !strings.EqualFold(resolved, absPath) {
		return "", fmt.Errorf("uninstaller aponta para caminho redirecionado (%s -> %s)", absPath, resolved)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("uninstaller não encontrado em %s: %w", resolved, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return "", fmt.Errorf("uninstaller não é arquivo regular: %s", resolved)
	}
	if !strings.EqualFold(filepath.Base(resolved), uninstallerFileName) {
		return "", fmt.Errorf("executável inesperado para desinstalação: %s", filepath.Base(resolved))
	}

	dir := filepath.Dir(resolved)
	for _, forbidden := range untrustedUninstallerRootsFn() {
		if pathHasDirPrefixCI(dir, forbidden) {
			return "", fmt.Errorf("uninstaller em diretório não confiável (%s): %s", forbidden, resolved)
		}
	}
	if !isTrustedUninstallerDir(dir) {
		return "", fmt.Errorf("uninstaller fora do diretório de instalação confiável: %s", resolved)
	}

	if verifyUninstallerACLFn != nil {
		if aclErr := verifyUninstallerACLFn(resolved); aclErr != nil {
			return "", fmt.Errorf("ACL/owner do uninstaller não confiável: %w", aclErr)
		}
	}

	if verifyUninstallerAuthenticodeFn != nil {
		if exe, exeErr := os.Executable(); exeErr == nil && strings.TrimSpace(exe) != "" {
			if verifyUninstallerAuthenticodeFn(exe) == nil {
				if sigErr := verifyUninstallerAuthenticodeFn(resolved); sigErr != nil {
					return "", fmt.Errorf("assinatura Authenticode do uninstaller inválida: %w", sigErr)
				}
			}
		}
	}

	return resolved, nil
}

func isTrustedUninstallerDir(dir string) bool {
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	if cleanDir == "" || cleanDir == "." {
		return false
	}
	for _, root := range trustedUninstallerRootsFn() {
		if cleanRoot := filepath.Clean(strings.TrimSpace(root)); cleanRoot != "" && strings.EqualFold(cleanDir, cleanRoot) {
			return true
		}
	}
	return false
}

func defaultTrustedUninstallerRoots() []string {
	var roots []string
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, envName := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if base := strings.TrimSpace(os.Getenv(envName)); base != "" {
			roots = append(roots, filepath.Join(base, "Discovery"))
		}
	}
	return roots
}

func defaultUntrustedUninstallerRoots() []string {
	var roots []string
	for _, envName := range []string{"ProgramData", "TEMP", "TMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		if base := strings.TrimSpace(os.Getenv(envName)); base != "" {
			roots = append(roots, base)
		}
	}
	if tmpDir := strings.TrimSpace(os.TempDir()); tmpDir != "" {
		roots = append(roots, tmpDir)
	}
	return roots
}

func pathHasDirPrefixCI(candidate, prefix string) bool {
	c := strings.ToLower(filepath.Clean(strings.TrimSpace(candidate)))
	p := strings.ToLower(filepath.Clean(strings.TrimSpace(prefix)))
	if c == "" || p == "" || p == "." {
		return false
	}
	if c == p {
		return true
	}
	if !strings.HasSuffix(p, string(filepath.Separator)) {
		p += string(filepath.Separator)
	}
	return strings.HasPrefix(c, p)
}

// parseUninstallerExecutable extrai o caminho do executável de um
// UninstallString/QuietUninstallString do registro. Formatos aceitos:
//
//	"C:/Program Files/Discovery/uninstall.exe" /S
//	C:/Program Files/Discovery/uninstall.exe /S
//	"C:/Program Files/Discovery/uninstall.exe"
func parseUninstallerExecutable(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if raw[0] == '"' {
		if end := strings.Index(raw[1:], "\""); end >= 0 {
			return strings.TrimSpace(raw[1 : 1+end])
		}
		return ""
	}

	// Sem aspas: corta logo após o primeiro ".exe" (o caminho pode ter espaços).
	if idx := strings.Index(strings.ToLower(raw), ".exe"); idx >= 0 {
		return strings.TrimSpace(raw[:idx+len(".exe")])
	}
	return raw
}
