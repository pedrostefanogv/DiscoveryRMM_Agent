package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/platform"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

const (
	mtlsEnrollEndpoint     = "/api/v1/agent-auth/me/mtls/enroll"
	mtlsEnrollTimeout      = 15 * time.Second
	mtlsRenewCheckInterval = 6 * time.Hour
	// mtlsRetryMinInterval é o primeiro intervalo do backoff de retry (dobra
	// x4 até mtlsRenewCheckInterval) quando o enrollment falha.
	mtlsRetryMinInterval = time.Minute
)

// errMtlsNotProvisioned indica agente sem agentId/authToken/apiServer. Não é
// falha definitiva: o provisionamento pode chegar segundos depois do startup
// (zero-touch/installer) e o loop deve tentar de novo em pouco tempo.
var errMtlsNotProvisioned = errors.New("agente ainda nao provisionado (sem agentId/authToken/apiServer)")

type mtlsEnrollRequest struct {
	CsrPem string `json:"csrPem"`
}

type mtlsEnrollResponse struct {
	CertificatePem   string `json:"certificatePem"`
	CaCertificatePem string `json:"caCertificatePem"`
	SerialNumber     string `json:"serialNumber"`
	ExpiresAtUtc     string `json:"expiresAtUtc"`
}

// LoadCachedAgentMtlsIdentity carrega do disco a identidade mTLS existente
// (sem rede) para que o Agent e o P2P já subam com mTLS ativo no startup.
func (a *App) LoadCachedAgentMtlsIdentity() {
	if a == nil {
		return
	}
	debugCfg := a.getDebugConfigInternal()
	agentID := strings.TrimSpace(debugCfg.AgentID)
	if agentID == "" {
		return
	}
	mtlsDir := filepath.Join(a.GetDataDir(), "mtls")
	id, err := tlsutil.LoadMtlsIdentity(mtlsDir)
	if err != nil {
		return
	}
	if !strings.EqualFold(id.AgentID, agentID) {
		return
	}
	now := time.Now()
	if id.Leaf == nil || now.After(id.Leaf.NotAfter) || now.Before(id.Leaf.NotBefore) {
		return
	}
	if a.P2PCoord != nil {
		a.P2PCoord.SetMtlsIdentity(id)
	}
}

// StartMtlsEnrollmentLoop garante a identidade mTLS no startup (quando
// provisionado) e verifica renovação periodicamente.
//
// Falha NÃO volta direto para o ciclo de 6h: sem certificado o agente perde
// toda a API HTTP (401 MTLS_CERT_REQUIRED), então uma falha transitória no
// startup (API fora, rede, provisionamento tardio) não pode custar 6h de
// cegueira. O retry começa em 1 min e cresce x4 até o teto de 6h — e zera
// assim que o enrollment tem sucesso.
func (a *App) StartMtlsEnrollmentLoop(ctx context.Context) {
	if a == nil {
		return
	}
	ticker := time.NewTicker(mtlsRenewCheckInterval)
	defer ticker.Stop()

	retry := mtlsRetryMinInterval
	if err := a.EnsureAgentMtlsIdentity(ctx); err != nil {
		a.Logs.Append("[mtls] enrollment inicial adiado: " + err.Error())
		// O primeiro retry não pode esperar o ciclo de 6h.
		ticker.Reset(retry)
		retry = nextMtlsRetry(retry)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.EnsureAgentMtlsIdentity(ctx); err == nil {
				retry = mtlsRetryMinInterval
				ticker.Reset(mtlsRenewCheckInterval)
				continue
			} else {
				a.Logs.Append("[mtls] enrollment falhou: " + err.Error())
			}
			ticker.Reset(retry)
			retry = nextMtlsRetry(retry)
		}
	}
}

// nextMtlsRetry cresce o intervalo de retry x4 até o teto de renovação.
func nextMtlsRetry(retry time.Duration) time.Duration {
	next := retry * 4
	if next > mtlsRenewCheckInterval {
		return mtlsRenewCheckInterval
	}
	return next
}

// EnsureAgentMtlsIdentity carrega (ou emite/renova via API) o certificado mTLS
// assinado pela CA interna da DiscoveryRMM, aplicando-o no tlsutil e no P2P.
func (a *App) EnsureAgentMtlsIdentity(ctx context.Context) error {
	if a == nil {
		return nil
	}
	// getDebugConfigInternal (e NÃO GetDebugConfig): o acessor público redige o
	// AuthToken (redactDebugConfig), e o enrollment exige o token do agente.
	// Usar o acessor redigido fazia esta função cair no early-return abaixo
	// silenciosamente — sem CSR, sem POST /mtls/enroll e sem nenhum log —
	// deixando o agente sem certificado cliente enquanto a API exigia mTLS
	// (401 MTLS_CERT_REQUIRED em toda chamada HTTP).
	debugCfg := a.getDebugConfigInternal()
	agentID := strings.TrimSpace(debugCfg.AgentID)
	authToken := strings.TrimSpace(debugCfg.AuthToken)
	apiServer := strings.TrimSpace(debugCfg.ApiServer)
	if agentID == "" || authToken == "" || apiServer == "" {
		return errMtlsNotProvisioned
	}

	mtlsDir := filepath.Join(a.GetDataDir(), "mtls")
	if id, err := tlsutil.LoadMtlsIdentity(mtlsDir); err == nil {
		if !tlsutil.NeedsMtlsRenewal(id.Leaf, time.Now()) && strings.EqualFold(id.AgentID, agentID) {
			if a.P2PCoord != nil {
				a.P2PCoord.SetMtlsIdentity(id)
			}
			return nil
		}
	}

	key, err := tlsutil.LoadOrCreateMtlsKey(mtlsDir)
	if err != nil {
		return err
	}
	_ = platform.HardenSecretFileACL(filepath.Join(mtlsDir, "agent.key"))

	csrPEM, err := tlsutil.CreateAgentCSR(key, agentID)
	if err != nil {
		return err
	}

	body, err := json.Marshal(mtlsEnrollRequest{CsrPem: csrPEM})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, mtlsEnrollTimeout)
	defer cancel()

	endpoint := debugCfg.APIScheme() + "://" + apiServer + mtlsEnrollEndpoint
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, authToken, agentID); err != nil {
		return err
	}

	client := tlsutil.NewPinnedHTTPClient(mtlsEnrollTimeout, debugCfg.ApiTlsCertHash, strings.TrimSpace(debugCfg.ApiTlsCertHash) != "")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enroll mTLS retornou HTTP %d", resp.StatusCode)
	}

	var out mtlsEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode enroll mTLS: %w", err)
	}
	if err := tlsutil.SaveMtlsCerts(mtlsDir, out.CertificatePem, out.CaCertificatePem); err != nil {
		return err
	}
	id, err := tlsutil.LoadMtlsIdentity(mtlsDir)
	if err != nil {
		return err
	}
	if a.P2PCoord != nil {
		a.P2PCoord.SetMtlsIdentity(id)
	}
	a.Logs.Append(fmt.Sprintf("[mtls] certificado mTLS ativo (serial=%s expira=%s)", out.SerialNumber, out.ExpiresAtUtc))
	return nil
}
