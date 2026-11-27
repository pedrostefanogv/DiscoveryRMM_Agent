package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func testGenerateCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("caKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA", Organization: []string{"DiscoveryRMM"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate CA: %v", err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, caKey, caPEM
}

func testSignCSR(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, csrPEM, agentID, clientID string, serial int64, notAfter time.Time) string {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		t.Fatal("csrPEM invalido")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	agentURI, _ := url.Parse("urn:discovery:agent:" + agentID)
	clientURI, _ := url.Parse("urn:discovery:client:" + clientID)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject: pkix.Name{
			CommonName:         "agent:" + agentID,
			OrganizationalUnit: []string{"client:" + clientID},
			Organization:       []string{"DiscoveryRMM"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{agentURI, clientURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate leaf: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestMtlsIdentityLifecycleAndPeerBinding(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateMtlsKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMtlsKey: %v", err)
	}
	csrPEM, err := CreateAgentCSR(key, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("CreateAgentCSR: %v", err)
	}

	caCert, caKey, caPEM := testGenerateCA(t)
	certPEM := testSignCSR(t, caCert, caKey, csrPEM,
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		2, time.Now().Add(30*24*time.Hour))

	if err := SaveMtlsCerts(dir, certPEM, caPEM); err != nil {
		t.Fatalf("SaveMtlsCerts: %v", err)
	}
	id, err := LoadMtlsIdentity(dir)
	if err != nil {
		t.Fatalf("LoadMtlsIdentity: %v", err)
	}
	if NeedsMtlsRenewal(id.Leaf, time.Now()) {
		t.Fatal("certificado com 30 dias nao deve precisar de renovacao")
	}

	peerID := "12D3KooWTestPeerBinding"
	sig, err := SignPeerBinding(id.PrivateKey, peerID)
	if err != nil {
		t.Fatalf("SignPeerBinding: %v", err)
	}

	gotAgent, gotClient, err := VerifyPeerCertAndBinding(id.CertPEM, sig, peerID, id.CAPool, "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("VerifyPeerCertAndBinding valido falhou: %v", err)
	}
	if gotAgent != "11111111-1111-4111-8111-111111111111" || gotClient != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("identidades inesperadas: agent=%s client=%s", gotAgent, gotClient)
	}

	// Tenant divergente deve falhar
	if _, _, err := VerifyPeerCertAndBinding(id.CertPEM, sig, peerID, id.CAPool, "99999999-9999-4999-8999-999999999999"); err == nil {
		t.Fatal("esperava falha com tenant divergente")
	}
	// PeerID diferente (replay de assinatura) deve falhar
	if _, _, err := VerifyPeerCertAndBinding(id.CertPEM, sig, "12D3KooWOutroPeer", id.CAPool, "22222222-2222-4222-8222-222222222222"); err == nil {
		t.Fatal("esperava falha com peerID divergente")
	}

	// SetAgentClientCertificate anexa GetClientCertificate no PinnedTLSConfig
	cert := id.TLSCert
	SetAgentClientCertificate(&cert)
	t.Cleanup(func() { SetAgentClientCertificate(nil) })

	tlsCfg := PinnedTLSConfig("", false)
	if tlsCfg.GetClientCertificate == nil {
		t.Fatal("esperava GetClientCertificate preenchido")
	}
	gotCert, err := tlsCfg.GetClientCertificate(nil)
	if err != nil || len(gotCert.Certificate) == 0 {
		t.Fatalf("GetClientCertificate falhou: %v", err)
	}
}
