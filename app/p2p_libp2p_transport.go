package app

// app_p2p_libp2p_transport.go
//
// Protocolos libp2p de transporte P2P para troca de artifacts e gossip entre agents.
// Substitui os endpoints HTTP: /p2p/peers, /p2p/artifact/access,
// /p2p/artifact/{name}, /p2p/artifact/{name}/manifest e /p2p/replicate.
//
// Protocolos definidos (stream-based, JSON framing):
//   /discovery/peers/1.0.0       — gossip: retorna peers conhecidos + catálogo local
//   /artifact/access/1.0.0       — emite token de acesso para um artifact
//   /artifact/manifest/1.0.0     — retorna manifest de chunks de um artifact
//   /artifact/get/1.0.0          — transfere bytes de um chunk (range-aware)
//   /artifact/replicate/1.0.0    — notifica peer para pull de artifact (push desativado, retorna Gone)

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	protoDiscoveryPeers    = "/discovery/peers/1.0.0"
	protoArtifactAccess    = "/artifact/access/1.0.0"
	protoArtifactManifest  = "/artifact/manifest/1.0.0"
	protoArtifactGet       = "/artifact/get/1.0.0"
	protoArtifactReplicate = "/artifact/replicate/1.0.0"
	libp2pStreamTimeout    = 30 * time.Second
	libp2pTransferTimeout  = 2 * time.Minute
)

// ── Request / Response types ─────────────────────────────────────────────────

type libp2pPeersResponse struct {
	AgentID       string            `json:"agentId"`
	KnownPeers    []P2PPeerView     `json:"knownPeers"`
	Artifacts     []P2PArtifactView `json:"artifacts"`
	CatalogSource string            `json:"catalogSource"`
	UpdatedAtUTC  string            `json:"updatedAtUtc"`
}

type libp2pAccessRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
}

type libp2pManifestRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
}

type libp2pGetRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
	RangeStart   int64  `json:"rangeStart"`
	RangeEnd     int64  `json:"rangeEnd"` // -1 = até o fim
}

type libp2pGetResponse struct {
	ArtifactName string `json:"artifactName"`
	SHA256       string `json:"sha256"`
	TotalSize    int64  `json:"totalSize"`
	RangeStart   int64  `json:"rangeStart"`
	RangeEnd     int64  `json:"rangeEnd"`
	// Após este JSON header, o remetente envia exatamente (RangeEnd-RangeStart+1) bytes.
}

type libp2pReplicateRequest struct {
	ArtifactName   string `json:"artifactName"`
	ChecksumSHA256 string `json:"checksumSha256"`
	SourceAgentID  string `json:"sourceAgentId"`
}

type libp2pReplicateResponse struct {
	Gone    bool   `json:"gone"`
	Message string `json:"message"`
}

type libp2pErrorResponse struct {
	Error string `json:"error"`
}

// ── Server-side: registrar handlers no host libp2p ───────────────────────────

// RegisterP2PProtocols instala todos os stream handlers nos 5 protocolos.
// Chamado em app_p2p_libp2p.go após criar o host.
func RegisterP2PProtocols(h host.Host, coord *p2pCoordinator, transfer *p2pTransferServer) {
	h.SetStreamHandler(protoDiscoveryPeers, func(s network.Stream) {
		handleStreamPeers(s, coord, transfer)
	})
	h.SetStreamHandler(protoArtifactAccess, func(s network.Stream) {
		handleStreamArtifactAccess(s, transfer)
	})
	h.SetStreamHandler(protoArtifactManifest, func(s network.Stream) {
		handleStreamArtifactManifest(s, transfer)
	})
	h.SetStreamHandler(protoArtifactGet, func(s network.Stream) {
		handleStreamArtifactGet(s, transfer)
	})
	h.SetStreamHandler(protoArtifactReplicate, func(s network.Stream) {
		handleStreamArtifactReplicate(s)
	})
}

func handleStreamPeers(s network.Stream, coord *p2pCoordinator, transfer *p2pTransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	enc := json.NewEncoder(s)
	peers := coord.GetPeers()
	artifacts := transfer.localArtifactsSnapshot()
	transfer.mu.RLock()
	agentID := transfer.agentID
	transfer.mu.RUnlock()

	_ = enc.Encode(libp2pPeersResponse{
		AgentID:       agentID,
		KnownPeers:    peers,
		Artifacts:     artifacts,
		CatalogSource: "self",
		UpdatedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	})
}

func handleStreamArtifactAccess(s network.Stream, transfer *p2pTransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pAccessRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload invalido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact invalido"})
		return
	}
	if req.RequesterID == "" {
		req.RequesterID = "peer-anon"
	}
	access, err := transfer.BuildArtifactAccess(req.ArtifactName, req.RequesterID)
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(s).Encode(access)
}

