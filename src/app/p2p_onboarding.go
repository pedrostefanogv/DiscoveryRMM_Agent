package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

const (
	onboardingDeployKeyTTL     = 30 * time.Minute
	onboardingRetryBase        = 30 * time.Second
	onboardingRetryMax         = 10 * time.Minute
	onboardingMaxAttempts      = 0 // 0 = ilimitado enquanto nao houver credenciais
	onboardingRetryInterval    = 60 * time.Second
	onboardingPeerRecheckDelay = 60 * time.Second
	onboardingAttemptTimeout   = 25 * time.Second
	p2pOnboardingEndpoint      = "/p2p/config/onboard"
)

// p2pOnboardingState tracks the onboarding progress for this agent.
type p2pOnboardingState struct {
	mu            sync.Mutex
	attempts      int
	lastAttemptAt time.Time
	configured    bool
	audit         []P2POnboardingAuditEvent
}

type zeroTouchRegisterCredentials struct {
	AuthToken string
	AgentID   string
	ApiScheme string
	ApiServer string
}

// isAgentConfigured returns true when this agent already has server credentials persisted.
func isAgentConfigured() bool {
	inst, _, err := loadInstallerConfig()
	if err != nil {
		return false
	}
	return strings.TrimSpace(inst.AuthToken) != "" && strings.TrimSpace(inst.ApiServer) != ""
}

// zeroTouchConfigRegistrationAllowed respeita o kill-switch local em config.json.
// Quando ausente, o comportamento padrao e permitir (true).
func (a *App) zeroTouchConfigRegistrationAllowed() bool {
	if a == nil {
		return true
	}
	cfg, _, err := loadInstallerConfig()
	if err != nil {
		return true
	}
	if cfg.AutoProvisioning == nil {
		return true
	}
	allowed := *cfg.AutoProvisioning
	if !allowed {
		a.logs.append("[zero-touch] autoProvisioning=false em config.json: zero-touch config registration desabilitado localmente")
	}
	return allowed
}

func (a *App) tryZeroTouchConfigRegistration(ctx context.Context, state *p2pOnboardingState, reason string) (bool, error) {
	if a == nil {
		return false, fmt.Errorf("app indisponivel")
	}
	if isAgentConfigured() {
		return false, nil
	}
	if !a.zeroTouchConfigRegistrationAllowed() {
		return false, fmt.Errorf("zero-touch config registration desabilitado localmente")
	}
	if !a.zeroTouchAttemptInFlight.CompareAndSwap(false, true) {
		return false, nil
	}
	defer a.zeroTouchAttemptInFlight.Store(false)

	if ctx == nil {
		ctx = context.Background()
	}

	attemptNumber := 1
	if state != nil {
		state.mu.Lock()
		attemptNumber = state.attempts + 1
		state.mu.Unlock()
	}
	a.logs.append(fmt.Sprintf("[zero-touch] tentativa %d (%s)", attemptNumber, strings.TrimSpace(reason)))

	attemptCtx, cancel := context.WithTimeout(ctx, onboardingAttemptTimeout)
	defer cancel()
	err := a.requestOnboardingFromPeers(attemptCtx, state)

	if state != nil {
		state.mu.Lock()
		state.attempts++
		state.lastAttemptAt = time.Now().UTC()
		state.mu.Unlock()
	}
	return true, err
}

// RunOnboardingLoop periodically requests configuration from the local P2P network
// when this agent has no server credentials ("generic agent" state).
// Exits when configured, max attempts reached, or ctx cancelled.
func (a *App) RunOnboardingLoop(ctx context.Context) {
	if isAgentConfigured() {
		a.logs.append("[zero-touch] agente ja configurado, loop de zero-touch config registration nao iniciado")
		return
	}
	if !a.zeroTouchConfigRegistrationAllowed() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	state := &p2pOnboardingState{}
	a.logs.append("[zero-touch] agente generico detectado: aguardando Zero-Touch Config Registration")

	tryAttempt := func(reason string) bool {
		if isAgentConfigured() {
			a.logs.append("[zero-touch] configuracao recebida com sucesso, loop encerrado")
			return true
		}
		state.mu.Lock()
		attempts := state.attempts
		state.mu.Unlock()
		if onboardingMaxAttempts > 0 && attempts >= onboardingMaxAttempts {
			a.logs.append("[zero-touch] limite de tentativas atingido, loop encerrado")
			return true
		}
		attempted, err := a.tryZeroTouchConfigRegistration(ctx, state, reason)
		if err != nil {
			a.logs.append("[zero-touch] falha: " + err.Error())
		} else if !attempted {
			a.logs.append("[zero-touch] tentativa ignorada: outra tentativa em andamento")
		}
		if isAgentConfigured() {
			a.logs.append("[zero-touch] configuracao recebida com sucesso, loop encerrado")
			return true
		}
		return false
	}

	if tryAttempt("startup-imediato") {
		return
	}

	ticker := time.NewTicker(onboardingRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if tryAttempt("retry-peers-60s") {
				return
			}
		}
	}
}

