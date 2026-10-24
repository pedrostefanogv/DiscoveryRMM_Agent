package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"discovery/app/core/inventory/native"
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
//     AÇÃO PADRÃO explícita ("o acesso será NEGADO automaticamente").
//  2. sem prompt (janela não pôde ser criada): nega (fail closed).
//     O antigo fallback PSADT foi removido junto com a substituição do PSADT.
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
