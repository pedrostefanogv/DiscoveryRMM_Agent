package app

// app_p2p_libp2p_transport.go
//
// Protocolos libp2p de transporte P2P para troca de artifacts e gossip entre agents.
// Substitui os endpoints HTTP: /p2p/peers, /p2p/artifact/access,
// /p2p/artifact/{name}, /p2p/artifact/{name}/manifest e /p2p/replicate.
//
// Protocolos definidos (stream-based, JSON framing):
//   /discovery/peers/1.0.0       â€” gossip: retorna peers conhecidos + catÃ¡logo local
//   /artifact/access/1.0.0       â€” emite token de acesso para um artifact
//   /artifact/manifest/1.0.0     â€” retorna manifest de chunks de um artifact
//   /artifact/get/1.0.0          â€” transfere bytes de um chunk (range-aware)
//   /artifact/replicate/1.0.0    â€” notifica peer para pull de artifact (push desativado, retorna Gone)

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"discovery/internal/platform"

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
	protoFetchCandidacy    = "/fetch/candidacy/1.0.0"
	protoFetchHeartbeat    = "/fetch/heartbeat/1.0.0"
	libp2pStreamTimeout        = 30 * time.Second
	libp2pTransferTimeout      = 2 * time.Minute
	libp2pCandidacyTimeout     = 10 * time.Second
	libp2pTransferTimeoutMax   = 30 * time.Minute
	libp2pManifestTimeout      = 5 * time.Minute   // cobre leitura completa do arquivo para gerar manifest
	libp2pMinTransferSpeed     = 1 * 1024 * 1024   // 1 MB/s — piso de velocidade para cálculo de timeout
	libp2pTransferTimeoutMargin = 30 * time.Second
)

// computeTransferDeadline calcula um deadline adaptativo baseado no tamanho dos dados.
// Piso: libp2pTransferTimeout (2 min). Teto: libp2pTransferTimeoutMax (30 min).
// Assume velocidade mínima de 1 MB/s mais margem fixa de 30s.
func computeTransferDeadline(dataSize int64) time.Time {
	if dataSize <= 0 {
		return time.Now().Add(libp2pTransferTimeout)
	}
	estimated := time.Duration(dataSize/libp2pMinTransferSpeed)*time.Second + libp2pTransferTimeoutMargin
	if estimated < libp2pTransferTimeout {
		estimated = libp2pTransferTimeout
	}
	if estimated > libp2pTransferTimeoutMax {
		estimated = libp2pTransferTimeoutMax
	}
	return time.Now().Add(estimated)
}

// â”€â”€ Request / Response types â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

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
	RangeEnd     int64  `json:"rangeEnd"` // -1 = atÃ© o fim
}