func (a *App) triggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx context.Context, peer p2pDiscoveredPeer) {
	if a == nil || isAgentConfigured() || !a.zeroTouchConfigRegistrationAllowed() {
		return
	}
	peerID := strings.TrimSpace(peer.AgentID)
	if peerID == "" {
		return
	}

	if attempted, err := a.tryZeroTouchConfigRegistration(ctx, nil, "peer-novo:"+peerID); err != nil {
		a.logs.append("[zero-touch] falha na tentativa imediata apos peer novo " + peerID + ": " + err.Error())
	} else if attempted {
		a.logs.append("[zero-touch] tentativa imediata executada apos descobrir peer=" + peerID)
	}

	timer := time.NewTimer(onboardingPeerRecheckDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	if isAgentConfigured() {
		return
	}
	if attempted, err := a.tryZeroTouchConfigRegistration(ctx, nil, "recheck-60s:"+peerID); err != nil {
		a.logs.append("[zero-touch] falha no recheck de 60s apos peer " + peerID + ": " + err.Error())
	} else if attempted {
		a.logs.append("[zero-touch] recheck de 60s executado para peers conhecidos (peer inicial=" + peerID + ")")
	}
}

func (a *App) requestOnboardingFromPeers(ctx context.Context, state *p2pOnboardingState) error {
	if a.p2pCoord == nil {
		return fmt.Errorf("coordinator P2P indisponivel")
	}
	peers := a.p2pCoord.GetPeers()
	if len(peers) == 0 {
		return fmt.Errorf("nenhum peer conhecido na rede")
	}
	for _, peer := range peers {
		if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
			continue
		}
		endpoint := fmt.Sprintf("http://%s:%d%s", strings.TrimSpace(peer.Address), peer.Port, p2pOnboardingEndpoint)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}
		var offer P2POnboardingRequest
		if err := json.NewDecoder(resp.Body).Decode(&offer); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		result, applyErr := a.applyOnboardingOffer(offer)
		event := P2POnboardingAuditEvent{
			TimestampUTC:  time.Now().UTC().Format(time.RFC3339),
			SourceAgentID: strings.TrimSpace(offer.SourceAgent),
			ServerURL:     strings.TrimSpace(offer.ServerURL),
			Success:       applyErr == nil,
		}
		if applyErr != nil {
			event.Message = applyErr.Error()
			a.logs.append("[zero-touch] oferta rejeitada de " + peer.AgentID + ": " + applyErr.Error())
		} else {
			event.Message = "configurado com sucesso"
			event.TargetAgentID = result.AgentID
			a.logs.append("[zero-touch] configurado via peer=" + peer.AgentID + " agentId=" + result.AgentID)
		}
		if state != nil {
			state.mu.Lock()
			state.audit = append(state.audit, event)
			state.configured = applyErr == nil
			state.mu.Unlock()
		}
		if applyErr == nil {
			return nil
		}
	}
	return fmt.Errorf("nenhum peer forneceu configuracao valida")
}

