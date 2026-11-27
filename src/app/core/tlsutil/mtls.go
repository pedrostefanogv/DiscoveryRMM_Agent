package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mtlsKeyFileName   = "agent.key"
	mtlsCertFileName  = "agent.crt"
	mtlsCaFileName    = "ca.crt"
	mtlsRenewalWindow = 7 * 24 * time.Hour
	peerBindingPrefix = "discovery-p2p-peer:"
	agentUrnPrefix    = "urn:discovery:agent:"
	clientUrnPrefix   = "urn:discovery:client:"
)

// AgentMtlsIdentity agrupa o certificado cliente emitido pela CA da
// DiscoveryRMM, a chave privada ECDSA e a raiz da CA para validar peers.
type AgentMtlsIdentity struct {
	TLSCert    tls.Certificate
	Leaf       *x509.Certificate
	CertPEM    string
	CAPool     *x509.CertPool
	PrivateKey *ecdsa.PrivateKey
	AgentID    string
	ClientID   string
}

var (
	agentClientCertMu sync.RWMutex
	agentClientCert   *tls.Certificate
)

// SetAgentClientCertificate registra o certificado cliente mTLS ativo no
// processo para uso automático por NewHTTPClient, NewWebSocketDialer e PinnedTLSConfig.
func SetAgentClientCertificate(cert *tls.Certificate) {
	agentClientCertMu.Lock()
	agentClientCert = cert
	agentClientCertMu.Unlock()
}

// GetAgentClientCertificate retorna o certificado cliente mTLS ativo, se houver.
func GetAgentClientCertificate() *tls.Certificate {
	agentClientCertMu.RLock()
	cert := agentClientCert
	agentClientCertMu.RUnlock()
	return cert
}

func attachAgentClientCert(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if cert := GetAgentClientCertificate(); cert != nil {
			return cert, nil
		}
		return &tls.Certificate{}, nil
	}
}

