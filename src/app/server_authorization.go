package app

// server_authorization.go — estado único de "provisionado, mas ainda sem
// liberação do servidor" (2026-11-17).
//
// ── Problema ───────────────────────────────────────────────────────────────
// Depois do registro zero-touch o agente já tem token/agentId e o servidor
// aceita /me/configuration e o heartbeat, mas TODOS os endpoints de conteúdo
// respondem 403 {"error":"Agent registration is pending (zero-touch)."}
// (AgentAuthController.GetAgentOrBlockAsync com allowPending=false) até um
// admin aprovar o agente.
//
// O agente só sabia disso pelo campo zeroTouchPending de /me/configuration, e
// apenas enquanto o processo que roda o sync estava no modo "aguardando
// aprovação". Nas janelas em que esse estado não estava marcado (após a
// aprovação, antes do próximo fetch, após restart, ou na UI companion que
// ainda não recebeu o snapshot), as telas caíam no caminho de erro e
// exibiam o 403 CRU — prints do relato:
//   • Loja:    app-store (Winget) retornou HTTP 403 Forbidden: {...}
//   • Suporte: Erro ao carregar chamados: ... ipc request support:tickets ...
//   • KB:      Erro ao carregar base de conhecimento.
//
// ── Regra ──────────────────────────────────────────────────────────────────
// O agente está "sem liberação do servidor" quando:
//   1. já está provisionado (tem token + servidor), E
//   2. o servidor respondeu que ele NÃO está autorizado:
//        - zeroTouchPending=true em /me/configuration (fonte primária), ou
//        - qualquer endpoint autenticado devolveu 403 com o corpo
//          "Agent registration is pending (zero-touch)." (detector por
//          assinatura, usado quando o flag ainda não chegou), ou
//   3. o servidor está inalcançável (request/erro de transporte) e o agente
//      NUNCA teve acesso autorizado nesta instalação — o caso de "acabou de
//      receber o token".
// Um fetch bem-sucedido do /me/configuration (zeroTouchPending ausente ou
// false) LIMPA o estado e marca a instalação como já autorizada, então um erro
// furtivo posterior não reinstala a tela de espera à toa.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

const (
	// onboardPendingReasonApproval: o servidor respondeu que o agente aguarda
	// aprovação/liberação (zero-touch).
	onboardPendingReasonApproval = "awaiting-approval"
	// onboardPendingReasonNotProvisioned: ainda sem credenciais.
	onboardingPendingReasonNotProvisioned = "not-provisioned"
	// onboardingPendingReasonServerUnreachable: provisionado, nunca autorizado
	// e o servidor não respondeu.
	onboardingPendingReasonServerUnreachable = "server-unreachable"

	// onboardingApprovalMessage é a mensagem única usada no overlay e nas telas
	// de conteúdo. Mantida em sincronia com provisioning.approval.message da UI.
	onboardingApprovalMessage = "Dispositivo provisionado, aguardando aprovacao da equipe de TI para integracao com o servidor."

	// pendingAuthSignature é a frase que o servidor devolve no 403 de agente
	// pendente (AgentAuthController.GetAgentOrBlockAsync). É o detector de
	// fallback quando o flag zeroTouchPending não chegou ao agente.
	pendingAuthSignature = "registration is pending"

	// serverAuthConfirmTTL é o intervalo mínimo entre duas confirmações de
	// /me/configuration disparadas por um 403 (evita bater no servidor a cada
	// tela aberta quando a pendência já é conhecida).
	serverAuthConfirmTTL = 60 * time.Second
)

// serverAuthTracker guarda a evidência de autorização do servidor para esta
// instalação. Vive por processo; não é persistido (um restart reavalia em
// poucos segundos e a UI cobre a janela com o estado salvo do core).
type serverAuthTracker struct {
	// pending: 1 = aguardando liberação do servidor.
	pending atomic.Bool
	// verifiedOnce: 1 = o servidor já confirmou acesso (zeroTouchPending=false).
	// Impede que uma queda de rede posterior reative a tela de espera.
	verifiedOnce atomic.Bool

	// mu protege os campos de diagnóstico/razão. A razão NÃO é um segundo flag
	// atômico: ter dois relógios (pending + unreachable) foi o que fazia o
	// estado ficar preso em "aguardando aprovação" mesmo com o servidor fora.
	mu            sync.Mutex
	detectedAt    string
	lastReason    string
	lastConfirmAt time.Time
}

