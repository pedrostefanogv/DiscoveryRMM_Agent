package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Handlers HTTP para /p2p/config/onboard ───────────────────────────────────

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
	if s.deps == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !isAgentConfigured() {
		http.Error(w, "not configured", http.StatusNoContent)
		return
	}

	// Respeitar feature flag DiscoveryEnabled (controlada pelo servidor).
	agentCfg := s.deps.GetAgentConfiguration()
	if agentCfg.DiscoveryEnabled != nil && !*agentCfg.DiscoveryEnabled {
		http.Error(w, "zero-touch config registration disabled", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	agentID := s.agentID
	s.mu.RUnlock()

	// Buscar token de provisionamento temporário na API.
	deployKey, expiresAt, err := s.deps.RequestProvisioningToken(r.Context())
	if err != nil {
		s.deps.Log("[zero-touch] falha ao obter provisioning token: " + err.Error())
		http.Error(w, "provisioning token unavailable", http.StatusServiceUnavailable)
		recordAutoProvisioningEvent(s.coord, agentID, "", false, "provisioning token error: "+err.Error())
		return
	}
	keyLen := len(deployKey)
	s.deps.Log(fmt.Sprintf("[zero-touch] provisioning token obtido (len=%d, expiresAt=%s)", keyLen, expiresAt))

	// Construir URL canônica (evitar inst.ServerURL legado).
	// Usa inst.APIScheme() que deriva o scheme de ApiInsecure (campo canônico),
	// pois ApiScheme é legado e não é serializado no config.json.
	inst, _, loadErr := loadInstallerConfig()
	var serverURL string
	if loadErr == nil && strings.TrimSpace(inst.ApiServer) != "" {
		serverURL = inst.APIScheme() + "://" + strings.TrimSpace(inst.ApiServer)
	} else if loadErr == nil && strings.TrimSpace(inst.ServerURL) != "" {
		// fallback compatível com instalações legadas
		serverURL = strings.TrimSpace(inst.ServerURL)
	}
	if serverURL == "" {
		http.Error(w, "server url not configured", http.StatusInternalServerError)
		recordAutoProvisioningEvent(s.coord, agentID, "", false, "server url missing")
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
		recordAutoProvisioningEvent(s.coord, agentID, serverURL, false, "build offer error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(offer)

	// Registrar evento de auditoria (lado provisionador).
	recordAutoProvisioningEvent(s.coord, agentID, serverURL, true, "offer emitida")
}

func (s *p2pTransferServer) handleOnboardReceive(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil {
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
func recordAutoProvisioningEvent(c *p2pCoordinator, sourceAgentID, serverURL string, success bool, msg string) {
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
