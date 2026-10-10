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
	pid, err := launchAgentUninstaller(uninstallerPath)
	if err != nil {
		agentDecommissionLaunching.Store(false)
		return err
	}
	a.Logs.Append(fmt.Sprintf("[decommission] uninstaller iniciado (pid=%d): %s", pid, uninstallerPath))
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
	if !agentDecommissionLaunching.CompareAndSwap(false, true) {
		return
	}

	a.Logs.Append("[decommission] retomando desinstalação pendente: " + req.UninstallerPath)
	a.safeGo(func() {
		if err := a.runAgentUninstaller(req.UninstallerPath); err != nil {
			a.Logs.Append("[decommission] falha ao retomar uninstaller: " + err.Error())
			return
		}
		a.markAgentDecommissionLaunched()
	})
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

// validateUninstallerPath exige arquivo existente e nome esperado.
func validateUninstallerPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("caminho do uninstaller vazio")
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("uninstaller não encontrado em %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("uninstaller aponta para um diretório: %s", path)
	}
	if !strings.EqualFold(filepath.Base(path), uninstallerFileName) {
		return "", fmt.Errorf("executável inesperado para desinstalação: %s", filepath.Base(path))
	}
	return path, nil
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