func handleStreamArtifactManifest(s network.Stream, transfer *p2pTransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pManifestRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload invalido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact invalido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	app := transfer.app
	transfer.mu.RUnlock()

	path := filepath.Join(tempDir, req.ArtifactName)
	if _, err := os.Stat(path); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact nao encontrado"})
		return
	}
	var chunkSize int64 = defaultChunkSizeBytes
	if app != nil {
		if cfg := app.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
			chunkSize = cfg.ChunkSizeBytes
		}
	}
	artifactID := CanonicalArtifactID("", req.ArtifactName, "")
	manifest, err := buildChunkManifest(path, artifactID, chunkSize)
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao construir manifest: " + err.Error()})
		return
	}
	_ = json.NewEncoder(s).Encode(manifest)
}

func handleStreamArtifactGet(s network.Stream, transfer *p2pTransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pTransferTimeout))

	var req libp2pGetRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload invalido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact invalido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	transfer.mu.RUnlock()

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact nao encontrado"})
		return
	}
	checksum, err := computeFileSHA256(path)
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao calcular checksum"})
		return
	}

	totalSize := info.Size()
	rangeStart := req.RangeStart
	rangeEnd := req.RangeEnd
	if rangeStart < 0 {
		rangeStart = 0
	}
	if rangeEnd < 0 || rangeEnd >= totalSize {
		rangeEnd = totalSize - 1
	}
	if rangeStart > rangeEnd {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "range invalido"})
		return
	}
	chunkLen := rangeEnd - rangeStart + 1

	// Enviar header JSON primeiro.
	hdr := libp2pGetResponse{
		ArtifactName: req.ArtifactName,
		SHA256:       checksum,
		TotalSize:    totalSize,
		RangeStart:   rangeStart,
		RangeEnd:     rangeEnd,
	}
	if err := json.NewEncoder(s).Encode(hdr); err != nil {
		return
	}

	// Stream de bytes.
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if rangeStart > 0 {
		if _, err := f.Seek(rangeStart, io.SeekStart); err != nil {
			return
		}
	}
	if transfer.app != nil && transfer.app.p2pCoord != nil {
		transfer.app.p2pCoord.recordBytesServed(chunkLen)
	}
	_, _ = io.Copy(s, io.LimitReader(f, chunkLen))
}

func handleStreamArtifactReplicate(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))
	_ = json.NewDecoder(bufio.NewReader(s)).Decode(&struct{}{}) // consume request
	_ = json.NewEncoder(s).Encode(libp2pReplicateResponse{
		Gone:    true,
		Message: "modo push desabilitado: use transferencia pull sob demanda",
	})
}

// ── Client-side: funções de chamada para peers ────────────────────────────────

// libp2pFetchPeers abre um stream /discovery/peers/1.0.0 para o peer e retorna
// a resposta de gossip (catálogo + peers conhecidos).
func libp2pFetchPeers(ctx context.Context, h host.Host, peerID peer.ID) (libp2pPeersResponse, error) {
	s, err := h.NewStream(ctx, peerID, protoDiscoveryPeers)
	if err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("stream peers: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var resp libp2pPeersResponse
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&resp); err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("decode peers: %w", err)
	}
	return resp, nil
}

// libp2pRequestAccess abre um stream /artifact/access/1.0.0 e retorna o token de acesso.
func libp2pRequestAccess(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string) (P2PArtifactAccess, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactAccess)
	if err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("stream access: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	req := libp2pAccessRequest{ArtifactName: artifactName, RequesterID: requesterID}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("encode access req: %w", err)
	}
	// Lê resposta: pode ser P2PArtifactAccess ou libp2pErrorResponse.
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("read access resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return P2PArtifactAccess{}, fmt.Errorf("peer: %s", errResp.Error)
	}
	var access P2PArtifactAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("decode access resp: %w", err)
	}
	return access, nil
}

// libp2pFetchManifest abre um stream /artifact/manifest/1.0.0 e retorna o manifest de chunks.
func libp2pFetchManifest(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string) (P2PChunkManifest, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactManifest)
	if err != nil {
		return P2PChunkManifest{}, fmt.Errorf("stream manifest: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	req := libp2pManifestRequest{ArtifactName: artifactName, RequesterID: requesterID}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return P2PChunkManifest{}, fmt.Errorf("encode manifest req: %w", err)
	}
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return P2PChunkManifest{}, fmt.Errorf("read manifest resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return P2PChunkManifest{}, fmt.Errorf("peer: %s", errResp.Error)
	}
	var manifest P2PChunkManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return P2PChunkManifest{}, fmt.Errorf("decode manifest resp: %w", err)
	}
	return manifest, nil
}

