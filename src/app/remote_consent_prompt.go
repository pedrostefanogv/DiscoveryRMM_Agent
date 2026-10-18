package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/inventory/native"
	"discovery/app/core/platform"
	"discovery/app/core/processutil"
	"discovery/app/core/remotesession"
	"discovery/app/core/screen"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

// ── Prompt de consentimento (PSADT primário, nativo como fallback) ─────────

var (
	remoteConsentRuntimeMu sync.Mutex
	remoteConsentAPI       string
	remoteConsentToken     string
	remoteConsentAgentID   string
)

// configureRemoteConsentRuntime injeta endpoint/token/agentId VIVOS do processo
// que inicia a sessão (worker ou UI). Sem isso o reporte lê os arquivos
// persistidos, que podem estar defasados após rotação de token.
func configureRemoteConsentRuntime(apiBase, token, agentID string) {
	remoteConsentRuntimeMu.Lock()
	defer remoteConsentRuntimeMu.Unlock()
	remoteConsentAPI = strings.TrimSpace(apiBase)
	remoteConsentToken = strings.TrimSpace(token)
	remoteConsentAgentID = strings.TrimSpace(agentID)
}

// remoteConsentPrompt tenta o prompt Fluent do PSADT (contagem regressiva) e,
// se o módulo/estilização falhar, cai para o MessageBox com timeout.
func remoteConsentPrompt(ctx context.Context, req remotesession.ConsentRequest) remotesession.ConsentDecision {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return remotesession.ConsentDecision{
				Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
				Auto:          true,
				Outcome:       "timeout-decision",
				Reason:        "deadline-exceeded",
				PromptBackend: "timeout",
				DecidedAt:     time.Now().UTC(),
			}
		}
		if remaining < req.Timeout {
			req.Timeout = remaining
		}
	}

	if runtime.GOOS != "windows" {
		return remotesession.ConsentDecision{
			Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
			Auto:          true,
			Outcome:       "timeout-decision",
			Reason:        "prompt-unsupported",
			PromptBackend: "none",
			DecidedAt:     time.Now().UTC(),
		}
	}

	if decision, ok := psadtConsentPrompt(ctx, req); ok {
		return decision
	}
	// O PSADT pode ter consumido parte do prazo antes de falhar: o fallback
	// nativo nunca pode esperar mais do que o deadline do servidor.
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return remotesession.ConsentDecision{
				Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
				Auto:          true,
				Outcome:       "timeout-decision",
				Reason:        "deadline-exceeded",
				PromptBackend: "timeout",
				DecidedAt:     time.Now().UTC(),
			}
		}
		req.Timeout = remaining
	}
	if decision, ok := nativeConsentPrompt(req); ok {
		return decision
	}

	return remotesession.ConsentDecision{
		Accepted:      false,
		Auto:          true,
		Outcome:       "prompt-unavailable",
		Reason:        "nenhum prompt disponível",
		PromptBackend: "none",
		DecidedAt:     time.Now().UTC(),
	}
}

// consentDefaultButton mapeia a ação do timeout para o botão padrão do prompt.
func consentDefaultButton(timeoutAction string) string {
	if strings.EqualFold(strings.TrimSpace(timeoutAction), "approve") {
		return "Left" // Permitir
	}
	return "Right" // Negar
}