// IsServerAuthorizationPending informa à UI (binding Wails) se o agente está
// provisionado aguardando liberação do servidor. É o mesmo estado que alimenta
// GetOnboardingStatus; existe como binding separado para as telas de conteúdo
// (Loja/Suporte/Tela de espera) checarem sem depender do overlay.
func (a *App) IsServerAuthorizationPending() bool {
	if a == nil {
		return false
	}
	pending, _, _ := a.serverAuthorizationPending()
	return pending
}

// serverAuthorizationPending devolve (pending, reason, detectedAtUtc).
//
// A razão é guardada em lastReason (sob mutex) em vez de derivada de um
// segundo flag atômico: assim ela sempre reflete a EVIDÊNCIA mais recente e
// não há dois relógios para manter coerentes.
func (a *App) serverAuthorizationPending() (bool, string, string) {
	if a == nil {
		return false, "", ""
	}
	t := &a.serverAuth
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.pending.Load() {
		return false, "", ""
	}
	reason := strings.TrimSpace(t.lastReason)
	if reason == "" {
		reason = onboardPendingReasonApproval
	}
	return true, reason, t.detectedAt
}

// markServerAuthorizationPending registra que o servidor negou acesso ao
// agente provisionado. Idempotente quanto a log/evento (só a PRIMEIRA
// transição para pendente loga e emite), mas sempre atualiza a razão: um
// agente já pendente que perde a rede precisa passar a reportar
// "server-unreachable" em vez de continuar dizendo "aguardando aprovação".
func (a *App) markServerAuthorizationPending(reason string) {
	if a == nil {
		return
	}
	t := &a.serverAuth
	reason = strings.TrimSpace(reason)
	t.mu.Lock()
	already := t.pending.Load()
	if !already {
		t.detectedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// A razão acompanha a evidência MAIS RECENTE. Sem isto, um 403 (que só
	// prova "não autorizado") apagava o "servidor inalcançável" e vice-versa.
	t.lastReason = reason
	t.mu.Unlock()
	if already {
		return
	}
	t.pending.Store(true)
	msg := "[onboarding] agente provisionado, mas o servidor ainda nao liberou o acesso (aguardando autorizacao da equipe de TI)"
	if reason == onboardingPendingReasonServerUnreachable {
		msg = "[onboarding] agente provisionado e ainda sem liberacao do servidor; servidor inalcancavel — aguardando"
	}
	a.Logs.Append(msg)
	// Assinatura por variáveis (igual a sync.go) para o payload chegar
	// estruturado no frontend.
	a.EmitEvent("agent:onboarding", "reason", "zero-touch-pending", "mode", "awaiting-approval")
}

// clearServerAuthorizationPending marca que o servidor AUTORIZOU o agente.
// Devolve true quando havia pendência (transição para notificar a UI).
func (a *App) clearServerAuthorizationPending() bool {
	if a == nil {
		return false
	}
	t := &a.serverAuth
	t.mu.Lock()
	was := t.pending.Load()
	t.detectedAt = ""
	t.lastReason = ""
	t.mu.Unlock()
	if !was {
		return false
	}
	t.pending.Store(false)
	a.Logs.Append("[onboarding] liberacao do servidor confirmada; acesso autorizado para o agente")
	a.EmitEvent("agent:onboarding", "reason", "zero-touch-approved", "mode", "normal")
	return true
}

// serverAuthorizationConfirmed é chamado quando /me/configuration respondeu
// com sucesso (payload efetivo do agente).
func (a *App) serverAuthorizationConfirmed() {
	if a == nil {
		return
	}
	a.serverAuth.verifiedOnce.Store(true)
	a.clearServerAuthorizationPending()
}

// noteServerAuthorizationRefusal interpreta uma resposta HTTP de um endpoint
// autenticado do agente. Só age quando há evidência de que a recusa é por
// falta de aprovação (404/403 de agente pendente) ou que o agente está
// provisionado, nunca autorizado e não conseguiu falar com o servidor.
//
// A detecção por assinatura é o que fecha a janela em que o flag
// zeroTouchPending ainda não chegou: a própria operação que falhou já prova a
// pendência, sem custo extra de rede.
func (a *App) noteServerAuthorizationRefusal(resp *http.Response, body []byte, err error) {
	if a == nil {
		return
	}
	if err != nil {
		// Falha de transporte: só vira "aguardando liberação" quando o agente
		// tem MESMO credenciais (senão o estado correto é "aguardando
		// provisionamento") e nunca foi autorizado — depois da primeira
		// liberação, rede fora é só offline (o aviso de offline já existe).
		//
		// A chamada é feita TAMBÉM quando já está pendente: a razão precisa
		// acompanhar a evidência (ver markServerAuthorizationPending).
		if !a.serverAuth.verifiedOnce.Load() && !isHTTPStatusError(err) && isConnectionError(err) && hasServerCredentials() {
			a.markServerAuthorizationPending(onboardingPendingReasonServerUnreachable)
		}
		return
	}
	if resp == nil {
		return
	}
	if resp.StatusCode != http.StatusForbidden {
		// Só 403 é o gate do zero-touch. 401 é credencial inválida/revogada —
		// outro diagnóstico (e o agente tem fluxo próprio para isso); 2xx/404/5xx
		// também não limpam: quem limpa é só a confirmação explícita do
		// /me/configuration (serverAuthorizationConfirmed).
		return
	}
	if !strings.Contains(strings.ToLower(string(body)), pendingAuthSignature) {
		return
	}
	a.markServerAuthorizationPending(onboardPendingReasonApproval)
	a.confirmServerAuthorizationAsync()
}

// confirmServerAuthorizationAsync reconsulta /me/configuration (fonte primária
// do zeroTouchPending) no máximo uma vez por serverAuthConfirmTTL. Serve para a
// UI sair da tela de espera SOZINHA logo após a aprovação, sem esperar o
// próximo ciclo de sync.
func (a *App) confirmServerAuthorizationAsync() {
	if a == nil || a.AgentConfigSvc == nil {
		return
	}
	t := &a.serverAuth
	t.mu.Lock()
	if !t.lastConfirmAt.IsZero() && time.Since(t.lastConfirmAt) < serverAuthConfirmTTL {
		t.mu.Unlock()
		return
	}
	t.lastConfirmAt = time.Now()
	t.mu.Unlock()

	// contextOrBackground: a.ctx é nil em processos ainda não inicializados (e
	// nos testes). Passar nil a http.NewRequestWithContext PANICA — o pânico é
	// recuperado pelo safeGo (não derruba o processo), mas a confirmação se
	// perde e o log enche de stack trace inútil. Melhor não pânica.
	a.safeGo(func() {
		_ = a.refreshAgentConfiguration(a.contextOrBackground())
	})
}

// ── Classificação de erro de transporte ─────────────────────────────────────

// isConnectionError identifica falha de rede/transporte (DNS, conexão
// recusada, timeout, TLS) em vez de erro de protocolo/negócio.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// Só expressões que são INEQUIVOCAMENTE de transporte. Palavras genéricas
	// ("timeout", "eof", "conexão") eram um falso positivo fácil: uma mensagem
	// de negócio que apenas CITASSE a palavra ligava a tela de espera de
	// liberação em cima de um erro que não tinha nada a ver com rede.
	for _, needle := range []string{
		"connection refused",
		"no such host",
		"i/o timeout",
		"context deadline exceeded",
		"deadline exceeded (client.timeout",
		"connection reset by peer",
		"tls handshake",
		"network is unreachable",
		"no route to host",
		"dial tcp",
		"unexpected eof",
		"server misbehaving",
		"certificate is not valid",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// isHTTPStatusError evita classificar como "sem rede" um erro que já é o status
// HTTP (ex.: erros sintéticos com "HTTP 503" em testes).
func isHTTPStatusError(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "HTTP ")
}

// hasServerCredentials diz se o agente já está PROVISIONADO (token + servidor
// persistidos). É a variável que separa "aguardando provisionamento" de
// "aguardando liberação". Existe como função para os testes fixarem o estado.
var hasServerCredentials = isAgentConfigured

// probeServerAuthorization faz um GET curto em /me/configuration só para
// atualizar o estado de autorização (usado no boot e no pós-bootstrap, quando
// nenhuma tela pediu dados ainda). Erros são silenciosos: o estado já é
// reconciliado pelo sync normal.
func (a *App) probeServerAuthorization() {
	if a == nil {
		return
	}
	cfg := a.GetDebugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		// Sem credenciais: nenhum estado de "aguardando liberação" (a UI usa
		// "aguardando provisionamento").
		return
	}
	scheme := strings.ToLower(strings.TrimSpace(cfg.ApiScheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	endpoint := scheme + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/v1/agent-auth/me/configuration"
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return
	}
	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		a.noteServerAuthorizationRefusal(nil, nil, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		pending, hasFlag := agentconfig.ParseZeroTouchPending(body)
		if hasFlag && pending {
			a.markServerAuthorizationPending(onboardPendingReasonApproval)
			return
		}
		a.serverAuthorizationConfirmed()
		return
	}
	a.noteServerAuthorizationRefusal(resp, body, nil)
}

// contextOrBackground devolve o contexto do App (nil-safe).
func (a *App) contextOrBackground() context.Context {
	if a != nil && a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
