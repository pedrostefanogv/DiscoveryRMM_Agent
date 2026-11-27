package app

import (
	"bytes"
	"context"
	"encoding/json"
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
)

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
	debugCfg := a.GetDebugConfig()
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
	cert := id.TLSCert
	tlsutil.SetAgentClientCertificate(&cert)
	if a.P2PCoord != nil {
		a.P2PCoord.SetMtlsIdentity(id)
	}
}

// StartMtlsEnrollmentLoop garante a identidade mTLS no startup (quando
// provisionado) e verifica renovação periodicamente.
func (a *App) StartMtlsEnrollmentLoop(ctx context.Context) {
	if a == nil {
		return
	}
	if err := a.EnsureAgentMtlsIdentity(ctx); err != nil {
		a.Logs.Append("[mtls] enrollment inicial adiado: " + err.Error())
	}
	ticker := time.NewTicker(mtlsRenewCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.EnsureAgentMtlsIdentity(ctx); err != nil {
				a.Logs.Append("[mtls] renovacao falhou: " + err.Error())
			}
		}
	}
}

// EnsureAgentMtlsIdentity carrega (ou emite/renova via API) o certificado mTLS
// assinado pela CA interna da DiscoveryRMM, aplicando-o no tlsutil e no P2P.
func (a *App) EnsureAgentMtlsIdentity(ctx context.Context) error {
	if a == nil {
		return nil
	}
	debugCfg := a.GetDebugConfig()
	agentID := strings.TrimSpace(debugCfg.AgentID)
	authToken := strings.TrimSpace(debugCfg.AuthToken)
	apiServer := strings.TrimSpace(debugCfg.ApiServer)
	if agentID == "" || authToken == "" || apiServer == "" {
		return nil // agente ainda não provisionado
	}

	mtlsDir := filepath.Join(a.GetDataDir(), "mtls")
	if id, err := tlsutil.LoadMtlsIdentity(mtlsDir); err == nil {
		if !tlsutil.NeedsMtlsRenewal(id.Leaf, time.Now()) && strings.EqualFold(id.AgentID, agentID) {
			cert := id.TLSCert
			tlsutil.SetAgentClientCertificate(&cert)
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
	cert := id.TLSCert
	tlsutil.SetAgentClientCertificate(&cert)
	if a.P2PCoord != nil {
		a.P2PCoord.SetMtlsIdentity(id)
	}
	a.Logs.Append(fmt.Sprintf("[mtls] certificado mTLS ativo (serial=%s expira=%s)", out.SerialNumber, out.ExpiresAtUtc))
	return nil
}