type libp2pGetResponse struct {
	ArtifactName string `json:"artifactName"`
	SHA256       string `json:"sha256"`
	TotalSize    int64  `json:"totalSize"`
	RangeStart   int64  `json:"rangeStart"`
	RangeEnd     int64  `json:"rangeEnd"`
	// ApÃ³s este JSON header, o remetente envia exatamente (RangeEnd-RangeStart+1) bytes.
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

// â”€â”€ Tipos do protocolo /fetch â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// libp2pCandidacyRequest Ã© enviado por um peer que quer se candidatar a fetcher.
// Reutiliza ArtifactFetchCandidate como payload.
type libp2pCandidacyRequest = ArtifactFetchCandidate

// libp2pCandidacyResponse confirma recebimento da candidatura.
type libp2pCandidacyResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// â”€â”€ Server-side: registrar handlers no host libp2p â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// RegisterP2PProtocols instala todos os stream handlers nos 7 protocolos.
// Chamado em app_p2p_libp2p.go apÃ³s criar o host.
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
	h.SetStreamHandler(protoFetchCandidacy, func(s network.Stream) {
		handleStreamFetchCandidacy(s, coord)
	})
	h.SetStreamHandler(protoFetchHeartbeat, func(s network.Stream) {
		handleStreamFetchHeartbeat(s, coord)
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
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
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
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	app := transfer.app
	transfer.mu.RUnlock()

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact não encontrado"})
		return
	}

	// Tentar cache de manifest primeiro.
	manifestDir := filepath.Join(tempDir, manifestDirName)
	if cached := loadCachedManifest(manifestDir, req.ArtifactName, path); cached != nil {
		_ = json.NewEncoder(s).Encode(cached)
		return
	}

	// Reajustar deadline: buildChunkManifest lê o arquivo inteiro.
	_ = s.SetDeadline(computeTransferDeadline(info.Size()))

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

	// Cachear manifest para reuso futuro (best-effort).
	_ = saveCachedManifest(manifestDir, req.ArtifactName, manifest)

	_ = json.NewEncoder(s).Encode(manifest)
}

func handleStreamArtifactGet(s network.Stream, transfer *p2pTransferServer) {
	defer s.Close()

	var req libp2pGetRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	transfer.mu.RUnlock()

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact não encontrado"})
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
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "range inválido"})
		return
	}
	chunkLen := rangeEnd - rangeStart + 1

	// Deadline adaptativo baseado no tamanho real a transferir.
	_ = s.SetDeadline(computeTransferDeadline(chunkLen))

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

	var coord *p2pCoordinator
	if transfer.app != nil {
		coord = transfer.app.p2pCoord
	}
	requesterID := strings.TrimSpace(req.RequesterID)
	if requesterID == "" {
		requesterID = "peer-remote"
	}

	reader := io.LimitReader(f, chunkLen)
	if coord != nil {
		reader = newProgressReader(reader, chunkLen, func(readSoFar int64) {
			coord.emitTransferProgress(p2pTransferProgress{
				ArtifactName: req.ArtifactName,
				PeerID:       requesterID,
				BytesRead:    readSoFar,
				TotalBytes:   chunkLen,
				Operation:    "serve",
				Direction:    "upload",
			})
		})
	}

	written, copyErr := io.Copy(s, reader)
	if coord != nil && written > 0 {
		coord.recordBytesServed(written)
	}
	if coord != nil {
		done := p2pTransferProgress{
			ArtifactName: req.ArtifactName,
			PeerID:       requesterID,
			BytesRead:    written,
			TotalBytes:   chunkLen,
			Operation:    "serve",
			Direction:    "upload",
			Done:         true,
		}
		if copyErr != nil {
			done.Error = copyErr.Error()
		}
		coord.emitTransferProgress(done)
	}
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

// â”€â”€ Client-side: funÃ§Ãµes de chamada para peers â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// libp2pFetchPeers abre um stream /discovery/peers/1.0.0 para o peer e retorna
// a resposta de gossip (catÃ¡logo + peers conhecidos).
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
	// LÃª resposta: pode ser P2PArtifactAccess ou libp2pErrorResponse.
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
	_ = s.SetDeadline(time.Now().Add(libp2pManifestTimeout))

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

func decodeLibp2pGetHeaderAndPayload(stream io.Reader) (libp2pGetResponse, io.Reader, error) {
	dec := json.NewDecoder(stream)
	var hdr libp2pGetResponse
	if err := dec.Decode(&hdr); err != nil {
		return libp2pGetResponse{}, nil, fmt.Errorf("decode get hdr: %w", err)
	}
	if strings.TrimSpace(hdr.ArtifactName) == "" {
		return libp2pGetResponse{}, nil, fmt.Errorf("resposta de get invalida")
	}
	if hdr.RangeEnd < hdr.RangeStart {
		return libp2pGetResponse{}, nil, fmt.Errorf("range retornado inválido")
	}
	if payloadLen := hdr.RangeEnd - hdr.RangeStart + 1; payloadLen <= 0 {
		return libp2pGetResponse{}, nil, fmt.Errorf("payload vazio")
	}

	// Mantem uma trilha unica de leitura: bytes que ja estao no buffer do decoder
	// + bytes restantes no mesmo reader subjacente.
	payloadReader := bufio.NewReader(io.MultiReader(dec.Buffered(), stream))
	// /artifact/get envia o header via json.Encoder.Encode, que adiciona um '\n'
	// antes dos bytes binarios do payload.
	if first, err := payloadReader.Peek(1); err == nil && len(first) == 1 && first[0] == '\n' {
		_, _ = payloadReader.ReadByte()
	}
	return hdr, payloadReader, nil
}

func readPayloadExact(reader io.Reader, expected int64) ([]byte, error) {
	if expected < 0 {
		return nil, fmt.Errorf("tamanho de payload inválido")
	}
	if expected > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("payload grande demais")
	}
	data := make([]byte, int(expected))
	n, err := io.ReadFull(reader, data)
	if err != nil {
		return nil, fmt.Errorf("leitura incompleta: esperado=%d recebido=%d: %w", expected, n, err)
	}
	return data, nil
}

