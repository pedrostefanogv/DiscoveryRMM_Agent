package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
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
	onboardingMaxAttempts      = 0 // 0 = ilimitado enquanto não houver credenciais
	onboardingPeerRecheckDelay = 60 * time.Second
	// 40s: a fase libp2p pode gastar até onboardingLibp2pBudget (15s) e o
	// fallback HTTP até onboardingHTTPPeerTimeout por peer — 25s não caberia e
	// cortava provisionadores lentos (achado da revisão 2026-10-29).
	onboardingAttemptTimeout = 40 * time.Second
	p2pOnboardingEndpoint    = "/p2p/config/onboard"

	// Backoff exponencial com jitter do loop de onboarding (achados Z6/Z13).
	// O primeiro retry fica entre onboardingRetryBase/2 e onboardingRetryBase e
	// cresce até onboardingRetryMax (10 min) — antes o loop usava um ticker fixo
	// de 60s, o que sincronizava todos os agentes de um mesmo site (rebanho) e
	// mantinha o operador sem diagnóstico quando nada funcionava.
	onboardingRetryBase = 30 * time.Second
	// 2 min (antes 10 min): se a config chega por outro caminho (ex.
	// triggerZeroTouchConfigRegistrationOnPeerDiscovery), o loop ficava até 10
	// min sem perceber; 2 min mantém a malha responsiva sem virar rebanho.
	onboardingRetryMax = 2 * time.Minute

	// onboardingLibp2pBudget limita APENAS a fase libp2p da tentativa. O pull de
	// onboarding precisa ser libp2p porque o listener HTTP que serve
	// /p2p/config/onboard é loopback-only (hardening C4) — inalcançável da LAN.
	// Precisa ser MAIOR que o orçamento do provisionador para emitir a oferta
	// (p2pOnboardAPIBudget=20s + handshake): se o requisitante desistir antes, o
	// provisionador já emitiu um token de uso único e ele é queimado (MaxUses=1).
	onboardingLibp2pBudget = 25 * time.Second
	// onboardingHTTPPeerTimeout é o timeout por peer do fallback HTTP.
	onboardingHTTPPeerTimeout = 10 * time.Second
	// onboardingStuckAttempts: a partir desta contagem de tentativas frustradas o
	// loop registra um diagnóstico agregado (peers conhecidos, último motivo). O
	// sintoma anterior era o agente preso em "aguardando provisionamento" sem
	// nenhuma causa exposta ao operador (Z6/Z13).
	onboardingStuckAttempts = 5
	// onboardingMaxPeerCauses limita quantas causas por peer entram no erro final
	// (frotas grandes produzem mensagens ilegíveis).
	onboardingMaxPeerCauses = 8
)

