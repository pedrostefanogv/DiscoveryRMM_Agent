package p2p

// Testes das correções T1 (gossip autenticado com token de discovery), T2
// (manifest autenticado), T4 (deadline rolante com callback de progresso) e T5
// (detecção tipada de manifest stale).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// requireModeTransferServer monta um TransferServer em modo "require" com
// segredo efêmero (não precisa do listener HTTP: tokens HMAC bastam).
func requireModeTransferServer(agentID, tempDir string) *TransferServer {
	return &TransferServer{
		secret:    []byte("01234567890123456789012345678901"),
		agentID:   agentID,
		tempDir:   tempDir,
		tokenTTL:  5 * time.Minute,
		serveMode: p2pArtifactServeRequire,
	}
}

// TestPeerGossipRequiresDiscoveryToken cobre T1 no framing do protocolo:
// /discovery/peers no modo "require" aceita o token de discovery ("*") e recusa
// o stream sem token. Antes da correção o cliente nunca enviava o frame e o
// catálogo remoto vinha sempre vazio.
func TestPeerGossipRequiresDiscoveryToken(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	srv := requireModeTransferServer("agent-server", t.TempDir())
	coord := NewCoordinator(deps)

	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()

	RegisterP2PProtocols(h1, coord, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// (1) Sem token o servidor em modo require recusa (framing vazio).
	if _, err := libp2pFetchPeers(ctx, h2, h1.ID(), "agent-client", ""); err == nil {
		t.Fatal("gossip sem token deveria ser recusado no modo require")
	} else if !strings.Contains(strings.ToLower(err.Error()), "token de acesso") {
		t.Fatalf("erro deveria apontar credencial obrigatória, obtido: %v", err)
	}

	// (2) Token de discovery válido: framing correto + resposta de gossip.
	access, token, err := srv.BuildDiscoveryAccess("agent-client")
	if err != nil {
		t.Fatalf("BuildDiscoveryAccess: %v", err)
	}
	if access.ArtifactName != p2pDiscoveryTokenArtifact {
		t.Fatalf("artifact do token de discovery = %q, want %q", access.ArtifactName, p2pDiscoveryTokenArtifact)
	}
	if access.URL != "" {
		t.Fatalf("token de discovery não deve carregar URL de download, obtido %q", access.URL)
	}
	if strings.TrimSpace(token) == "" {
		t.Fatal("token de discovery vazio")
	}
	resp, err := libp2pFetchPeers(ctx, h2, h1.ID(), "agent-client", token)
	if err != nil {
		t.Fatalf("gossip autenticado falhou: %v", err)
	}
	if resp.AgentID != "agent-server" {
		t.Fatalf("agentId do gossip = %q, want agent-server", resp.AgentID)
	}

	// (3) Caminho de produção: fetchPeerGossip negocia o token sozinho.
	resp, err = fetchPeerGossip(ctx, h2, h1.ID(), "agent-client", func(msg string) { t.Log(msg) })
	if err != nil {
		t.Fatalf("fetchPeerGossip: %v", err)
	}
	if resp.AgentID != "agent-server" {
		t.Fatalf("agentId via fetchPeerGossip = %q, want agent-server", resp.AgentID)
	}
}

// TestDiscoveryTokenNotAcceptedForArtifact garante que o token de discovery não
// vira passe livre para baixar artifacts: o HMAC cobre o nome do artifact.
func TestDiscoveryTokenNotAcceptedForArtifact(t *testing.T) {
	srv := requireModeTransferServer("agent-server", t.TempDir())
	_, token, err := srv.BuildDiscoveryAccess("peer-a")
	if err != nil {
		t.Fatalf("BuildDiscoveryAccess: %v", err)
	}
	if err := srv.verifyToken("agent.bin", "peer-a", token, time.Now()); err == nil {
		t.Fatal("token de discovery não deveria validar para um artifact real")
	}
	if err := validateDiscoveryAccessToken(srv, "peer-a", token); err != nil {
		t.Fatalf("token de discovery deveria autenticar o gossip: %v", err)
	}
	if err := validateArtifactAccessToken(srv, "agent.bin", "peer-a", token); err == nil {
		t.Fatal("token de discovery não deveria autorizar /artifact/get nem /artifact/manifest")
	}
}

// TestPeerGossipLegacyPeerMarksOnce cobre a compatibilidade com peers antigos
// (T1): o peer que não conhece o artifact "*" é marcado como legacy, o erro
// aparece uma única vez e não há nova negociação a cada ciclo de gossip.
func TestPeerGossipLegacyPeerMarksOnce(t *testing.T) {
	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()

	var accessCalls atomic.Int64
	// Servidor antigo: não entende o token de discovery e ignora o frame do
	// cliente no gossip (apenas escreve a resposta).
	h1.SetStreamHandler(protoArtifactAccess, func(s network.Stream) {
		accessCalls.Add(1)
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact no encontrado"})
	})
	h1.SetStreamHandler(protoDiscoveryPeers, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(s).Encode(libp2pPeersResponse{AgentID: "agent-legacy"})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	var logs []string
	var logsMu sync.Mutex
	logf := func(msg string) {
		logsMu.Lock()
		logs = append(logs, msg)
		logsMu.Unlock()
	}

	if _, err := fetchPeerGossip(ctx, h2, h1.ID(), "agent-client", logf); err == nil {
		t.Fatal("peer legacy deveria resultar em erro (catálogo remoto desabilitado)")
	}
	// Segunda tentativa: marcado como legacy, não negocia token de novo.
	if _, err := fetchPeerGossip(ctx, h2, h1.ID(), "agent-client", logf); err == nil {
		t.Fatal("peer legacy deveria permanecer sem gossip autenticado")
	}
	if got := accessCalls.Load(); got != 1 {
		t.Fatalf("negociações de token = %d, want 1 (peer legacy deve ser marcado sem loop de erro)", got)
	}

	logsMu.Lock()
	defer logsMu.Unlock()
	legacyLogs := 0
	for _, line := range logs {
		if strings.Contains(line, "não suporta token de discovery") {
			legacyLogs++
		}
	}
	if legacyLogs != 1 {
		t.Fatalf("log de peer legacy = %d ocorrências, want 1 (logs=%v)", legacyLogs, logs)
	}
}

// TestManifestRequestCarriesToken cobre T2: o cliente do manifest precisa
// enviar o token de acesso; no modo require o servidor recusa o pedido sem ele.
func TestManifestRequestCarriesToken(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "agent.bin"), bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	// Servidor HTTP local (a rota /artifact/access valida no BuildArtifactAccess
	// que o arquivo existe) + handlers libp2p usando o mesmo TransferServer.
	srv := NewTransferServer(deps, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.AuthTokenRotationMinutes = 5
	cfg.ArtifactServeMode = p2pArtifactServeRequire
	if err := srv.Start(ctx, cfg, "agent-server", tempDir, func() []P2PPeerView { return nil }); err != nil {
		t.Fatalf("start transfer server: %v", err)
	}
	defer srv.Close()

	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()
	h1.SetStreamHandler(protoArtifactManifest, func(s network.Stream) {
		handleStreamArtifactManifest(s, srv)
	})
	h1.SetStreamHandler(protoArtifactAccess, func(s network.Stream) {
		handleStreamArtifactAccess(s, srv)
	})

	callCtx, callCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer callCancel()
	if err := h2.Connect(callCtx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// (1) Sem token: recusado no modo require.
	if _, err := libp2pFetchManifest(callCtx, h2, h1.ID(), "agent.bin", "agent-client", ""); err == nil {
		t.Fatal("manifest sem token deveria ser recusado no modo require")
	} else if !strings.Contains(strings.ToLower(err.Error()), "token de acesso") {
		t.Fatalf("erro deveria apontar credencial obrigatória, obtido: %v", err)
	}

	// (2) Requisição com token explícito carrega req.Token e devolve o manifest.
	_, token, err := srv.BuildDiscoveryAccess("agent-client")
	if err != nil {
		t.Fatalf("BuildDiscoveryAccess: %v", err)
	}
	if _, err := libp2pFetchManifest(callCtx, h2, h1.ID(), "agent.bin", "agent-client", token); err == nil {
		t.Fatal("token de discovery não deveria servir o manifest de um artifact")
	}
	access, err := srv.BuildArtifactAccess("agent.bin", "agent-client")
	if err != nil {
		t.Fatalf("BuildArtifactAccess: %v", err)
	}
	manifest, err := libp2pFetchManifest(callCtx, h2, h1.ID(), "agent.bin", "agent-client", tokenFromAccessURL(access.URL))
	if err != nil {
		t.Fatalf("manifest autenticado falhou: %v", err)
	}
	if manifest.TotalChunks == 0 || manifest.TotalSize != 4096 {
		t.Fatalf("manifest inesperado: chunks=%d size=%d", manifest.TotalChunks, manifest.TotalSize)
	}

	// (3) Caminho de produção: o coordinator negocia o token sozinho.
	coord := NewCoordinator(deps)
	coord.transferServer = srv
	manifest, err = coord.fetchManifestWithToken(callCtx, h2, h1.ID(), "agent.bin", coord.p2pRequesterID())
	if err != nil {
		t.Fatalf("fetchManifestWithToken: %v", err)
	}
	if manifest.TotalChunks == 0 {
		t.Fatal("fetchManifestWithToken devolveu manifest vazio")
	}
}

// fakeDeadlineSetter registra as renovações de deadline do stream.
type fakeDeadlineSetter struct {
	mu    sync.Mutex
	calls int
	last  time.Time
}

func (f *fakeDeadlineSetter) SetDeadline(t time.Time) error {
	f.mu.Lock()
	f.calls++
	f.last = t
	f.mu.Unlock()
	return nil
}

func (f *fakeDeadlineSetter) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestChunkPayloadReaderKeepsRollingDeadline cobre T4: com callback de progresso
// ativo o deadline rolante precisa continuar no pipeline (antes o wrapper de
// progresso descartava o rolling reader e o deadline nunca era renovado).
func TestChunkPayloadReaderKeepsRollingDeadline(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), int(3*rollingDeadlineInterval))
	setter := &fakeDeadlineSetter{}
	var progressCalls int
	var lastRead, lastTotal int64

	reader := newChunkPayloadReader(context.Background(), bytes.NewReader(payload), setter, int64(len(payload)),
		func(readSoFar, total int64) {
			progressCalls++
			lastRead = readSoFar
			lastTotal = total
		})

	written, err := io.Copy(io.Discard, reader)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("bytes lidos = %d, want %d", written, len(payload))
	}
	if setter.Calls() < 2 {
		t.Fatalf("deadline rolante não foi renovado (calls=%d): o wrapper de progresso descartou o rolling reader (regressão T4)", setter.Calls())
	}
	if progressCalls == 0 || lastRead != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Fatalf("progresso inesperado: calls=%d lido=%d total=%d", progressCalls, lastRead, lastTotal)
	}
}