// libp2pDownloadChunk abre um stream /artifact/get/1.0.0, solicita um range e
// salva os bytes no destFile. Verifica SHA256 do chunk apÃ³s receber.
// onProgress Ã© opcional â€” quando != nil, chamado com (bytesLidosNoChunk, tamanhoDoChunk).
func libp2pDownloadChunk(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string, chunk P2PChunk, destFile string, onProgress func(readSoFar, total int64)) error {
	if chunk.Size <= 0 {
		return fmt.Errorf("chunk %d: tamanho inválido", chunk.Index)
	}
	if chunk.Offset < 0 {
		return fmt.Errorf("chunk %d: offset invalido", chunk.Index)
	}

	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(computeTransferDeadline(chunk.Size))

	req := libp2pGetRequest{
		ArtifactName: artifactName,
		RequesterID:  requesterID,
		RangeStart:   chunk.Offset,
		RangeEnd:     chunk.Offset + chunk.Size - 1,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return fmt.Errorf("encode get req: %w", err)
	}

	hdr, payloadReader, err := decodeLibp2pGetHeaderAndPayload(s)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(hdr.ArtifactName), strings.TrimSpace(artifactName)) {
		return fmt.Errorf("chunk %d: artifact inesperado no payload", chunk.Index)
	}
	expectedStart := chunk.Offset
	expectedEnd := chunk.Offset + chunk.Size - 1
	if hdr.RangeStart != expectedStart || hdr.RangeEnd != expectedEnd {
		return fmt.Errorf("chunk %d: range inesperado no payload (esperado=%d-%d recebido=%d-%d)",
			chunk.Index, expectedStart, expectedEnd, hdr.RangeStart, hdr.RangeEnd)
	}

	chunkLen := hdr.RangeEnd - hdr.RangeStart + 1
	if chunkLen != chunk.Size {
		return fmt.Errorf("chunk %d: tamanho divergente (esperado=%d recebido=%d)", chunk.Index, chunk.Size, chunkLen)
	}
	var dataReader io.Reader = payloadReader
	if onProgress != nil {
		dataReader = newProgressReader(payloadReader, chunkLen, func(readSoFar int64) {
			onProgress(readSoFar, chunkLen)
		})
	}
	data, err := readPayloadExact(dataReader, chunkLen)
	if err != nil {
		return fmt.Errorf("leitura do chunk: %w", err)
	}

	// Verificar hash do chunk.
	if !verifySHA256(data, chunk.SHA256) {
		return fmt.Errorf("chunk %d: checksum divergente", chunk.Index)
	}

	return os.WriteFile(destFile, data, 0o644)
}

// libp2pDownloadArtifact faz download simples (arquivo inteiro) via /artifact/get/1.0.0.
// onProgress Ã© opcional â€” quando != nil, Ã© chamado com (bytesLidos, totalBytes) durante a transferÃªncia.
func libp2pDownloadArtifact(ctx context.Context, h host.Host, peerID peer.ID, access P2PArtifactAccess, destDir string, onProgress func(readSoFar, total int64)) (string, int64, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return "", 0, fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	// Deadline inicial: 2 min para handshake + header. Será reajustado após ler o header.
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

	hdr, payloadReader, err := decodeLibp2pGetHeaderAndPayload(s)
	if err != nil {
		return "", 0, err
	}
	if !strings.EqualFold(strings.TrimSpace(hdr.ArtifactName), strings.TrimSpace(access.ArtifactName)) {
		return "", 0, fmt.Errorf("artifact inesperado no payload")
	}
	if hdr.RangeStart != 0 {
		return "", 0, fmt.Errorf("range inicial inesperado no payload")
	}
	expectedBytes := hdr.RangeEnd - hdr.RangeStart + 1

	// Reajustar deadline com base no tamanho real do payload.
	_ = s.SetDeadline(computeTransferDeadline(expectedBytes))

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", 0, err
	}
	targetPath := filepath.Join(destDir, access.ArtifactName)
	tmpPath := targetPath + ".partial"
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}

	var reader io.Reader = payloadReader
	if onProgress != nil {
		reader = newProgressReader(payloadReader, expectedBytes, func(readSoFar int64) {
			onProgress(readSoFar, expectedBytes)
		})
	}
	size, copyErr := io.CopyN(f, reader, expectedBytes)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("leitura incompleta do artifact: esperado=%d recebido=%d: %w", expectedBytes, size, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, closeErr
	}
	if size != expectedBytes {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("tamanho recebido divergente: esperado=%d recebido=%d", expectedBytes, size)
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
	_ = platform.EnsureWorldReadable(targetPath)
	return targetPath, size, nil
}

// verifySHA256 verifica se data bate com o hex SHA256 esperado.