// LoadOrCreateMtlsKey carrega ou gera a chave ECDSA P-256 em <dir>/agent.key.
func LoadOrCreateMtlsKey(dir string) (*ecdsa.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("criar diretorio mtls: %w", err)
	}
	keyPath := filepath.Join(dir, mtlsKeyFileName)
	if raw, err := os.ReadFile(keyPath); err == nil && len(raw) > 0 {
		if block, _ := pem.Decode(raw); block != nil {
			if k, parseErr := x509.ParseECPrivateKey(block.Bytes); parseErr == nil {
				return k, nil
			}
			if pkcs8, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil {
				if ec, ok := pkcs8.(*ecdsa.PrivateKey); ok {
					return ec, nil
				}
			}
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("gerar chave ECDSA P-256: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal chave PKCS8: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("persistir chave mtls: %w", err)
	}
	return key, nil
}

// CreateAgentCSR gera um CSR PEM assinado pela chave privada do agente.
func CreateAgentCSR(key *ecdsa.PrivateKey, agentID string) (string, error) {
	if key == nil {
		return "", errors.New("chave privada nil")
	}
	cn := strings.TrimSpace(agentID)
	if cn == "" {
		cn = "agent"
	} else {
		cn = "agent:" + cn
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"DiscoveryRMM"},
		},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return "", fmt.Errorf("criar CSR: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// SaveMtlsCerts persiste o certificado do agente e o certificado da CA em <dir>.
func SaveMtlsCerts(dir, certPEM, caPEM string) error {
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(caPEM) == "" {
		return errors.New("certificado ou CA PEM vazio")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("criar diretorio mtls: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, mtlsCertFileName), []byte(certPEM), 0o600); err != nil {
		return fmt.Errorf("gravar agent.crt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, mtlsCaFileName), []byte(caPEM), 0o644); err != nil {
		return fmt.Errorf("gravar ca.crt: %w", err)
	}
	return nil
}

// LoadMtlsIdentity carrega chave, certificado do agente e CA de <dir>.
func LoadMtlsIdentity(dir string) (*AgentMtlsIdentity, error) {
	keyPath := filepath.Join(dir, mtlsKeyFileName)
	certPath := filepath.Join(dir, mtlsCertFileName)
	caPath := filepath.Join(dir, mtlsCaFileName)

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	caBytes, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}

	tlsCert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("par chave/certificado invalido: %w", err)
	}
	if len(tlsCert.Certificate) == 0 {
		return nil, errors.New("certificado vazio")
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse leaf cert: %w", err)
	}
	tlsCert.Leaf = leaf

	ecKey, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("chave privada mtls nao eh ECDSA")
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caBytes) {
		return nil, errors.New("CA PEM invalida")
	}

	agentID, clientID := extractCertIdentities(leaf)
	return &AgentMtlsIdentity{
		TLSCert:    tlsCert,
		Leaf:       leaf,
		CertPEM:    string(certBytes),
		CAPool:     caPool,
		PrivateKey: ecKey,
		AgentID:    agentID,
		ClientID:   clientID,
	}, nil
}

// NeedsMtlsRenewal retorna true quando o certificado está expirado ou vence em <= 7 dias.
func NeedsMtlsRenewal(leaf *x509.Certificate, now time.Time) bool {
	if leaf == nil {
		return true
	}
	return !now.Add(mtlsRenewalWindow).Before(leaf.NotAfter) || now.Before(leaf.NotBefore)
}

// SignPeerBinding assina o PeerID libp2p com a chave privada do certificado mTLS
// como prova de posse durante o handshake P2P.
func SignPeerBinding(key *ecdsa.PrivateKey, peerID string) (string, error) {
	if key == nil || strings.TrimSpace(peerID) == "" {
		return "", errors.New("chave ou peerID invalido")
	}
	digest := sha256.Sum256([]byte(peerBindingPrefix + strings.TrimSpace(peerID)))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyPeerCertAndBinding valida o certificado X.509 contra a CA interna,
// verifica a prova de posse do PeerID e compara o tenant (ClientID).
func VerifyPeerCertAndBinding(certPEM, sigB64, peerID string, caPool *x509.CertPool, expectedClientID string) (string, string, error) {
	if caPool == nil {
		return "", "", errors.New("CA pool indisponivel")
	}
	if strings.TrimSpace(certPEM) == "" {
		return "", "", errors.New("certificado mTLS ausente")
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", "", errors.New("certificado PEM invalido")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("parse certificado: %w", err)
	}

	opts := x509.VerifyOptions{
		Roots:       caPool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return "", "", fmt.Errorf("cadeia mTLS invalida: %w", err)
	}

	agentID, clientID := extractCertIdentities(leaf)
	if agentID == "" {
		return "", "", errors.New("certificado sem AgentId")
	}
	if exp := strings.TrimSpace(expectedClientID); exp != "" && !strings.EqualFold(clientID, exp) {
		return "", "", fmt.Errorf("tenant divergente no certificado: cert=%s esperado=%s", clientID, exp)
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) == 0 {
		return "", "", errors.New("assinatura de posse mTLS invalida")
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", "", errors.New("chave publica do certificado nao eh ECDSA")
	}
	digest := sha256.Sum256([]byte(peerBindingPrefix + strings.TrimSpace(peerID)))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return "", "", errors.New("prova de posse mTLS falhou")
	}
	return agentID, clientID, nil
}

func extractCertIdentities(cert *x509.Certificate) (string, string) {
	if cert == nil {
		return "", ""
	}
	var agentID, clientID string
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		raw := u.String()
		if strings.HasPrefix(strings.ToLower(raw), agentUrnPrefix) && agentID == "" {
			agentID = strings.TrimSpace(raw[len(agentUrnPrefix):])
		} else if strings.HasPrefix(strings.ToLower(raw), clientUrnPrefix) && clientID == "" {
			clientID = strings.TrimSpace(raw[len(clientUrnPrefix):])
		}
	}
	if agentID == "" && strings.HasPrefix(strings.ToLower(cert.Subject.CommonName), "agent:") {
		agentID = strings.TrimSpace(cert.Subject.CommonName[len("agent:"):])
	}
	if clientID == "" {
		for _, ou := range cert.Subject.OrganizationalUnit {
			if strings.HasPrefix(strings.ToLower(ou), "client:") {
				clientID = strings.TrimSpace(ou[len("client:"):])
				break
			}
		}
	}
	return agentID, clientID
}
