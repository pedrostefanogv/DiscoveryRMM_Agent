package p2p

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestP2PTokenValidation(t *testing.T) {
	srv := &TransferServer{secret: []byte("01234567890123456789012345678901")}
	token, err := srv.issueTokenLocked("artifact.bin", "peer-a", time.Now().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := srv.verifyToken("artifact.bin", "peer-a", token, time.Now()); err != nil {
		t.Fatalf("verify token should succeed: %v", err)
	}
	if err := srv.verifyToken("other.bin", "peer-a", token, time.Now()); err == nil {
		t.Fatal("verify token should fail for different artifact")
	}
	if err := srv.verifyToken("artifact.bin", "peer-b", token, time.Now()); err == nil {
		t.Fatal("verify token should fail for different peer")
	}
}

func TestSanitizeArtifactName(t *testing.T) {
	if got := SanitizeArtifactName("../danger.txt"); got != "" {
		t.Fatalf("expected empty for traversal, got %q", got)
	}
	if got := SanitizeArtifactName("folder/file.txt"); got != "" {
		t.Fatalf("expected empty for path separator, got %q", got)
	}
	if got := SanitizeArtifactName("artifact.bin"); got != "artifact.bin" {
		t.Fatalf("expected artifact.bin, got %q", got)
	}
}

func TestComputeFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	hash, err := computeFileSHA256(path)
	if err != nil {
		t.Fatalf("compute hash: %v", err)
	}
	if hash != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("unexpected sha256: %s", hash)
	}
}

func TestVerifyReplicationControl(t *testing.T) {
	srv := &TransferServer{sharedSecret: []byte("shared-secret")}
	access := P2PArtifactAccess{
		ArtifactName:   "artifact.bin",
		ChecksumSHA256: "abc123",
	}
	ts := time.Now().UTC().Unix()
	timestampValue := strconv.FormatInt(ts, 10)
	signature := signReplicationControl(srv.sharedSecret, "agent-source", access, timestampValue)
	req, err := http.NewRequest(http.MethodPost, "http://peer/p2p/replicate", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(p2pControlHeaderSourceAgent, "agent-source")
	req.Header.Set(p2pControlHeaderTimestamp, timestampValue)
	req.Header.Set(p2pControlHeaderSignature, signature)
	if err := srv.verifyReplicationControl(req, access); err != nil {
		t.Fatalf("expected control verification success, got %v", err)
	}
	req.Header.Set(p2pControlHeaderSignature, "bad-signature")
	if err := srv.verifyReplicationControl(req, access); err == nil {
		t.Fatal("expected control verification failure for invalid signature")
	}
}

// TestP2PTransferServerAcceptsLoopbackOnly garante que o servidor HTTP local de
// transferência (que aceita /p2p/config/onboard — onboarding por deploy key)
// escuta apenas em loopback. Sem esta restrição, qualquer host da mesma sub-rede
// alcança o onboarding e tenta registrar o agente em um servidor controlado
// pelo atacante (achado C4 da análise de segurança do agent).
func TestP2PTransferServerAcceptsLoopbackOnly(t *testing.T) {
	srv := NewTransferServer(nil, nil)
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "artifact.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.AuthTokenRotationMinutes = 5
	if err := srv.Start(ctx, cfg, "agent-test", tempDir, func() []P2PPeerView { return nil }); err != nil {
		t.Fatalf("start transfer server: %v", err)
	}
	defer srv.Close()

	baseURL := srv.BaseURL()
	if baseURL == "" {
		t.Fatal("baseURL vazio após Start")
	}
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("baseURL de peers deve usar loopback, obtido %q", baseURL)
	}

	addr := strings.TrimPrefix(baseURL, "http://")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/p2p/health")
	if err != nil {
		t.Fatalf("health local: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health local status = %d", resp.StatusCode)
	}

	// Nenhuma interface não-loopback deve aceitar conexão na porta do servidor.
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addrs: %v", err)
	}
	for _, iface := range ifaces {
		ipNet, ok := iface.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
			continue
		}
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(ipNet.IP.String(), portStr), 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatalf("servidor P2P aceitou conexão em %s:%s (deveria ser somente loopback)", ipNet.IP, portStr)
		}
	}
}

// TestDownloadArtifactRequiresToken valida que /p2p/artifact/{nome} exige o
// token emitido por BuildArtifactAccess (antes ele nunca era verificado na
// rota de download).
func TestDownloadArtifactRequiresToken(t *testing.T) {
	srv := NewTransferServer(nil, nil)
	tempDir := t.TempDir()
	payload := []byte("conteudo-do-artifact")
	if err := os.WriteFile(filepath.Join(tempDir, "artifact.bin"), payload, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.AuthTokenRotationMinutes = 5
	if err := srv.Start(ctx, cfg, "agent-test", tempDir, func() []P2PPeerView { return nil }); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	base := srv.BaseURL() + "/p2p/artifact/artifact.bin"

	// Sem token: 401.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base)
	if err != nil {
		t.Fatalf("get sem token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: status = %d, quer 401", resp.StatusCode)
	}

	// Com token válido: 200 e conteúdo íntegro.
	access, err := srv.BuildArtifactAccess("artifact.bin", "peer-a")
	if err != nil {
		t.Fatalf("build access: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, access.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("get com token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("com token: status = %d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload divergente: %q", got)
	}
}