// p2pOnboardingState tracks the onboarding progress for this agent.
type p2pOnboardingState struct {
	mu            sync.Mutex
	attempts      int
	lastAttemptAt time.Time
	lastReason    string
	lastError     string
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

// zeroTouchConfigRegistrationAllowed respeita o kill-switch do servidor
// (zeroTouchEnabled, herdável server → cliente → site), a dependência de
// Descoberta de Rede e o kill-switch local em config.json.
// Quando ausente, o comportamento padrao e permitir (true).
func (a *App) zeroTouchConfigRegistrationAllowed() bool {
	if a == nil {
		return true
	}

	agentCfg := a.GetAgentConfiguration()
	if agentCfg.ZeroTouchEnabled != nil && !*agentCfg.ZeroTouchEnabled {
		a.Logs.Append("[zero-touch] zeroTouchEnabled=false na configuração efetiva: registro zero-touch desabilitado pelo servidor")
		return false
	}
	// Zero-touch depende da Descoberta de Rede: sem ela os agents não se
	// encontram para trocar a configuração.
	if agentCfg.DiscoveryEnabled != nil && !*agentCfg.DiscoveryEnabled {
		a.Logs.Append("[zero-touch] discoveryEnabled=false: registro zero-touch requer descoberta de rede ativa")
		return false
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
		a.Logs.Append("[zero-touch] autoProvisioning=false em config.json: zero-touch config registration desabilitado localmente")
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
	a.Logs.Append(fmt.Sprintf("[zero-touch] tentativa %d (%s)", attemptNumber, strings.TrimSpace(reason)))

	attemptCtx, cancel := context.WithTimeout(ctx, onboardingAttemptTimeout)
	defer cancel()
	err := a.requestOnboardingFromPeers(attemptCtx, state)

	if state != nil {
		state.mu.Lock()
		state.attempts++
		state.lastAttemptAt = time.Now().UTC()
		state.lastReason = strings.TrimSpace(reason)
		if err != nil {
			state.lastError = err.Error()
		} else {
			state.lastError = ""
		}
		state.mu.Unlock()
	}
	return true, err
}

// RunOnboardingLoop periodically requests configuration from the local P2P network
// when this agent has no server credentials ("generic agent" state).
// Exits when configured, max attempts reached, or ctx cancelled.
func (a *App) RunOnboardingLoop(ctx context.Context) {
	if isAgentConfigured() {
		a.Logs.Append("[zero-touch] agente já configurado, loop de zero-touch config registration não iniciado")
		return
	}
	if !a.zeroTouchConfigRegistrationAllowed() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	state := &p2pOnboardingState{}
	a.Logs.Append("[zero-touch] agente genérico detectado: aguardando Zero-Touch Config Registration")

	tryAttempt := func(reason string) bool {
		if isAgentConfigured() {
			a.Logs.Append("[zero-touch] configuração recebida com sucesso, loop encerrado")
			return true
		}
		state.mu.Lock()
		attempts := state.attempts
		state.mu.Unlock()
		if onboardingMaxAttempts > 0 && attempts >= onboardingMaxAttempts {
			a.Logs.Append("[zero-touch] limite de tentativas atingido, loop encerrado")
			return true
		}
		attempted, err := a.tryZeroTouchConfigRegistration(ctx, state, reason)
		if err != nil {
			a.Logs.Append("[zero-touch] falha: " + err.Error())
		} else if !attempted {
			a.Logs.Append("[zero-touch] tentativa ignorada: outra tentativa em andamento")
		}
		if isAgentConfigured() {
			a.Logs.Append("[zero-touch] configuração recebida com sucesso, loop encerrado")
			return true
		}
		return false
	}

	if tryAttempt("startup-imediato") {
		return
	}

	// Backoff exponencial com jitter: cada rodada espera um tempo crescente
	// (base*2^n, teto onboardingRetryMax) com jitter em [d/2, d] para
	// desincronizar agentes que descobriram os mesmos peers juntos. O ticker
	// fixo anterior (60s) gerava rebanho e nenhum diagnóstico de "preso"
	// (achados Z6/Z13).
	failures := 1 // a tentativa de startup já conta como falha
	for attempt := 0; ; attempt++ {
		delay := onboardingBackoffDelay(attempt, nil)
		a.Logs.Append(fmt.Sprintf("[zero-touch] próxima tentativa em %s (backoff com jitter)", delay.Round(time.Second)))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if tryAttempt(fmt.Sprintf("retry-backoff-%s", delay.Round(time.Second))) {
			return
		}

		failures++
		if failures == onboardingStuckAttempts || (failures > onboardingStuckAttempts && failures%onboardingStuckAttempts == 0) {
			a.logOnboardingStuckDiagnostics(state, failures)
		}
	}
}

// onboardingBackoffDelay devolve o atraso até a próxima tentativa do loop de
// onboarding: exponencial (onboardingRetryBase * 2^attempt) limitado a
// onboardingRetryMax, com jitter uniforme em [d/2, d].
// rnd == nil usa math/rand/v2.Float64; testes injetam um gerador determinístico.
func onboardingBackoffDelay(attempt int, rnd func() float64) time.Duration {
	if rnd == nil {
		rnd = rand.Float64
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := onboardingRetryBase
	for i := 0; i < attempt && delay < onboardingRetryMax; i++ {
		delay *= 2
	}
	if delay > onboardingRetryMax {
		delay = onboardingRetryMax
	}

	// Jitter "de baixo": mantém o primeiro retry rápido (base/2) e nunca passa
	// do teto configurado.
	jittered := time.Duration(float64(delay) * (0.5 + 0.5*rnd()))
	if minDelay := onboardingRetryBase / 2; jittered < minDelay {
		jittered = minDelay
	}
	if jittered > onboardingRetryMax {
		jittered = onboardingRetryMax
	}
	return jittered
}

// logOnboardingStuckDiagnostics registra um diagnóstico agregado quando o loop
// já falhou várias vezes seguidas: peers conhecidos, endereços tentados e o
// último motivo da falha. Sem isso o operador só via "aguardando
// provisionamento" indefinidamente (achados Z6/Z13).
func (a *App) logOnboardingStuckDiagnostics(state *p2pOnboardingState, failures int) {
	if a == nil {
		return
	}

	var peers []P2PPeerView
	if a.P2PCoord != nil {
		peers = a.P2PCoord.GetPeers()
	}

	attempts, reason, lastErr := 0, "", ""
	if state != nil {
		state.mu.Lock()
		attempts = state.attempts
		reason = state.lastReason
		lastErr = state.lastError
		state.mu.Unlock()
	}

	a.Logs.Append(fmt.Sprintf("[zero-touch] preso: %d tentativas sem configuração (peers conhecidos=%d, tentativas registradas=%d, último motivo=%q)",
		failures, len(peers), attempts, reason))
	if strings.TrimSpace(lastErr) != "" {
		a.Logs.Append("[zero-touch] preso: último erro = " + lastErr)
	}
	if len(peers) == 0 {
		a.Logs.Append("[zero-touch] preso: nenhum peer descoberto — verifique descoberta de rede (discoveryEnabled) e o kill-switch zeroTouchEnabled no servidor")
		return
	}
	a.Logs.Append("[zero-touch] preso: peers=" + describeOnboardingPeers(peers, onboardingMaxPeerCauses))
	a.Logs.Append("[zero-touch] preso: o pull de onboarding usa stream libp2p (/discovery-onboard/1.0.0); o endpoint HTTP /p2p/config/onboard é loopback-only (C4)")
}

// describeOnboardingPeers formata os peers conhecidos para diagnóstico/log,
// limitando a quantidade de entradas (frotas grandes).
func describeOnboardingPeers(peers []P2PPeerView, limit int) string {
	if len(peers) == 0 {
		return "nenhum"
	}
	if limit <= 0 {
		limit = len(peers)
	}

	parts := make([]string, 0, limit)
	for i, peer := range peers {
		if i >= limit {
			break
		}
		agentID := strings.TrimSpace(peer.AgentID)
		if agentID == "" {
			agentID = "<sem-agent-id>"
		}
		address := strings.TrimSpace(peer.Address)
		if address == "" {
			parts = append(parts, fmt.Sprintf("%s(sem-endereço)", agentID))
			continue
		}
		via := strings.TrimSpace(peer.ConnectedVia)
		if via == "" {
			parts = append(parts, fmt.Sprintf("%s@%s:%d", agentID, address, peer.Port))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s@%s:%d/%s", agentID, address, peer.Port, via))
	}
	if len(peers) > limit {
		parts = append(parts, fmt.Sprintf("... (+%d peers)", len(peers)-limit))
	}
	return strings.Join(parts, ", ")
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
		a.Logs.Append("[zero-touch] falha na tentativa imediata após peer novo " + peerID + ": " + err.Error())
	} else if attempted {
		a.Logs.Append("[zero-touch] tentativa imediata executada após descobrir peer=" + peerID)
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
		a.Logs.Append("[zero-touch] falha no recheck de 60s apos peer " + peerID + ": " + err.Error())
	} else if attempted {
		a.Logs.Append("[zero-touch] recheck de 60s executado para peers conhecidos (peer inicial=" + peerID + ")")
	}
}

// requestOnboardingFromPeers busca a oferta de onboarding nos peers conhecidos.
//
// Caminho PRIMÁRIO: stream libp2p /discovery-onboard/1.0.0. O listener HTTP que
// serve /p2p/config/onboard escuta apenas em 127.0.0.1 (hardening C4), portanto
// o pull HTTP a partir de outro host falha SEMPRE — era a causa do agente preso
// em "aguardando provisionamento" (achado Z1, 2026-10-29).
//
// Caminho SECUNDÁRIO (compatibilidade): loop HTTP no endpoint loopback, útil
// quando o provisionador roda na MESMA máquina ou atrás de um proxy que publica
// o endpoint.
//
// Todas as causas por peer são agregadas no erro final (antes ECONNREFUSED era
// descartado em silêncio e o operador só via "nenhum peer forneceu configuração
// válida" — achado Z9).
func (a *App) requestOnboardingFromPeers(ctx context.Context, state *p2pOnboardingState) error {
	if a.P2PCoord == nil {
		return fmt.Errorf("coordinator P2P indisponível")
	}
	peers := a.P2PCoord.GetPeers()
	if len(peers) == 0 {
		return fmt.Errorf("nenhum peer conhecido na rede")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	causes := make([]string, 0, len(peers)+1)

	// 1) Caminho primário: stream libp2p (alcançável na LAN).
	libp2pCtx, cancelLibp2p := context.WithTimeout(ctx, onboardingLibp2pBudget)
	libp2pOffer, libp2pErr := a.P2PCoord.FetchOnboardingOfferFromPeers(libp2pCtx)
	cancelLibp2p()
	if libp2pErr != nil {
		causes = append(causes, "libp2p: "+libp2pErr.Error())
		a.Logs.Append("[zero-touch] caminho libp2p sem oferta: " + libp2pErr.Error())
	} else {
		if _, applyErr := a.applyPeerOnboardingOffer(libp2pOffer, state, "libp2p"); applyErr == nil {
			return nil
		} else {
			causes = append(causes, fmt.Sprintf("libp2p peer=%s: %v", sourceAgentLabel(libp2pOffer), applyErr))
		}
	}

	// 2) Fallback HTTP loopback (compatibilidade), com causa explícita por peer.
	for _, peer := range peers {
		agentID := strings.TrimSpace(peer.AgentID)
		if agentID == "" {
			agentID = "<sem-agent-id>"
		}
		address := strings.TrimSpace(peer.Address)
		if address == "" || peer.Port <= 0 {
			reason := fmt.Sprintf("http peer=%s: sem endereço/porta (port=%d)", agentID, peer.Port)
			causes = append(causes, reason)
			a.Logs.Append("[zero-touch] " + reason)
			continue
		}
		endpoint := fmt.Sprintf("http://%s:%d%s", address, peer.Port, p2pOnboardingEndpoint)
		offer, err := a.fetchOnboardingOfferHTTP(ctx, endpoint)
		if err != nil {
			causes = append(causes, fmt.Sprintf("http peer=%s endpoint=%s: %v", agentID, endpoint, err))
			a.Logs.Append("[zero-touch] HTTP peer=" + agentID + " falhou em " + endpoint + ": " + err.Error())
			continue
		}
		if _, applyErr := a.applyPeerOnboardingOffer(offer, state, "http"); applyErr == nil {
			return nil
		} else {
			causes = append(causes, fmt.Sprintf("http peer=%s endpoint=%s: %v", agentID, endpoint, applyErr))
		}
	}

	err := fmt.Errorf("nenhum peer forneceu configuração válida (%d peers; %s)", len(peers), summarizeOnboardingCauses(causes))
	if state != nil {
		state.mu.Lock()
		state.lastError = err.Error()
		state.mu.Unlock()
	}
	return err
}

// fetchOnboardingOfferHTTP faz o GET /p2p/config/onboard no peer e decodifica a
// oferta, devolvendo erro EXPLÍCITO (antes ECONNREFUSED/status != 200 eram
// descartados em silêncio — achado Z9).
func (a *App) fetchOnboardingOfferHTTP(ctx context.Context, endpoint string) (P2POnboardingRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return P2POnboardingRequest{}, fmt.Errorf("request: %w", err)
	}
	client := &http.Client{Timeout: onboardingHTTPPeerTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return P2POnboardingRequest{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return P2POnboardingRequest{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}

	var offer P2POnboardingRequest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&offer); err != nil {
		return P2POnboardingRequest{}, fmt.Errorf("decode: %w", err)
	}
	return offer, nil
}

// applyPeerOnboardingOffer aplica a oferta recebida de um peer, registra o
// evento de auditoria e devolve o resultado. via identifica o caminho
// (libp2p|http) nas mensagens de log.
func (a *App) applyPeerOnboardingOffer(offer P2POnboardingRequest, state *p2pOnboardingState, via string) (P2POnboardingResult, error) {
	result, applyErr := a.applyOnboardingOffer(offer)
	sourceAgentID := strings.TrimSpace(offer.SourceAgent)

	event := P2POnboardingAuditEvent{
		TimestampUTC:  time.Now().UTC().Format(time.RFC3339),
		SourceAgentID: sourceAgentID,
		ServerURL:     strings.TrimSpace(offer.ServerURL),
		Success:       applyErr == nil,
	}
	if applyErr != nil {
		event.Message = applyErr.Error()
		a.Logs.Append("[zero-touch] oferta rejeitada via " + via + " de " + sourceAgentLabel(offer) + ": " + applyErr.Error())
	} else {
		event.Message = "configurado com sucesso via " + via
		event.TargetAgentID = result.AgentID
		a.Logs.Append("[zero-touch] configurado via " + via + " peer=" + sourceAgentLabel(offer) + " agentId=" + result.AgentID)
	}
	if state != nil {
		state.mu.Lock()
		state.audit = append(state.audit, event)
		state.configured = applyErr == nil
		state.mu.Unlock()
	}
	return result, applyErr
}

// sourceAgentLabel devolve um rótulo legível do peer que emitiu a oferta.
func sourceAgentLabel(offer P2POnboardingRequest) string {
	if id := strings.TrimSpace(offer.SourceAgent); id != "" {
		return id
	}
	return "<peer-desconhecido>"
}

// summarizeOnboardingCauses junta as causas por peer limitando o volume, para o
// erro/log continuarem legíveis em frotas grandes.
func summarizeOnboardingCauses(causes []string) string {
	if len(causes) == 0 {
		return "sem detalhes"
	}
	if len(causes) <= onboardingMaxPeerCauses {
		return strings.Join(causes, "; ")
	}
	return fmt.Sprintf("%s; ... (+%d causas)", strings.Join(causes[:onboardingMaxPeerCauses], "; "), len(causes)-onboardingMaxPeerCauses)
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
	if a.HardwareIDSvc != nil {
		hw := a.HardwareIDSvc.Get()
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
	a.Logs.Append(fmt.Sprintf("[zero-touch] credenciais persistidas em %s agentId=%s clientId=%s siteId=%s apiServer=%s authToken=%s",
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
	if a.DebugSvc != nil {
		a.DebugSvc.ApplyRuntimeConnectionConfig(inst.APIScheme(), inst.ApiServer, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)
	}

	if a.AgentConn != nil {
		a.AgentConn.Reload()
	}

	// Após zero-touch, reinicia o provider P2P com o clientId recém-obtido.
	// Isso acelera a entrada na malha correta sem esperar o próximo ciclo.
	if a.P2PCoord != nil {
		agentCfg := a.GetAgentConfiguration()
		if strings.TrimSpace(agentCfg.ClientID) != "" {
			a.P2PCoord.RestartProvider()
			a.Logs.Append(fmt.Sprintf("[zero-touch] coordinator P2P reiniciado com clientId=%s", agentCfg.ClientID))
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