// applyOnboardingOffer validates and persists the received onboarding offer.
// Guards: already-configured check, TTL/expiry, required fields, and HMAC signature.
func (a *App) applyOnboardingOffer(offer P2POnboardingRequest) (P2POnboardingResult, error) {
	// Never overwrite valid existing credentials.
	if isAgentConfigured() {
		return P2POnboardingResult{}, fmt.Errorf("agente ja esta configurado")
	}

	// Validate expiry (TTL anti-replay layer 1).
	exp, err := time.Parse(time.RFC3339, strings.TrimSpace(offer.ExpiresAtUTC))
	if err != nil || time.Now().UTC().After(exp) {
		return P2POnboardingResult{}, fmt.Errorf("oferta expirada ou invalida")
	}

	// Required fields.
	if strings.TrimSpace(offer.ServerURL) == "" || strings.TrimSpace(offer.DeployKey) == "" {
		return P2POnboardingResult{}, fmt.Errorf("payload incompleto: serverUrl ou deployKey ausentes")
	}
	if strings.TrimSpace(offer.Nonce) == "" || strings.TrimSpace(offer.Signature) == "" {
		return P2POnboardingResult{}, fmt.Errorf("payload sem nonce/assinatura")
	}

	// Validate HMAC-SHA256 signature (anti-replay layer 2).
	expected := computeOnboardingSignature(
		strings.TrimSpace(offer.SourceAgent),
		strings.TrimSpace(offer.ServerURL),
		strings.TrimSpace(offer.DeployKey),
		strings.TrimSpace(offer.ExpiresAtUTC),
		strings.TrimSpace(offer.Nonce),
	)
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(offer.Signature))) {
		return P2POnboardingResult{}, fmt.Errorf("assinatura invalida")
	}

	return a.registerWithDeployKey(offer.ServerURL, offer.DeployKey)
}

// validateServerURL ensures the URL uses http or https and has a non-empty host,
// preventing SSRF via unexpected schemes (file://, data://, etc.).
func validateServerURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL do servidor invalida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL do servidor deve usar http ou https, obtido: %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL do servidor sem host")
	}
	return nil
}

// registerWithDeployKey calls the server registration endpoint with the deploy key
// and persists the returned credentials.
func (a *App) registerWithDeployKey(serverURL, deployKey string) (P2POnboardingResult, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if err := validateServerURL(serverURL); err != nil {
		return P2POnboardingResult{}, err
	}
	hostname, _ := os.Hostname()
	payload, _ := json.Marshal(map[string]string{
		"hostname":  hostname,
		"deployKey": deployKey,
	})
	endpoint := serverURL + "/api/v1/agent-install/register"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return P2POnboardingResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deployKey)

	resp, err := tlsutil.NewHTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return P2POnboardingResult{}, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao ler resposta de registro: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 240 {
			preview = preview[:240] + "..."
		}
		return P2POnboardingResult{}, fmt.Errorf("registro falhou HTTP %d: %s", resp.StatusCode, preview)
	}

	credentials, err := parseZeroTouchRegisterResponse(body, serverURL)
	if err != nil {
		return P2POnboardingResult{}, err
	}

	inst, path, err := loadInstallerConfigForZeroTouchPersist()
	if err != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao carregar config para persistencia zero-touch: %w", err)
	}
	inst.ServerURL = buildZeroTouchServerURL(credentials.ApiScheme, credentials.ApiServer)
	inst.ApiScheme = strings.TrimSpace(credentials.ApiScheme)
	inst.ApiServer = strings.TrimSpace(credentials.ApiServer)
	inst.AuthToken = strings.TrimSpace(credentials.AuthToken)
	inst.AgentID = strings.TrimSpace(credentials.AgentID)
	// Mantem o deploy token para resiliencia de bootstrap (fallback de re-registro).
	inst.APIKey = strings.TrimSpace(deployKey)

	if strings.TrimSpace(inst.ApiScheme) == "" || strings.TrimSpace(inst.ApiServer) == "" ||
		strings.TrimSpace(inst.AuthToken) == "" || strings.TrimSpace(inst.AgentID) == "" {
		return P2POnboardingResult{}, fmt.Errorf("credenciais incompletas apos registro zero-touch")
	}

	writePath, err := persistInstallerConfig(path, inst)
	if err != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao persistir credenciais: %w", err)
	}

	a.applyZeroTouchRuntimeConnection(inst)
	if !isAgentConfigured() {
		return P2POnboardingResult{}, fmt.Errorf("credenciais persistidas, mas agente ainda nao aparece provisionado")
	}

	a.logs.append("[zero-touch] credenciais persistidas em " + strings.TrimSpace(writePath) + " agentId=" + inst.AgentID)
	return P2POnboardingResult{
		AgentID:    inst.AgentID,
		Registered: true,
		Message:    "configurado via zero-touch config registration",
	}, nil
}

func loadInstallerConfigForZeroTouchPersist() (InstallerConfig, string, error) {
	baseCfg, basePath, baseFound, baseErr := loadInstallerConfigFromCandidates(installerConfigPathCandidates())
	if baseErr != nil {
		return InstallerConfig{}, "", baseErr
	}
	overrideCfg, _, overrideFound, overrideErr := loadInstallerConfigFromCandidates(installerOverridePathCandidates())
	if overrideErr != nil {
		return InstallerConfig{}, "", overrideErr
	}
	if baseFound {
		resolved := baseCfg
		if overrideFound {
			resolved = mergeInstallerOverride(baseCfg, overrideCfg)
		}
		return resolved, basePath, nil
	}
	if overrideFound {
		return overrideCfg, "", nil
	}
	return InstallerConfig{}, "", nil
}