// TestChunkPayloadReaderWithoutProgressStillRolls garante que o caminho sem
// callback (usado pelo download chunked) continua renovando o deadline.
func TestChunkPayloadReaderWithoutProgressStillRolls(t *testing.T) {
	payload := bytes.Repeat([]byte("b"), int(2*rollingDeadlineInterval))
	setter := &fakeDeadlineSetter{}
	reader := newChunkPayloadReader(context.Background(), bytes.NewReader(payload), setter, int64(len(payload)), nil)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if setter.Calls() < 2 {
		t.Fatalf("deadline rolante não renovado sem progresso (calls=%d)", setter.Calls())
	}
}

// TestIsStaleManifestError cobre T5: a detecção de manifest stale precisa
// reconhecer tanto a divergência de chunk quanto a de arquivo final, por
// sentinela e não apenas por substring.
func TestIsStaleManifestError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cancelado", err: errors.New("context canceled"), want: false},
		{name: "chunk sentinela", err: fmt.Errorf("chunk 0: checksum divergente (esperado=a obtido=b): %w", errChunkChecksumMismatch), want: true},
		{name: "arquivo final sentinela", err: fmt.Errorf("checksum do arquivo final divergente: %w", errFinalChecksumMismatch), want: true},
		{name: "arquivo final aninhado", err: fmt.Errorf("3/10 chunks falharam; primeiro erro: %w", fmt.Errorf("checksum do arquivo final divergente: %w", errFinalChecksumMismatch)), want: true},
		{name: "legacy chunk string", err: errors.New("chunk 0: checksum divergente (esperado=a obtido=b)"), want: true},
		{name: "legacy arquivo final string", err: errors.New("checksum do arquivo final divergente"), want: true},
	}
	for _, tc := range cases {
		if got := isStaleManifestError(tc.err); got != tc.want {
			t.Fatalf("%s: isStaleManifestError = %v, want %v (err=%v)", tc.name, got, tc.want, tc.err)
		}
	}
}
