package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// onboardingDeployKeyTTL é o TTL padrão (e o TETO) da chave de deploy na oferta
// de onboarding. Alinhado a DeployTokenService.ZeroTouchTokenTtl (10 min) no
// servidor: anunciar mais validade que o token real fazia o agente falhar no
// registro com 401/403.
const onboardingDeployKeyTTL = 10 * time.Minute

// ── Handlers HTTP para /p2p/config/onboard ───────────────────────────────────

// handleP2POnboard is the HTTP handler for GET/PUT /p2p/config/onboard.
//
//	GET  → returns a signed offer when this agent is already configured (for unconfigured peers pulling).
//	PUT  → receives an offer pushed from another peer.
func (s *TransferServer) handleP2POnboard(w http.ResponseWriter, r *http.Request) {
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
func (s *TransferServer) handleOnboardOffer(w http.ResponseWriter, r *http.Request) {
	// Mesmo limite do canal libp2p: cada oferta consome um token de uso único.
	// O listener é loopback-only, mas um processo local poderia minerar tokens.
	now := time.Now()
	requester := peer.ID("http:" + requestRemoteIP(r))
	if !onboardingOfferAllowed(requester, now) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	offer, status, err := s.buildOnboardingOffer(r.Context())
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	markOnboardingOffer(requester, now)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(offer)
}

// requestRemoteIP devolve o IP de origem do request (host:porta → IP).
func requestRemoteIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// buildOnboardingOffer monta a oferta assinada de provisionamento. É a ÚNICA
// implementação da regra (feature flag, provisioning token, URL canônica, TTL):
// usada pelo handler HTTP (loopback) e pelo stream libp2p de onboarding, que é
// o caminho alcançável na LAN — o listener HTTP local é loopback-only por
// hardening (achado Z1, 2026-10-29).
func (s *TransferServer) buildOnboardingOffer(ctx context.Context) (P2POnboardingRequest, int, error) {
	if s.deps == nil {
		return P2POnboardingRequest{}, http.StatusServiceUnavailable, fmt.Errorf("unavailable")
	}
	if !s.deps.IsAgentConfigured() {
		return P2POnboardingRequest{}, http.StatusNoContent, fmt.Errorf("not configured")
	}

	// Respeitar feature flag DiscoveryEnabled (controlada pelo servidor).
	agentCfg := s.deps.GetAgentConfiguration()
	if agentCfg.DiscoveryEnabled != nil && !*agentCfg.DiscoveryEnabled {
		return P2POnboardingRequest{}, http.StatusForbidden, fmt.Errorf("zero-touch config registration disabled")
	}

	s.mu.RLock()
	agentID := s.agentID
	s.mu.RUnlock()

	// Buscar token de provisionamento temporário na API.
	deployKey, expiresAt, err := s.deps.RequestProvisioningToken(ctx)
	if err != nil {
		if s.deps != nil {
			s.deps.Log("[zero-touch] falha ao obter provisioning token: " + err.Error())
		}
		recordAutoProvisioningEvent(s.coord, agentID, "", false, "provisioning token error: "+err.Error())
		return P2POnboardingRequest{}, http.StatusServiceUnavailable, fmt.Errorf("provisioning token unavailable")
	}
	if s.deps != nil {
		s.deps.Log(fmt.Sprintf("[zero-touch] provisioning token obtido (len=%d, expiresAt=%s)", len(deployKey), expiresAt))
	}

	// Construir URL canônica (evitar inst.ServerURL legado).
	// Usa inst.APIScheme() que deriva o scheme de ApiInsecure (campo canônico),
	// pois ApiScheme é legado e não é serializado no config.json.
	inst, _, loadErr := s.deps.LoadInstallerConfig()
	var serverURL string
	if loadErr == nil && strings.TrimSpace(inst.ApiServer) != "" {
		serverURL = inst.APIScheme() + "://" + strings.TrimSpace(inst.ApiServer)
	} else if loadErr == nil && strings.TrimSpace(inst.ServerURL) != "" {
		// fallback compatível com instalações legadas
		serverURL = strings.TrimSpace(inst.ServerURL)
	}
	if serverURL == "" {
		recordAutoProvisioningEvent(s.coord, agentID, "", false, "server url missing")
		return P2POnboardingRequest{}, http.StatusInternalServerError, fmt.Errorf("server url not configured")
	}

	// Calcular TTL a partir do expiresAt retornado pela API.
	//
	// BUGFIX 2026-10-29: antes, expiresAt vazio/ilegível caía no fallback de
	// 30 min e a oferta anunciava MAIS validade que o token (10 min) — o agente
	// registrava e recebia 401/403, voltando a "preso". Agora: usa o remaining,
	// recusa token já vencido e NUNCA anuncia mais que onboardingDeployKeyTTL.
	ttl := onboardingDeployKeyTTL
	if parsed, parseErr := time.Parse(time.RFC3339, expiresAt); parseErr == nil {
		remaining := time.Until(parsed)
		if remaining <= 0 {
			recordAutoProvisioningEvent(s.coord, agentID, serverURL, false, "provisioning token expirado")
			return P2POnboardingRequest{}, http.StatusServiceUnavailable, fmt.Errorf("provisioning token expired")
		}
		ttl = remaining
	}
	if ttl > onboardingDeployKeyTTL {
		ttl = onboardingDeployKeyTTL
	}

	offer, err := s.deps.BuildOnboardingOffer(agentID, serverURL, deployKey, ttl)
	if err != nil {
		recordAutoProvisioningEvent(s.coord, agentID, serverURL, false, "build offer error: "+err.Error())
		return P2POnboardingRequest{}, http.StatusInternalServerError, fmt.Errorf("failed to build offer")
	}

	// Registrar evento de auditoria (lado provisionador).
	recordAutoProvisioningEvent(s.coord, agentID, serverURL, true, "offer emitida")
	return offer, http.StatusOK, nil
}

func (s *TransferServer) handleOnboardReceive(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.deps.IsAgentConfigured() {
		http.Error(w, "already configured", http.StatusConflict)
		return
	}
	var offer P2POnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	result, err := s.deps.ApplyOnboardingOffer(offer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// ── Auditoria ─────────────────────────────────────────────────────────────────

// recordAutoProvisioningEvent regista um evento de auditoria no coordinator
// pelo lado do provisionador (agente configurado que entregou uma oferta).
func recordAutoProvisioningEvent(c *Coordinator, sourceAgentID, serverURL string, success bool, msg string) {
	if c == nil {
		return
	}
	event := P2POnboardingAuditEvent{
		TimestampUTC:  time.Now().UTC().Format(time.RFC3339),
		SourceAgentID: sourceAgentID,
		ServerURL:     serverURL,
		Success:       success,
		Message:       msg,
	}
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