func psadtConsentPrompt(ctx context.Context, req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	timeoutSeconds := int(req.Timeout.Seconds())
	if timeoutSeconds < 10 {
		timeoutSeconds = 10
	}
	if timeoutSeconds > 600 {
		timeoutSeconds = 600
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Autorização de acesso remoto"
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "O suporte solicita acesso remoto ao seu computador. Você autoriza?"
	}

	visualReq := PSADTVisualNotificationRequest{
		NotifType:        "prompt_yesno",
		Title:            title,
		Message:          message,
		Subtitle:         "Acesso remoto",
		AppName:          "Sessão remota",
		PromptLeftText:   "Permitir",
		PromptRightText:  "Negar",
		PromptIcon:       "Question",
		// Enter/clique padrão segue a ação configurada: não "permite por acidente"
		// quando a política é negar no timeout.
		PromptDefaultButton: consentDefaultButton(req.TimeoutAction),
		PromptTimeout:       timeoutSeconds,
		PromptNoWait:     false,
		PromptNotTopMost: false,
	}
	script, scriptTimeout := buildPSADTVisualScript(visualReq)
	if scriptTimeout < req.Timeout+45*time.Second {
		scriptTimeout = req.Timeout + 45*time.Second
	}

	runDir, err := platform.EnsureTempDir()
	if err != nil {
		return remotesession.ConsentDecision{}, false
	}
	dir, err := os.MkdirTemp(runDir, "consent-prompt-")
	if err != nil {
		return remotesession.ConsentDecision{}, false
	}
	defer os.RemoveAll(dir)

	scriptPath := filepath.Join(dir, "consent.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return remotesession.ConsentDecision{}, false
	}

	runCtx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "powershell", "-NoProfile", "-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.Env = append(os.Environ(),
		"PSADT_STAGING_CONFIG=0",
		"PSADT_TITLE="+title,
		"PSADT_MESSAGE="+message,
		"PSADT_SUBTITLE=Acesso remoto",
		"PSADT_APPNAME=Sessão remota",
		"PSADT_APPVENDOR=",
		"PSADT_APPVERSION=",
		"PSADT_PROMPT_LEFT=Permitir",
		"PSADT_PROMPT_MIDDLE=",
		"PSADT_PROMPT_RIGHT=Negar",
		"PSADT_PROMPT_ICON=Question",
		fmt.Sprintf("PSADT_PROMPT_TIMEOUT=%d", timeoutSeconds),
		"PSADT_PROMPT_NOWAIT=0",
		"PSADT_PROMPT_NOT_TOPMOST=0",
		"PSADT_DURATION=0",
		"PSADT_BALLOON_TIME=0",
		"PSADT_BALLOON_NOWAIT=0",
		"PSADT_DIALOG_BUTTONS=",
		"PSADT_DIALOG_DEFAULT=",
		"PSADT_DIALOG_ICON=",
		"PSADT_DIALOG_TIMEOUT=0",
		"PSADT_DIALOG_NOWAIT=0",
		"PSADT_DIALOG_EXIT_ON_TIMEOUT=0",
		"PSADT_DIALOG_NOT_TOPMOST=0",
		"PSADT_DIALOG_FORCE=0",
		"PSADT_RESTART_COUNTDOWN=0",
		"PSADT_RESTART_NO_COUNTDOWN=0",
		"PSADT_WELCOME_PROCESSES=",
		"PSADT_WELCOME_ALLOW_DEFER=0",
		"PSADT_COUNTDOWN_NO_DEFER=0",
		"PSADT_WELCOME_DEFER_TIMES=0",
		"PSADT_WELCOME_DEFER_DEADLINE=",
		"PSADT_WELCOME_BLOCK_EXEC=0",
		"PSADT_WELCOME_CLOSE_COUNTDOWN=0",
	)
	processutil.HideWindow(cmd)

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	text := output.String()

	// Contexto vencido/cancelado durante o prompt: aplica a ação de timeout (não
	// abre outro prompt depois do prazo do servidor).
	if runCtx.Err() != nil {
		decision := remotesession.ConsentDecision{
			Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
			Auto:          true,
			Outcome:       "timeout-denied",
			Reason:        "timeout",
			PromptBackend: "psadt",
			DecidedAt:     time.Now().UTC(),
		}
		if decision.Accepted {
			decision.Outcome = "timeout-granted"
		}
		if ctx.Err() == nil {
			return decision, true
		}
		return decision, true
	}

	// Falha de infraestrutura do PSADT -> fallback nativo (não é decisão).
	if err != nil && !strings.Contains(text, "PSADT_RESULT=") && !strings.Contains(text, "sem resposta") {
		return remotesession.ConsentDecision{}, false
	}
	for _, marker := range []string{
		"falha ao importar psappdeploytoolkit",
		"falha ao abrir sessao psadt",
		"is not recognized",
		"ambiguousparameterset",
	} {
		if strings.Contains(strings.ToLower(text), marker) {
			return remotesession.ConsentDecision{}, false
		}
	}

	result := parsePSADTConsentResult(text)
	switch {
	case strings.EqualFold(result, "Permitir"):
		return remotesession.ConsentDecision{
			Accepted:      true,
			Outcome:       "granted",
			Reason:        "user_allowed",
			PromptBackend: "psadt",
			DecidedAt:     time.Now().UTC(),
		}, true
	case strings.EqualFold(result, "Negar"):
		return remotesession.ConsentDecision{
			Accepted:      false,
			Outcome:       "denied",
			Reason:        "user_denied",
			PromptBackend: "psadt",
			DecidedAt:     time.Now().UTC(),
		}, true
	default:
		// Timeout: PSADT retorna sem resposta. A ação configurada decide.
		decision := remotesession.ConsentDecision{
			Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
			Auto:          true,
			Outcome:       "timeout-denied",
			Reason:        "timeout",
			PromptBackend: "psadt",
			DecidedAt:     time.Now().UTC(),
		}
		if decision.Accepted {
			decision.Outcome = "timeout-granted"
		}
		return decision, true
	}
}