// libp2pDownloadChunk abre um stream /artifact/get/1.0.0, solicita um range e
// salva os bytes no destFile. Verifica SHA256 do chunk após receber.
func libp2pDownloadChunk(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string, chunk P2PChunk, destFile string) error {
	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pTransferTimeout))

	req := libp2pGetRequest{
		ArtifactName: artifactName,
		RequesterID:  requesterID,
		RangeStart:   chunk.Offset,
		RangeEnd:     chunk.Offset + chunk.Size - 1,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return fmt.Errorf("encode get req: %w", err)
	}

	// Ler header JSON (até newline que json.Encoder/Decoder inserem).
	var hdr libp2pGetResponse
	dec := json.NewDecoder(bufio.NewReader(s))
	if err := dec.Decode(&hdr); err != nil {
		return fmt.Errorf("decode get hdr: %w", err)
	}
	// Verificar se é erro.
	if hdr.ArtifactName == "" {
		return fmt.Errorf("resposta de get invalida")
	}

	// Ler bytes do chunk do reader restante (após o JSON decoder ter consumido o header).
	chunkLen := hdr.RangeEnd - hdr.RangeStart + 1
	data, err := io.ReadAll(io.LimitReader(dec.Buffered(), chunkLen))
	if err != nil || int64(len(data)) < chunkLen {
		// Tentar ler do stream diretamente se o buffered não foi suficiente.
		remaining := chunkLen - int64(len(data))
		extra, rerr := io.ReadAll(io.LimitReader(s, remaining))
		if rerr != nil {
			return fmt.Errorf("leitura do chunk: %w", rerr)
		}
		data = append(data, extra...)
	}

	// Verificar hash do chunk.
	if !verifySHA256(data, chunk.SHA256) {
		return fmt.Errorf("chunk %d: checksum divergente", chunk.Index)
	}

	return os.WriteFile(destFile, data, 0o644)
}

// libp2pDownloadArtifact faz download simples (arquivo inteiro) via /artifact/get/1.0.0.
func libp2pDownloadArtifact(ctx context.Context, h host.Host, peerID peer.ID, access P2PArtifactAccess, destDir string) (string, int64, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return "", 0, fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pTransferTimeout))

	req := libp2pGetRequest{
		ArtifactName: access.ArtifactName,
		RequesterID:  "peer-local",
		RangeStart:   0,
		RangeEnd:     -1,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return "", 0, fmt.Errorf("encode get req: %w", err)
	}

	dec := json.NewDecoder(bufio.NewReader(s))
	var hdr libp2pGetResponse
	if err := dec.Decode(&hdr); err != nil {
		return "", 0, fmt.Errorf("decode get hdr: %w", err)
	}
	if hdr.ArtifactName == "" {
		return "", 0, fmt.Errorf("resposta de get invalida")
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", 0, err
	}
	targetPath := filepath.Join(destDir, access.ArtifactName)
	tmpPath := targetPath + ".partial"
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}

	// Drenar buffered + stream.
	reader := io.MultiReader(dec.Buffered(), s)
	size, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, closeErr
	}

	checksum, err := computeFileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	if strings.TrimSpace(access.ChecksumSHA256) != "" && !strings.EqualFold(strings.TrimSpace(access.ChecksumSHA256), checksum) {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("checksum divergente")
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	return targetPath, size, nil
}

// verifySHA256 verifica se data bate com o hex SHA256 esperado.
func verifySHA256(data []byte, expected string) bool {
	import_crypto_sha256 := sha256.Sum256(data)
	got := hex.EncodeToString(import_crypto_sha256[:])
	return strings.EqualFold(got, strings.TrimSpace(expected))
}

// ── Mapa agentID → libp2p peer.ID ────────────────────────────────────────────

// libp2pPeerRegistry mantém mapeamento agentID → peer.ID libp2p para lookup
// durante operações de transferência. Atualizado pelo notifee quando um peer é
// conectado.
// mu protege peers contra acesso concorrente de goroutines de stream handlers
// (inbound/outbound) e leituras do coordinator.
type libp2pPeerRegistry struct {
	mu    sync.RWMutex
	peers map[string]peer.ID
}

func newLibp2pPeerRegistry() *libp2pPeerRegistry {
	return &libp2pPeerRegistry{peers: make(map[string]peer.ID)}
}

// Register associa um agentID a um peer.ID libp2p. Seguro para uso concorrente.
func (r *libp2pPeerRegistry) Register(agentID string, id peer.ID) {
	if r == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(agentID))
	r.mu.Lock()
	r.peers[key] = id
	r.mu.Unlock()
}

// Lookup retorna o peer.ID para um agentID, se registrado. Seguro para uso concorrente.
func (r *libp2pPeerRegistry) Lookup(agentID string) (peer.ID, bool) {
	if r == nil {
		return "", false
	}
	key := strings.ToLower(strings.TrimSpace(agentID))
	r.mu.RLock()
	id, ok := r.peers[key]
	r.mu.RUnlock()
	return id, ok
}
