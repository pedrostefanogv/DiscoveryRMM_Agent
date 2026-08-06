package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/p2p"
)

const (
	onboardingDeployKeyTTL     = 30 * time.Minute
	onboardingRetryBase        = 30 * time.Second
	onboardingRetryMax         = 10 * time.Minute
	onboardingMaxAttempts      = 0 // 0 = ilimitado enquanto não houver credenciais
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
	ClientID  string
	SiteID    string
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
		return false, fmt.Errorf("app indisponível")
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
		a.logs.append("[zero-touch] agente já configurado, loop de zero-touch config registration não iniciado")
		return
	}
	if !a.zeroTouchConfigRegistrationAllowed() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	state := &p2pOnboardingState{}
	a.logs.append("[zero-touch] agente genérico detectado: aguardando Zero-Touch Config Registration")

	tryAttempt := func(reason string) bool {
		if isAgentConfigured() {
			a.logs.append("[zero-touch] configuração recebida com sucesso, loop encerrado")
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
			a.logs.append("[zero-touch] configuração recebida com sucesso, loop encerrado")
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
		a.logs.append("[zero-touch] falha na tentativa imediata após peer novo " + peerID + ": " + err.Error())
	} else if attempted {
		a.logs.append("[zero-touch] tentativa imediata executada após descobrir peer=" + peerID)
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
		return fmt.Errorf("coordinator P2P indisponível")
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
	return fmt.Errorf("nenhum peer forneceu configuração válida")
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

// validateServerURL delega para p2p.ValidateServerURL.
func validateServerURL(rawURL string) error {
	return p2p.ValidateServerURL(rawURL)
}

// registerWithDeployKey calls the server registration endpoint with the deploy key
// and persists the returned credentials.
func (a *App) registerWithDeployKey(serverURL, deployKey string) (P2POnboardingResult, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if err := validateServerURL(serverURL); err != nil {
		return P2POnboardingResult{}, err
	}
	hostname, _ := os.Hostname()
	payloadMap := map[string]any{
		"cmd":          "CreateAgent",
		"name":         hostname,
		"macAddress":   nil,
		"departmentId": nil,
		"notes":        "Provisionado via zero-touch P2P",
	}

	// Fingerprint de hardware (Recuperação de Dispositivos): TPM EK + SMBIOS UUID.
	if a.hardwareIDSvc != nil {
		hw := a.hardwareIDSvc.Get()
		if strings.TrimSpace(hw.TPMEK) != "" {
			payloadMap["tpmEkHash"] = strings.TrimSpace(hw.TPMEK)
		}
		if strings.TrimSpace(hw.SMBIOSUUID) != "" {
			payloadMap["smbiosUuid"] = strings.TrimSpace(hw.SMBIOSUUID)
		}
	}

	payload, _ := json.Marshal(payloadMap)

	// Tenta o scheme informado e, em seguida, o alternativo (https<->http).
	// Isso torna o registro resiliente quando o peer emite a oferta com um
	// scheme (ex.: http por apiInsecure) mas o servidor aceita o outro.
	parsed, _ := url.Parse(serverURL)
	schemes := []string{parsed.Scheme}
	switch parsed.Scheme {
	case "https":
		schemes = append(schemes, "http")
	case "http":
		schemes = append(schemes, "https")
	}

	var lastErr error
	for _, candidateScheme := range schemes {
		candidateURL := candidateScheme + "://" + parsed.Host
		endpoint := candidateURL + "/api/v1/agent-register"
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deployKey)

		resp, err := tlsutil.NewHTTPClient(20 * time.Second).Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("falha ao ler resposta de registro: %w", readErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			preview := strings.TrimSpace(string(body))
			if len(preview) > 240 {
				preview = preview[:240] + "..."
			}
			lastErr = fmt.Errorf("registro falhou HTTP %d: %s", resp.StatusCode, preview)
			continue
		}

		credentials, err := parseZeroTouchRegisterResponse(body, candidateURL)
		if err != nil {
			return P2POnboardingResult{}, err
		}
		return a.persistZeroTouchCredentials(credentials)
	}
	return P2POnboardingResult{}, lastErr
}

// persistZeroTouchCredentials persiste as credenciais retornadas pelo servidor
// no config.json e aplica a conexão em runtime.
func (a *App) persistZeroTouchCredentials(credentials zeroTouchRegisterCredentials) (P2POnboardingResult, error) {
	inst, path, err := loadInstallerConfigForZeroTouchPersist()
	if err != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao carregar config para persistencia zero-touch: %w", err)
	}
	inst.ApiScheme = strings.TrimSpace(credentials.ApiScheme)
	inst.ApiServer = strings.TrimSpace(credentials.ApiServer)
	if strings.ToLower(strings.TrimSpace(credentials.ApiScheme)) == "http" {
		v := true
		inst.ApiInsecure = &v
	}
	inst.AuthToken = strings.TrimSpace(credentials.AuthToken)
	inst.AgentID = strings.TrimSpace(credentials.AgentID)
	if strings.TrimSpace(credentials.ClientID) != "" {
		inst.ClientID = strings.TrimSpace(credentials.ClientID)
	}
	if strings.TrimSpace(credentials.SiteID) != "" {
		inst.SiteID = strings.TrimSpace(credentials.SiteID)
	}
	// Deploy key é temporário (TTL 30min). Após registro bem-sucedido com
	// authToken + agentId definitivos, o deploy token é removido do config,
	// alinhado com BootstrapAgentCredentialsFromInstallerConfig.
	inst.APIKey = ""

	if strings.TrimSpace(inst.ApiServer) == "" ||
		strings.TrimSpace(inst.AuthToken) == "" || strings.TrimSpace(inst.AgentID) == "" {
		return P2POnboardingResult{}, fmt.Errorf("credenciais incompletas apos registro zero-touch")
	}

	writePath, err := persistInstallerConfig(path, inst)
	if err != nil {
		return P2POnboardingResult{}, fmt.Errorf("falha ao persistir credenciais: %w", err)
	}
	// Limpa arquivo de origem e override (mesmo padrão do installer bootstrap).
	if writePath != path {
		if err := debug.ScrubInstallerConfigSource(path, inst); err != nil {
			log.Printf("[zero-touch] aviso: falha ao limpar deploy token no config de origem: %v", err)
		}
	}
	if overridePath := findInstallerOverridePath(); overridePath != "" && overridePath != path {
		if err := debug.ScrubInstallerConfigSource(overridePath, inst); err != nil {
			log.Printf("[zero-touch] aviso: falha ao limpar deploy token no override: %v", err)
		}
	}

	a.applyZeroTouchRuntimeConnection(inst)
	if !isAgentConfigured() {
		return P2POnboardingResult{}, fmt.Errorf("credenciais persistidas, mas agente ainda não aparece provisionado")
	}

	tokenPreview := inst.AuthToken
	if len(tokenPreview) > 12 {
		tokenPreview = tokenPreview[:12] + "..."
	}
	a.logs.append(fmt.Sprintf("[zero-touch] credenciais persistidas em %s agentId=%s clientId=%s siteId=%s apiServer=%s authToken=%s",
		strings.TrimSpace(writePath), inst.AgentID, inst.ClientID, inst.SiteID, inst.ApiServer, tokenPreview))
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

// parseZeroTouchRegisterResponse delega para p2p.ParseZeroTouchRegisterResponse.
func parseZeroTouchRegisterResponse(body []byte, fallbackServerURL string) (zeroTouchRegisterCredentials, error) {
	creds, err := p2p.ParseZeroTouchRegisterResponse(body, fallbackServerURL)
	if err != nil {
		return zeroTouchRegisterCredentials{}, err
	}
	return zeroTouchRegisterCredentials{
		AuthToken: creds.AuthToken,
		AgentID:   creds.AgentID,
		ApiScheme: creds.ApiScheme,
		ApiServer: creds.ApiServer,
		ClientID:  creds.ClientID,
		SiteID:    creds.SiteID,
	}, nil
}

func (a *App) applyZeroTouchRuntimeConnection(inst InstallerConfig) {
	if a == nil {
		return
	}
	if a.debugSvc != nil {
		a.debugSvc.ApplyRuntimeConnectionConfig(inst.APIScheme(), inst.ApiServer, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)
	}

	if a.agentConn != nil {
		a.agentConn.Reload()
	}

	// Após zero-touch, reinicia o provider P2P com o clientId recém-obtido.
	// Isso acelera a entrada na malha correta sem esperar o próximo ciclo.
	if a.p2pCoord != nil {
		agentCfg := a.GetAgentConfiguration()
		if strings.TrimSpace(agentCfg.ClientID) != "" {
			a.p2pCoord.RestartProvider()
			a.logs.append(fmt.Sprintf("[zero-touch] coordinator P2P reiniciado com clientId=%s", agentCfg.ClientID))
		}
	}
}

// computeOnboardingSignature delega para p2p.ComputeOnboardingSignature.
func computeOnboardingSignature(sourceAgent, serverURL, deployKey, expiresAt, nonce string) string {
	return p2p.ComputeOnboardingSignature(sourceAgent, serverURL, deployKey, expiresAt, nonce)
}

// BuildOnboardingOffer delega para p2p.BuildOnboardingOffer.
func BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (P2POnboardingRequest, error) {
	offer, err := p2p.BuildOnboardingOffer(sourceAgentID, serverURL, deployKey, ttl)
	if err != nil {
		return P2POnboardingRequest{}, err
	}
	return P2POnboardingRequest(offer), nil
}

// handleP2POnboard is the HTTP handler for GET/PUT /p2p/config/onboard.
//
//	GET  → returns a signed offer when this agent is already configured (for unconfigured peers pulling).
//	PUT  → receives an offer pushed from another peer.
