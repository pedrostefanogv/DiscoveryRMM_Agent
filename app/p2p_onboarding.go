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
	"math"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	onboardingDeployKeyTTL = 30 * time.Minute
	onboardingRetryBase    = 30 * time.Second
	onboardingRetryMax     = 10 * time.Minute
	onboardingMaxAttempts  = 20
	p2pOnboardingEndpoint  = "/p2p/config/onboard"
)

// p2pOnboardingState tracks the onboarding progress for this agent.
type p2pOnboardingState struct {
	mu            sync.Mutex
	attempts      int
	lastAttemptAt time.Time
	configured    bool
	audit         []P2POnboardingAuditEvent
}

// isAgentConfigured returns true when this agent already has server credentials persisted.
func isAgentConfigured() bool {
	inst, _, err := loadInstallerConfig()
	if err != nil {
		return false
	}
	return strings.TrimSpace(inst.AuthToken) != "" && strings.TrimSpace(inst.ApiServer) != ""
}

// RunOnboardingLoop periodically requests configuration from the local P2P network
// when this agent has no server credentials ("generic agent" state).
// Exits when configured, max attempts reached, or ctx cancelled.
func (a *App) RunOnboardingLoop(ctx context.Context) {
	if isAgentConfigured() {
		a.logs.append("[onboarding] agente ja configurado, loop de onboarding nao iniciado")
		return
	}

	state := &p2pOnboardingState{}
	a.logs.append("[onboarding] agente generico detectado: aguardando configuracao da rede P2P")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if isAgentConfigured() {
			a.logs.append("[onboarding] configuracao recebida com sucesso, loop encerrado")
			return
		}

		state.mu.Lock()
		if state.attempts >= onboardingMaxAttempts {
			state.mu.Unlock()
			a.logs.append("[onboarding] limite de tentativas atingido, loop encerrado")
			return
		}
		attempt := state.attempts
		state.mu.Unlock()

		delay := onboardingBackoff(attempt)
		a.logs.append(fmt.Sprintf("[onboarding] tentativa %d aguardando %s", attempt+1, delay.Round(time.Second)))

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if err := a.requestOnboardingFromPeers(ctx, state); err != nil {
			a.logs.append("[onboarding] falha: " + err.Error())
		}

		state.mu.Lock()
		state.attempts++
		state.lastAttemptAt = time.Now().UTC()
		state.mu.Unlock()
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
			a.logs.append("[onboarding] oferta rejeitada de " + peer.AgentID + ": " + applyErr.Error())
		} else {
			event.Message = "configurado com sucesso"
			event.TargetAgentID = result.AgentID
			a.logs.append("[onboarding] configurado via peer=" + peer.AgentID + " agentId=" + result.AgentID)
		}
		state.mu.Lock()
		state.audit = append(state.audit, event)
		state.configured = applyErr == nil
		state.mu.Unlock()
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

// registerWithDeployKey calls the server registration endpoint with the deploy key
// and persists the returned credentials.
func (a *App) registerWithDeployKey(serverURL, deployKey string) (P2POnboardingResult, error) {
	hostname, _ := os.Hostname()
	payload, _ := json.Marshal(map[string]string{
		"hostname":  hostname,
		"deployKey": deployKey,
	})
	endpoint := strings.TrimRight(strings.TrimSpace(serverURL), "/") + "/api/agent-install/register"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return P2POnboardingResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deployKey)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return P2POnboardingResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return P2POnboardingResult{}, fmt.Errorf("registro falhou HTTP %d", resp.StatusCode)
	}
	var body struct {
		AgentID   string `json:"agentId"`
		AuthToken string `json:"authToken"`
		ApiScheme string `json:"apiScheme"`
		ApiServer string `json:"apiServer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return P2POnboardingResult{}, err
	}

	// Persist received credentials atomically.
	inst, path, _ := loadInstallerConfig()
	inst.ApiScheme = strings.TrimSpace(body.ApiScheme)
	inst.ApiServer = strings.TrimSpace(body.ApiServer)
	inst.AuthToken = strings.TrimSpace(body.AuthToken)
	inst.AgentID = strings.TrimSpace(body.AgentID)
	inst.APIKey = "" // scrub deploy key after use
	if _, err := persistInstallerConfig(path, inst); err != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao persistir credenciais: %w", err)
	}
	a.logs.append("[onboarding] credenciais persistidas agentId=" + inst.AgentID)
	return P2POnboardingResult{
		AgentID:    inst.AgentID,
		Registered: true,
		Message:    "configurado via onboarding P2P",
	}, nil
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
		http.Error(w, "auto-provisioning disabled", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	agentID := s.agentID
	s.mu.RUnlock()

	// Buscar token de provisionamento temporário na API.
	deployKey, expiresAt, err := s.app.requestProvisioningToken(r.Context())
	if err != nil {
		s.app.logs.append("[onboarding] falha ao obter provisioning token: " + err.Error())
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
	endpoint := scheme + "://" + strings.TrimSpace(inst.ApiServer) + "/api/agent-auth/me/zero-touch/deploy-token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("erro ao construir request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(inst.AuthToken))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
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
