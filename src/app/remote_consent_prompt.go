package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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

// remoteConsentPrompt abre o prompt de consentimento do desktop. Ordem:
//
//  1. prompt NATIVO desenhado pelo agente (remote_consent_native_windows.go):
//     cartão com o tema do Windows do usuário, CONTAGEM REGRESSIVA visível e a
//     AÇÃO PADRÃO explícita ("o acesso será NEGADO automaticamente"). O
//     Show-ADTInstallationPrompt do PSADT não mostra contador, por isso ele
//     deixou de ser o backend principal;
//  2. prompt PSADT (Fluent, com a identidade visual do toolkit) como fallback
//     quando a janela nativa não pode ser criada (sem desktop interativo);
//  3. sem prompt: nega (fail closed).
func remoteConsentPrompt(ctx context.Context, req remotesession.ConsentRequest) remotesession.ConsentDecision {
	if decision, expired := clipConsentTimeout(ctx, &req); expired {
		return decision
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

	if decision, ok := nativeConsentPrompt(ctx, req); ok {
		return decision
	}
	// O prompt nativo pode ter consumido parte do prazo antes de falhar: o
	// fallback PSADT nunca pode esperar mais do que o deadline do servidor.
	if decision, expired := clipConsentTimeout(ctx, &req); expired {
		return decision
	}
	if decision, ok := psadtConsentPrompt(ctx, req); ok {
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

// clipConsentTimeout reduz req.Timeout ao tempo restante do deadline do
// servidor e devolve a decisão de timeout quando o prazo já venceu.
func clipConsentTimeout(ctx context.Context, req *remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return remotesession.ConsentDecision{}, false
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return remotesession.ConsentDecision{
			Accepted:      strings.EqualFold(req.TimeoutAction, "approve"),
			Auto:          true,
			Outcome:       "timeout-decision",
			Reason:        "deadline-exceeded",
			PromptBackend: "timeout",
			DecidedAt:     time.Now().UTC(),
		}, true
	}
	if remaining < req.Timeout {
		req.Timeout = remaining
	}
	return remotesession.ConsentDecision{}, false
}

// consentDefaultActionVerb é o verbo da ação aplicada quando o tempo termina sem
// resposta ("PERMITIDO"/"NEGADO").
func consentDefaultActionVerb(timeoutAction string) string {
	if strings.EqualFold(strings.TrimSpace(timeoutAction), "approve") {
		return "PERMITIDO"
	}
	return "NEGADO"
}

// consentDefaultActionText explica ao usuário o que acontece ao fim do tempo.
func consentDefaultActionText(timeoutAction string) string {
	return "Se você não responder, o acesso será " + consentDefaultActionVerb(timeoutAction) + " automaticamente."
}

// consentCountdownClock formata o tempo restante como mm:ss arredondando para
// CIMA (o prompt nunca mostra 00:00 antes de expirar).
func consentCountdownClock(remaining time.Duration) string {
	if remaining < 0 {
		remaining = 0
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
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
	// O Show-ADTInstallationPrompt NÃO desenha contador (só os diálogos de
	// Welcome/Restart têm timer): como este é o fallback, a mensagem precisa
	// dizer qual ação padrão será aplicada e o tempo total para responder.
	message = message + "\n\n" + consentDefaultActionText(req.TimeoutAction) +
		" Tempo para responder: " + consentCountdownClock(time.Duration(timeoutSeconds)*time.Second) + "."

	visualReq := PSADTVisualNotificationRequest{
		NotifType:       "prompt_yesno",
		Title:           title,
		Message:         message,
		Subtitle:        "Acesso remoto",
		AppName:         "Sessão remota",
		PromptLeftText:  "Permitir",
		PromptRightText: "Negar",
		PromptIcon:      "Question",
		// Enter/clique padrão segue a ação configurada: não "permite por acidente"
		// quando a política é negar no timeout.
		PromptDefaultButton: consentDefaultButton(req.TimeoutAction),
		PromptTimeout:       timeoutSeconds,
		PromptNoWait:        false,
		PromptNotTopMost:    false,
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
	// Estado autoritativo da sessão no servidor (usado no replay do worker).
	mgr.SetSessionStatusFetcher(fetchRemoteSessionStatus)
	// Banner: informativo e click-through (não captura cliques do app em uso).
	mgr.SetBannerFactory(func(opts remotesession.BannerOptions) (remotesession.SessionBanner, error) {
		return screen.NewSessionBanner(screen.BannerOptions{
			Operator:        opts.Operator,
			Text:            opts.Text,
			AllowDisconnect: opts.AllowDisconnect,
			MonitorIndex:    opts.MonitorIndex,
		})
	})
	// Ícone da bandeja: é onde o usuário local desconecta a sessão.
	mgr.SetTrayFactory(func(opts remotesession.TrayOptions) (remotesession.SessionTray, error) {
		return screen.NewSessionTray(screen.SessionTrayOptions{
			Operator:        opts.Operator,
			Text:            opts.Text,
			AllowDisconnect: opts.AllowDisconnect,
			OnDisconnect:    opts.OnDisconnect,
		})
	})
}

// reportAgentInitiatedCloseIfNeeded sincroniza com o servidor os encerramentos
// decididos pelo AGENT (banner, viewer-timeout, sessão sem viewer no grace).
// Sem isso o agent fecha o banner local mas a API mantém a sessão ACTIVE até o
// TTL, bloqueando novas sessões e exibindo "suporte conectado" no endpoint.
// Best-effort e idempotente no servidor (stop de sessão já fechada é no-op).
func reportAgentInitiatedCloseIfNeeded(sessionID, reason string) {
	switch reason {
	case "banner-disconnect", "viewer-timeout", "peer-timeout", "expired", "max-duration":
	default:
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

// fetchRemoteSessionStatus consulta o estado autoritativo da sessão no servidor
// (agent-auth). Usado no REPLAY do worker: se a sessão já está ACTIVE o prompt
// de consentimento NÃO é reexibido; se foi negada/encerrada, o start é recusado
// sem prompt. Falha de consulta NÃO bloqueia: o fluxo normal (prompt) segue.
func fetchRemoteSessionStatus(ctx context.Context, sessionID string) (remotesession.RemoteSessionStatus, error) {
	apiBase, token := remoteConsentEndpoint()
	if apiBase == "" || token == "" {
		return remotesession.RemoteSessionStatus{}, fmt.Errorf("endpoint/token de consentimento indisponível")
	}
	url := strings.TrimRight(apiBase, "/") + "/api/v1/agent-auth/me/remote-sessions/" + sessionID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return remotesession.RemoteSessionStatus{}, err
	}
	_ = netutil.SetAgentAuthHeadersWithAgentID(req, token, remoteConsentAgent())
	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return remotesession.RemoteSessionStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remotesession.RemoteSessionStatus{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Status  string `json:"status"`
		Consent struct {
			Outcome string `json:"outcome"`
		} `json:"consent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return remotesession.RemoteSessionStatus{}, err
	}
	return remotesession.RemoteSessionStatus{
		Status:         strings.TrimSpace(body.Status),
		ConsentOutcome: strings.TrimSpace(body.Consent.Outcome),
	}, nil
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