func buildZeroTouchServerURL(scheme, server string) string {
	scheme = strings.TrimSpace(strings.ToLower(scheme))
	if scheme == "" {
		scheme = "https"
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	return scheme + "://" + server
}

func parseZeroTouchServerURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("server url vazio")
	}
	input := raw
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", "", err
	}
	scheme := strings.TrimSpace(strings.ToLower(u.Scheme))
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("scheme invalido: %s", scheme)
	}
	host := strings.TrimSpace(u.Host)
	if host == "" {
		host = strings.Trim(strings.TrimSpace(u.Path), "/")
	}
	if host == "" {
		return "", "", fmt.Errorf("host ausente em server url")
	}
	return scheme, host, nil
}

func firstNonEmptyAnyString(values ...any) string {
	for _, value := range values {
		s := strings.TrimSpace(fmt.Sprint(value))
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

func parseZeroTouchRegisterResponse(body []byte, fallbackServerURL string) (zeroTouchRegisterCredentials, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return zeroTouchRegisterCredentials{}, fmt.Errorf("resposta JSON invalida no registro zero-touch: %w", err)
	}

	extract := func(m map[string]any) zeroTouchRegisterCredentials {
		credentials := zeroTouchRegisterCredentials{
			AuthToken: firstNonEmptyAnyString(m["token"], m["authToken"], m["auth_token"], m["accessToken"], m["access_token"]),
			AgentID:   firstNonEmptyAnyString(m["agentId"], m["agentID"], m["agent_id"], m["id"]),
			ApiScheme: strings.ToLower(strings.TrimSpace(firstNonEmptyAnyString(m["apiScheme"], m["api_scheme"], m["scheme"]))),
			ApiServer: strings.TrimSpace(firstNonEmptyAnyString(m["apiServer"], m["api_server"], m["server"], m["serverHost"], m["server_host"])),
		}

		serverURL := strings.TrimSpace(firstNonEmptyAnyString(m["serverUrl"], m["server_url"], m["baseUrl"], m["base_url"]))
		if credentials.ApiScheme == "" || credentials.ApiServer == "" {
			if parsedScheme, parsedServer, err := parseZeroTouchServerURL(serverURL); err == nil {
				if credentials.ApiScheme == "" {
					credentials.ApiScheme = parsedScheme
				}
				if credentials.ApiServer == "" {
					credentials.ApiServer = parsedServer
				}
			}
		}

		return credentials
	}

	mergeMissing := func(dst *zeroTouchRegisterCredentials, src zeroTouchRegisterCredentials) {
		if strings.TrimSpace(dst.AuthToken) == "" {
			dst.AuthToken = strings.TrimSpace(src.AuthToken)
		}
		if strings.TrimSpace(dst.AgentID) == "" {
			dst.AgentID = strings.TrimSpace(src.AgentID)
		}
		if strings.TrimSpace(dst.ApiScheme) == "" {
			dst.ApiScheme = strings.TrimSpace(src.ApiScheme)
		}
		if strings.TrimSpace(dst.ApiServer) == "" {
			dst.ApiServer = strings.TrimSpace(src.ApiServer)
		}
	}

	credentials := extract(raw)
	for _, key := range []string{"data", "result", "payload"} {
		nested, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		mergeMissing(&credentials, extract(nested))
	}

	credentials.AuthToken = strings.TrimSpace(credentials.AuthToken)
	credentials.AgentID = strings.TrimSpace(credentials.AgentID)
	credentials.ApiScheme = strings.TrimSpace(strings.ToLower(credentials.ApiScheme))
	credentials.ApiServer = strings.TrimSpace(credentials.ApiServer)

	if credentials.AuthToken == "" || credentials.AgentID == "" {
		return zeroTouchRegisterCredentials{}, fmt.Errorf("resposta sem auth token/agent id no registro zero-touch")
	}

	if credentials.ApiScheme == "" || credentials.ApiServer == "" {
		parsedScheme, parsedServer, err := parseZeroTouchServerURL(fallbackServerURL)
		if err != nil {
			return zeroTouchRegisterCredentials{}, fmt.Errorf("resposta sem apiScheme/apiServer e fallback invalido: %w", err)
		}
		if credentials.ApiScheme == "" {
			credentials.ApiScheme = parsedScheme
		}
		if credentials.ApiServer == "" {
			credentials.ApiServer = parsedServer
		}
	}

	if credentials.ApiScheme != "http" && credentials.ApiScheme != "https" {
		return zeroTouchRegisterCredentials{}, fmt.Errorf("apiScheme invalido na resposta do registro zero-touch")
	}

	if strings.TrimSpace(credentials.ApiServer) == "" {
		return zeroTouchRegisterCredentials{}, fmt.Errorf("apiServer vazio na resposta do registro zero-touch")
	}

	return credentials, nil
}

func (a *App) applyZeroTouchRuntimeConnection(inst InstallerConfig) {
	if a == nil {
		return
	}
	if a.debugSvc != nil {
		a.debugSvc.ApplyRuntimeConnectionConfig(inst.ApiScheme, inst.ApiServer, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)
	}
	if a.serviceConnectedMode.Load() {
		a.requestServiceConfigReload(a.ctx, "zero-touch-registration")
	}
	if a.agentConn != nil {
		a.agentConn.Reload()
	}
}

// computeOnboardingSignature builds the HMAC-SHA256 for an onboarding offer.
// Key = deployKey itself (self-contained; no out-of-band shared secret needed).
func computeOnboardingSignature(sourceAgent, serverURL, deployKey, expiresAt, nonce string) string {
	payload := strings.Join([]string{sourceAgent, serverURL, deployKey, expiresAt, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(deployKey))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildOnboardingOffer creates a signed onboarding offer for distribution to unconfigured peers.
func BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (P2POnboardingRequest, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return P2POnboardingRequest{}, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	expiresAt := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	sig := computeOnboardingSignature(sourceAgentID, serverURL, deployKey, expiresAt, nonce)
	return P2POnboardingRequest{
		ServerURL:    serverURL,
		DeployKey:    deployKey,
		ExpiresAtUTC: expiresAt,
		SourceAgent:  sourceAgentID,
		Nonce:        nonce,
		Signature:    sig,
	}, nil
}

// handleP2POnboard is the HTTP handler for GET/PUT /p2p/config/onboard.
//
//	GET  → returns a signed offer when this agent is already configured (for unconfigured peers pulling).
//	PUT  → receives an offer pushed from another peer.
func (s *p2pTransferServer) handleP2POnboard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleOnboardOffer(w, r)
	case http.MethodPut:
		s.handleOnboardReceive(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleOnboardOffer responde ao GET /p2p/config/onboard com uma oferta de
// provisionamento assinada. Requer que este agente esteja configurado e que a
// feature DiscoveryEnabled esteja ativa na configuração do servidor.
func (s *p2pTransferServer) handleOnboardOffer(w http.ResponseWriter, r *http.Request) {
	if s.app == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !isAgentConfigured() {
		http.Error(w, "not configured", http.StatusNoContent)
		return
	}

	// Respeitar feature flag DiscoveryEnabled (controlada pelo servidor).
	agentCfg := s.app.GetAgentConfiguration()
	if agentCfg.DiscoveryEnabled != nil && !*agentCfg.DiscoveryEnabled {
		http.Error(w, "zero-touch config registration disabled", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	agentID := s.agentID
	s.mu.RUnlock()

	// Buscar token de provisionamento temporário na API.
	deployKey, expiresAt, err := s.app.requestProvisioningToken(r.Context())
	if err != nil {
		s.app.logs.append("[zero-touch] falha ao obter provisioning token: " + err.Error())
		http.Error(w, "provisioning token unavailable", http.StatusServiceUnavailable)
		recordAutoProvisioningEvent(s.app, agentID, "", false, "provisioning token error: "+err.Error())
		return
	}

	// Construir URL canônica (evitar inst.ServerURL legado).
	inst, _, loadErr := loadInstallerConfig()
	var serverURL string
	if loadErr == nil && strings.TrimSpace(inst.ApiScheme) != "" && strings.TrimSpace(inst.ApiServer) != "" {
		serverURL = strings.TrimSpace(inst.ApiScheme) + "://" + strings.TrimSpace(inst.ApiServer)
	} else if loadErr == nil && strings.TrimSpace(inst.ServerURL) != "" {
		// fallback compatível com instalações legadas
		serverURL = strings.TrimSpace(inst.ServerURL)
	}
	if serverURL == "" {
		http.Error(w, "server url not configured", http.StatusInternalServerError)
		recordAutoProvisioningEvent(s.app, agentID, "", false, "server url missing")
		return
	}

	// Calcular TTL a partir do expiresAt retornado pela API (com fallback).
	ttl := onboardingDeployKeyTTL
	if parsed, parseErr := time.Parse(time.RFC3339, expiresAt); parseErr == nil {
		if remaining := time.Until(parsed); remaining > 0 {
			ttl = remaining
		}
	}

	offer, err := BuildOnboardingOffer(agentID, serverURL, deployKey, ttl)
	if err != nil {
		http.Error(w, "failed to build offer", http.StatusInternalServerError)
		recordAutoProvisioningEvent(s.app, agentID, serverURL, false, "build offer error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(offer)

	// Registrar evento de auditoria (lado provisionador).
	recordAutoProvisioningEvent(s.app, agentID, serverURL, true, "offer emitida")
}

func (s *p2pTransferServer) handleOnboardReceive(w http.ResponseWriter, r *http.Request) {
	if s.app == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if isAgentConfigured() {
		http.Error(w, "already configured", http.StatusConflict)
		return
	}
	var offer P2POnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	result, err := s.app.applyOnboardingOffer(offer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// onboardingBackoff returns an exponential backoff with up to 20% jitter.
func onboardingBackoff(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt))
	base := float64(onboardingRetryBase) * exp
	if base > float64(onboardingRetryMax) {
		base = float64(onboardingRetryMax)
	}
	jitterMax := int64(base * 0.2)
	if jitterMax < 1 {
		jitterMax = 1
	}
	jitter, _ := rand.Int(rand.Reader, big.NewInt(jitterMax))
	return time.Duration(int64(base) + jitter.Int64())
}

// requestProvisioningToken solicita ao servidor um deploy key temporário para
// uso no auto-provisioning P2P. Retorna o deployKey, o expiresAt (RFC3339) e
// qualquer erro. Requer que o agente já esteja configurado com AuthToken válido.
func (a *App) requestProvisioningToken(ctx context.Context) (deployKey, expiresAt string, err error) {
	inst, _, err := loadInstallerConfig()
	if err != nil {
		return "", "", fmt.Errorf("config não disponível: %w", err)
	}
	if strings.TrimSpace(inst.AuthToken) == "" || strings.TrimSpace(inst.ApiServer) == "" {
		return "", "", fmt.Errorf("agente sem credenciais válidas")
	}

	scheme := strings.TrimSpace(inst.ApiScheme)
	if scheme == "" {
		scheme = "https"
	}
	endpoint := scheme + "://" + strings.TrimSpace(inst.ApiServer) + "/api/v1/agent-auth/me/zero-touch/deploy-token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("erro ao construir request: %w", err)
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, inst.AuthToken, inst.AgentID); err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := tlsutil.NewHTTPClient(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("falha na requisição: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("servidor retornou HTTP %d", resp.StatusCode)
	}

	var tokenResp P2PProvisioningTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", fmt.Errorf("resposta inválida do servidor: %w", err)
	}
	if strings.TrimSpace(tokenResp.Token) == "" {
		return "", "", fmt.Errorf("servidor retornou token vazio")
	}

	return strings.TrimSpace(tokenResp.Token), strings.TrimSpace(tokenResp.ExpiresAt), nil
}

// recordAutoProvisioningEvent regista um evento de auditoria no coordinator
// pelo lado do provisionador (agente configurado que entregou uma oferta).
func recordAutoProvisioningEvent(a *App, sourceAgentID, serverURL string, success bool, msg string) {
	if a == nil || a.p2pCoord == nil {
		return
	}
	event := P2POnboardingAuditEvent{
		TimestampUTC:  time.Now().UTC().Format(time.RFC3339),
		SourceAgentID: sourceAgentID,
		ServerURL:     serverURL,
		Success:       success,
		Message:       msg,
	}
	c := a.p2pCoord
	c.autoProvisionedMu.Lock()
	if success {
		c.autoProvisionedCount++
	}
	c.autoProvisionedAudit = append(c.autoProvisionedAudit, event)
	// Manter somente os 100 eventos mais recentes
	if len(c.autoProvisionedAudit) > 100 {
		c.autoProvisionedAudit = c.autoProvisionedAudit[len(c.autoProvisionedAudit)-100:]
	}
	c.autoProvisionedMu.Unlock()
}