func parsePSADTConsentResult(output string) string {
	marker := "PSADT_RESULT="
	index := strings.LastIndex(output, marker)
	if index < 0 {
		return ""
	}
	line := strings.SplitN(output[index+len(marker):], "\n", 2)[0]
	line = strings.TrimSpace(line)
	var result string
	if err := json.Unmarshal([]byte(line), &result); err == nil {
		return strings.TrimSpace(result)
	}
	return strings.Trim(line, "\" \r\n")
}

// nativeConsentPrompt usa MessageBoxTimeoutW como fallback: caixa topmost com
// timeout e Sim/Não. Não tem contador animado, mas informa os segundos no texto.
func nativeConsentPrompt(req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("MessageBoxTimeoutW")
	if err := proc.Find(); err != nil {
		return remotesession.ConsentDecision{}, false
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Autorização de acesso remoto"
	}
	seconds := int(req.Timeout.Seconds())
	if seconds < 10 {
		seconds = 10
	}
	message := fmt.Sprintf("%s\n\nA janela fecha em %d segundos e a conexão será %s.",
		strings.TrimSpace(req.Message), seconds,
		map[bool]string{true: "autorizada", false: "recusada"}[strings.EqualFold(req.TimeoutAction, "approve")])

	titlePtr, _ := windows.UTF16PtrFromString(title)
	messagePtr, _ := windows.UTF16PtrFromString(message)
	const (
		mbYesNo         = 0x00000004
		mbIconQuestion  = 0x00000020
		mbTopMost       = 0x00040000
		mbSetForeground = 0x00010000
	)
	flags := uintptr(mbYesNo | mbIconQuestion | mbTopMost | mbSetForeground)
	result, _, _ := proc.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)),
		flags, 0, uintptr(seconds*1000))

	switch result {
	case 6: // IDYES
		return remotesession.ConsentDecision{
			Accepted:      true,
			Outcome:       "granted",
			Reason:        "user_allowed",
			PromptBackend: "native",
			DecidedAt:     time.Now().UTC(),
		}, true
	case 7: // IDNO
		return remotesession.ConsentDecision{
			Accepted:      false,
			Outcome:       "denied",
			Reason:        "user_denied",
			PromptBackend: "native",
			DecidedAt:     time.Now().UTC(),
		}, true
	default: // 32000 = timeout
		decision := remotesession.ConsentDecision{
			Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
			Auto:          true,
			Outcome:       "timeout-denied",
			Reason:        "timeout",
			PromptBackend: "native",
			DecidedAt:     time.Now().UTC(),
		}
		if decision.Accepted {
			decision.Outcome = "timeout-granted"
		}
		return decision, true
	}
}

// workerAPIBase monta a base HTTP (scheme://host) a partir do config do worker.
func workerAPIBase(cfg WorkerDebugConfig) string {
	scheme := strings.TrimSpace(cfg.ApiScheme)
	if scheme == "" {
		scheme = "https"
	}
	server := strings.TrimSpace(cfg.ApiServer)
	if server == "" {
		return ""
	}
	if strings.Contains(server, "://") {
		return strings.TrimRight(server, "/")
	}
	return scheme + "://" + strings.TrimRight(server, "/")
}

// wireRemoteConsentManager liga prompt/report/usuário/banner ao manager.
// Chamado pelo worker (sessão interativa) e pelo processo de UI standalone.
func wireRemoteConsentManager(mgr *remotesession.Manager) {
	if mgr == nil {
		return
	}
	mgr.SetConsentPrompt(remoteConsentPrompt)
	mgr.SetConsentReporter(reportRemoteConsentDecision)
	mgr.SetInteractiveUser(remoteConsentInteractiveUser)
	mgr.SetBannerFactory(func(opts remotesession.BannerOptions) (remotesession.SessionBanner, error) {
		return screen.NewSessionBanner(screen.BannerOptions{
			Operator:        opts.Operator,
			Text:            opts.Text,
			AllowDisconnect: opts.AllowDisconnect,
			MonitorIndex:    opts.MonitorIndex,
			OnDisconnect:    opts.OnDisconnect,
		})
	})
}

// reportBannerDisconnectIfNeeded reporta ao servidor o encerramento iniciado
// pelo banner (best-effort; o viewer também chama stop).
func reportBannerDisconnectIfNeeded(sessionID, reason string) {
	if reason != "banner-disconnect" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = reportRemoteSessionStop(ctx, sessionID, reason)
	}()
}

// remoteConsentInteractiveUser retorna o usuário da sessão interativa ("" =
// logon/lock sem usuário ativo -> ação de timeout imediata).
func remoteConsentInteractiveUser() string {
	return strings.TrimSpace(native.CollectPrimaryLoggedInUser())
}

// reportRemoteConsentDecision envia a decisão ao servidor (agent-auth).
func reportRemoteConsentDecision(ctx context.Context, sessionID string, decision remotesession.ConsentDecision) error {
	apiBase, token := remoteConsentEndpoint()
	if apiBase == "" || token == "" {
		return fmt.Errorf("endpoint/token de consentimento indisponível")
	}

	body, err := json.Marshal(map[string]any{
		"accepted":      decision.Accepted,
		"auto":          decision.Auto,
		"outcome":       decision.Outcome,
		"reason":        decision.Reason,
		"promptBackend": decision.PromptBackend,
		"decidedAtUtc":  decision.DecidedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	url := strings.TrimRight(apiBase, "/") + "/api/v1/agent-auth/me/remote-sessions/" + sessionID + "/consent"
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			cancel()
			return reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		_ = netutil.SetAgentAuthHeadersWithAgentID(req, token, remoteConsentAgent())
		resp, doErr := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
		cancel()
		if doErr == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			// 409 = decisão tardia: não adianta retry (o agente não inicia).
			if resp.StatusCode == http.StatusConflict {
				return lastErr
			}
		} else {
			lastErr = doErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return lastErr
}

func reportRemoteSessionStop(ctx context.Context, sessionID, reason string) error {
	apiBase, token := remoteConsentEndpoint()
	if apiBase == "" || token == "" {
		return fmt.Errorf("endpoint/token indisponível")
	}
	body, _ := json.Marshal(map[string]any{"reason": reason})
	url := strings.TrimRight(apiBase, "/") + "/api/v1/agent-auth/me/remote-sessions/" + sessionID + "/stop"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	_ = netutil.SetAgentAuthHeadersWithAgentID(req, token, remoteConsentAgent())
	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func remoteConsentAgent() string {
	remoteConsentRuntimeMu.Lock()
	agentID := remoteConsentAgentID
	remoteConsentRuntimeMu.Unlock()
	if agentID == "" {
		agentID = strings.TrimSpace(GetDebugConfigForWorker().AgentID)
	}
	return agentID
}

func remoteConsentEndpoint() (string, string) {
	remoteConsentRuntimeMu.Lock()
	apiBase, token := remoteConsentAPI, remoteConsentToken
	remoteConsentRuntimeMu.Unlock()

	cfg := GetDebugConfigForWorker()
	if apiBase == "" {
		scheme := strings.TrimSpace(cfg.ApiScheme)
		if scheme == "" {
			scheme = "https"
		}
		server := strings.TrimSpace(cfg.ApiServer)
		if server != "" {
			if strings.Contains(server, "://") {
				apiBase = server
			} else {
				apiBase = scheme + "://" + server
			}
		}
	}
	if token == "" {
		token = GetInstallerAuthTokenForWorker()
		if token == "" {
			token = strings.TrimSpace(cfg.AuthToken)
		}
	}
	return strings.TrimRight(apiBase, "/"), token
}
