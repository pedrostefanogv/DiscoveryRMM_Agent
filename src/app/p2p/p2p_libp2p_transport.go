package p2p

// app_p2p_libp2p_transport.go
//
// Protocolos libp2p de transporte P2P para troca de artifacts e gossip entre agents.
// Substitui os endpoints HTTP: /p2p/peers, /p2p/artifact/access,
// /p2p/artifact/{name}, /p2p/artifact/{name}/manifest e /p2p/replicate.
//
// Protocolos definidos (stream-based, JSON framing):
//   /discovery/peers/1.0.0       —" gossip: retorna peers conhecidos + catálogo local
//   /artifact/access/1.0.0       —" emite token de acesso para um artifact
//   /artifact/manifest/1.0.0     —" retorna manifest de chunks de um artifact
//   /artifact/get/1.0.0          —" transfere bytes de um chunk (range-aware)
//   /artifact/replicate/1.0.0    —" notifica peer para pull de artifact (push desativado, retorna Gone)

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
	"time"

	"discovery/app/core/platform"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	protoDiscoveryPeers         = "/discovery/peers/1.0.0"
	protoArtifactAccess         = "/artifact/access/1.0.0"
	protoArtifactManifest       = "/artifact/manifest/1.0.0"
	protoArtifactGet            = "/artifact/get/1.0.0"
	protoArtifactReplicate      = "/artifact/replicate/1.0.0"
	protoFetchCandidacy         = "/fetch/candidacy/1.0.0"
	protoFetchHeartbeat         = "/fetch/heartbeat/1.0.0"
	libp2pStreamTimeout         = 30 * time.Second
	libp2pTransferTimeout       = 2 * time.Minute
	libp2pCandidacyTimeout      = 10 * time.Second
	libp2pTransferTimeoutMax    = 4 * time.Hour   // arquivos grandes (6GB+) precisam de deadlines longos
	libp2pManifestTimeout       = 5 * time.Minute // cobre leitura completa do arquivo para gerar manifest
	libp2pMinTransferSpeed      = 512 * 1024      // 512 KB/s - piso conservador para clculo de timeout
	libp2pTransferTimeoutMargin = 30 * time.Second
)

// computeTransferDeadline calcula um deadline adaptativo baseado no tamanho dos dados.
// Piso: libp2pTransferTimeout (2 min). Teto: libp2pTransferTimeoutMax (4 horas).
// Assume velocidade mnima de 512 KB/s mais margem fixa de 30s.
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

// rollingDeadlineInterval define a cada quantos bytes lidos/escritos o deadline
// do stream é renovado. 512KB equilibra granularidade e custo de SetDeadline.
const rollingDeadlineInterval = 512 * 1024

// rollingDeadlineStreamReader renova o deadline do stream conforme bytes fluem.
// Problema que resolve: com N chunks paralelos sobre a mesma conexão libp2p em
// redes lentas, a vazão por stream cai e o deadline FIXO (piso de 2min) expira
// no meio do chunk, matando todos os streams de uma vez ("Application error
// 0x0" simultâneo em seed e cliente). Com renovação rolante, o timeout passa a
// valer apenas para inatividade real (stall), não para duração total.
type rollingDeadlineStreamReader struct {
	r        io.Reader
	stream   network.Stream
	interval int64
	read     int64
	next     int64
}

func (rr *rollingDeadlineStreamReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	rr.read += int64(n)
	if rr.read >= rr.next {
		_ = rr.stream.SetDeadline(time.Now().Add(libp2pTransferTimeout))
		rr.next = rr.read + rr.interval
	}
	return n, err
}

// "—"— Request / Response types "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

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

// "—"— Tipos do protocolo /fetch "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// libp2pCandidacyRequest é enviado por um peer que quer se candidatar a fetcher.
// Reutiliza ArtifactFetchCandidate como payload.
type libp2pCandidacyRequest = ArtifactFetchCandidate

// libp2pCandidacyResponse confirma recebimento da candidatura.
type libp2pCandidacyResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// "—"— Server-side: registrar handlers no host libp2p "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// RegisterP2PProtocols instala todos os stream handlers nos 7 protocolos.
// Chamado em app_p2p_libp2p.go após criar o host.
func RegisterP2PProtocols(h host.Host, coord *Coordinator, transfer *TransferServer) {
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

func handleStreamPeers(s network.Stream, coord *Coordinator, transfer *TransferServer) {
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

func handleStreamArtifactAccess(s network.Stream, transfer *TransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pAccessRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
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

func handleStreamArtifactManifest(s network.Stream, transfer *TransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pManifestRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	deps := transfer.deps
	transfer.mu.RUnlock()

	// Rejeitar arquivos .importing (ainda sendo copiados) — não devem gerar
	// nem servir manifest, assim como o /artifact/get rejeita. Evita servir
	// um manifest com offsets/hashes de um arquivo parcial.
	if strings.HasSuffix(req.ArtifactName, ".importing") {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact em andamento"})
		return
	}

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact no encontrado"})
		return
	}

	// Tentar cache de manifest primeiro. Valida com manifestMatchesFile para
	// garantir que o manifest corresponde ao arquivo atual em disco (tamanho +
	// mtime + hash dos chunks de borda). Sem essa validação, um artifact
	// republicado com o mesmo nome serviria um manifest stale com offsets/hashes
	// da versão antiga, fazendo o receiver validar chunks contra hashes errados
	// (checksum divergente).
	manifestDir := filepath.Join(tempDir, manifestDirName)
	if cached := loadCachedManifest(manifestDir, req.ArtifactName, path); cached != nil {
		if manifestMatchesFile(cached, path) {
			if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
				shaShort := cached.SHA256
				if len(shaShort) > 16 {
					shaShort = shaShort[:16]
				}
				transfer.coord.deps.Log(fmt.Sprintf(
					"[p2p][serve-manifest] cache hit artifact=%s chunks=%d size=%d mtime=%d sha256=%s",
					req.ArtifactName, cached.TotalChunks, cached.TotalSize, cached.SourceMTime, shaShort))
			}
			_ = json.NewEncoder(s).Encode(cached)
			return
		}
		// Cache stale: invalida e regenera abaixo.
		if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
			transfer.coord.deps.Log(fmt.Sprintf(
				"[p2p][serve-manifest] cache stale artifact=%s — invalidando e regenerando", req.ArtifactName))
		}
		if transfer != nil && transfer.coord != nil {
			transfer.coord.recordStaleManifest()
		}
		_ = os.Remove(cachedManifestPath(manifestDir, req.ArtifactName))
	}

	// Reajustar deadline: buildChunkManifest l o arquivo inteiro.
	_ = s.SetDeadline(computeTransferDeadline(info.Size()))

	var chunkSize int64 = defaultChunkSizeBytes
	if deps != nil {
		if cfg := deps.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
			chunkSize = cfg.ChunkSizeBytes
		}
	}
	artifactID := CanonicalArtifactID("", req.ArtifactName, "")
	var cpuFn func() float64
	if transfer != nil && transfer.coord != nil {
		cpuFn = func() float64 { return transfer.coord.cpuSampler.Sample() }
	}
	manifest, err := buildChunkManifest(context.Background(), path, artifactID, chunkSize, nil, cpuFn)
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao construir manifest: " + err.Error()})
		return
	}

	// Cachear manifest para reuso futuro (best-effort).
	_ = saveCachedManifest(manifestDir, req.ArtifactName, manifest)

	_ = json.NewEncoder(s).Encode(manifest)
}

func handleStreamArtifactGet(s network.Stream, transfer *TransferServer) {
	defer s.Close()

	var req libp2pGetRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	transfer.mu.RUnlock()

	// Rejeitar arquivos .importing (ainda sendo copiados) - no devem ser servidos.
	if strings.HasSuffix(req.ArtifactName, ".importing") {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact em andamento"})
		return
	}

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact no encontrado"})
		return
	}

	// Reusa SHA256 do manifest cacheado em vez de recalcular o arquivo inteiro.
	// IMPORTANTE: valida o manifest contra o arquivo atual (tamanho + mtime +
	// hash de chunks de borda) antes de servir. Sem isso, um arquivo substituído
	// (republicado) durante transferências em andamento faria o servidor servir
	// bytes da versão nova enquanto o cliente valida contra hashes da versão
	// antiga → checksum divergente em chunks aleatórios.
	checksum := ""
	if transfer != nil {
		manifestDir := transfer.manifestDir()
		if cached := loadCachedManifest(manifestDir, req.ArtifactName, path); cached != nil {
			if manifestMatchesFile(cached, path) {
				checksum = cached.SHA256
			} else {
				// Manifest stale: invalida para forçar regeneração no próximo
				// serve-manifest e recalcula o checksum do arquivo atual.
				if transfer.coord != nil && transfer.coord.deps != nil {
					transfer.coord.deps.Log(fmt.Sprintf(
						"[p2p][serve] manifest stale detectado no GET artifact=%s (mtime arquivo=%d) — invalidando cache",
						req.ArtifactName, info.ModTime().UnixNano()))
					transfer.coord.recordStaleManifest()
				}
				_ = os.Remove(cachedManifestPath(manifestDir, req.ArtifactName))
			}
		}
	}
	if checksum == "" {
		var csErr error
		checksum, csErr = computeFileSHA256(path)
		if csErr != nil {
			_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao calcular checksum"})
			return
		}
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
	// Diagnóstico: se o range solicitado difere do que será servido, é forte
	// indício de manifest stale (o cliente pediu offsets de uma versão antiga
	// do artifact). Loga para facilitar a depuração de checksum divergente.
	if req.RangeEnd != rangeEnd || req.RangeStart != rangeStart {
		if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
			transfer.coord.deps.Log(fmt.Sprintf(
				"[p2p][serve] range ajustado artifact=%s solicitado=%d-%d servido=%d-%d (totalSize=%d) — possível manifest stale",
				req.ArtifactName, req.RangeStart, req.RangeEnd, rangeStart, rangeEnd, totalSize))
		}
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

	var coord *Coordinator
	if transfer != nil {
		coord = transfer.coord
	}
	requesterID := strings.TrimSpace(req.RequesterID)
	if requesterID == "" {
		requesterID = "peer-remote"
	}

	reader := io.LimitReader(f, chunkLen)
	if coord != nil {
		// Track progresso acumulado por sesso (artifact+requester) para
		// que a barra seja monotnica e relativa ao tamanho total do arquivo,
		// no a cada chunk individual.
		sessionKey := req.ArtifactName + "|" + requesterID
		coord.servingSessionsMu.Lock()
		sess, exists := coord.servingSessions[sessionKey]
		if !exists {
			sess = &servingSession{
				artifactName: req.ArtifactName,
				requesterID:  requesterID,
				totalSize:    totalSize,
				lastActive:   time.Now(),
			}
			coord.servingSessions[sessionKey] = sess
		}
		sess.lastActive = time.Now()
		coord.servingSessionsMu.Unlock()

		reader = newProgressReader(reader, chunkLen, func(readSoFar int64) {
			coord.servingSessionsMu.Lock()
			// Atualiza lastActive para que o GC não remova a sessão durante
			// um upload longo (ex.: arquivos grandes em redes lentas).
			sess.lastActive = time.Now()
			accumulated := sess.servedBytes + readSoFar
			// S emite se houve mudana significativa (>1% ou 100KB)
			delta := accumulated - sess.lastReported
			threshold := sess.totalSize / 100
			if threshold < 100*1024 {
				threshold = 100 * 1024
			}
			if delta >= threshold || accumulated >= sess.totalSize {
				sess.lastReported = accumulated
				coord.servingSessionsMu.Unlock()
				coord.emitTransferProgress(p2pTransferProgress{
					ArtifactName: req.ArtifactName,
					PeerID:       requesterID,
					BytesRead:    accumulated,
					TotalBytes:   sess.totalSize,
					Operation:    "serve",
					Direction:    "upload",
				})
				return
			}
			coord.servingSessionsMu.Unlock()
		})
	}

	// Deadline para o lado servidor: evita que o libp2p encerre a conexo
	// por timeout enquanto o chunk est sendo enviado (especialmente em redes lentas).
	// Deadline ROLANTE: renovado a cada 512KB enviados — só expira em stall real,
	// não em transferência lenta (vazão baixa com chunks paralelos).
	_ = s.SetDeadline(computeTransferDeadline(chunkLen))
	reader = &rollingDeadlineStreamReader{r: reader, stream: s, interval: rollingDeadlineInterval}

	written, copyErr := io.Copy(s, reader)
	if coord != nil && written > 0 {
		coord.recordBytesServed(written)
	}
	if copyErr != nil && coord != nil && coord.deps != nil {
		coord.deps.Log(fmt.Sprintf("[p2p][serve] erro ao enviar chunk artifact=%s para peer=%s chunkLen=%d enviado=%d: %v",
			req.ArtifactName, requesterID, chunkLen, written, copyErr))
	}
	if coord != nil {
		// Atualiza a sesso com os bytes acumulados desse chunk.
		sessionKey := req.ArtifactName + "|" + requesterID
		coord.servingSessionsMu.Lock()
		sess, ok := coord.servingSessions[sessionKey]
		// Snapshot dos valores DENTRO do lock para evitar data race: a sessão
		// pode ser deletada por outra goroutine (mesmo artifact+requester) logo
		// após o Unlock, então não podemos ler sess.servedBytes/totalSize fora.
		var snapshotServedBytes, snapshotTotalSize int64
		if ok {
			sess.servedBytes += written
			sess.lastReported = sess.servedBytes
			snapshotServedBytes = sess.servedBytes
			snapshotTotalSize = sess.totalSize
		}
		isComplete := ok && sess.servedBytes >= sess.totalSize
		// Se deu erro ou a sesso no existe, tambm encerra.
		isDone := copyErr != nil || isComplete || !ok
		if isDone {
			delete(coord.servingSessions, sessionKey)
		}
		coord.servingSessionsMu.Unlock()

		done := p2pTransferProgress{
			ArtifactName: req.ArtifactName,
			PeerID:       requesterID,
			Operation:    "serve",
			Direction:    "upload",
		}
		// BytesRead/TotalBytes so sempre relativos ao arquivo completo
		// para uma barra de progresso monotnica.
		if ok {
			done.BytesRead = snapshotServedBytes
			done.TotalBytes = snapshotTotalSize
		} else {
			done.BytesRead = written
			done.TotalBytes = chunkLen
		}
		done.Done = isDone
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

// "—"— Client-side: funções de chamada para peers "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

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
	// O header é enviado via json.Encoder.Encode, que grava `{...}\n` (uma
	// única linha JSON seguida de newline). Lemos a linha inteira até o
	// primeiro '\n', que é o delimitador do Encoder. Todo o resto do stream é
	// o payload binário.
	//
	// IMPORTANTE: abordagens anteriores usavam json.Decoder + Peek/ReadByte
	// para remover o '\n' do header, o que era frágil: o payload binário pode
	// começar com 0x0A e o decoder pode ter consumido (ou não) o delimitador,
	// fazendo um byte legítimo do payload ser removido (checksum divergente).
	// Ler a linha do header e tratar o restante como payload puro é robusto.
	br := bufio.NewReader(stream)
	headerLine, err := br.ReadBytes('\n')
	if err != nil {
		return libp2pGetResponse{}, nil, fmt.Errorf("decode get hdr: %w", err)
	}
	var hdr libp2pGetResponse
	if err := json.Unmarshal(headerLine, &hdr); err != nil {
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

	// Retorna um reader limitado ao tamanho EXATO do payload (conhecido no
	// header). Assim o caller lê exatamente payloadLen bytes do payload binário,
	// sem depender de nenhum delimitador entre header e payload.
	payloadLen := hdr.RangeEnd - hdr.RangeStart + 1
	payloadReader := io.LimitReader(br, payloadLen)
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
// salva os bytes no destFile. Verifica SHA256 do chunk após receber.
// onProgress é opcional —" quando != nil, chamado com (bytesLidosNoChunk, tamanhoDoChunk).
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
	// Deadline ROLANTE no cliente: renova a cada 512KB recebidos para que o
	// piso de 2min não expire em transferências lentas (paralelismo alto /
	// rede WiFi congestionada), apenas em stall real do peer.
	dataReader = &rollingDeadlineStreamReader{r: payloadReader, stream: s, interval: rollingDeadlineInterval}
	if onProgress != nil {
		dataReader = newProgressReader(payloadReader, chunkLen, func(readSoFar int64) {
			onProgress(readSoFar, chunkLen)
		})
	}

	// Streaming para disco: grava o chunk em um arquivo temporário enquanto
	// calcula o SHA256 em fluxo, evitando carregar o chunk inteiro em memória
	// (relevante com paralelismo alto e chunks de 8 MB).
	tmpFile := destFile + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("criar arquivo temporario do chunk: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(f, hasher), dataReader, chunkLen)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("leitura do chunk: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("fechar arquivo temporario do chunk: %w", closeErr)
	}
	if written != chunkLen {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("chunk %d: leitura incompleta (esperado=%d recebido=%d)", chunk.Index, chunkLen, written)
	}

	// Verificar hash do chunk.
	// hasher.Sum(nil) já é o SHA256 do chunk — verifySHA256Hex compara o digest
	// diretamente (a versão anterior re-hasheava o digest, causando falha em
	// 100% dos chunks mesmo com dados íntegros).
	if !verifySHA256Hex(hasher.Sum(nil), chunk.SHA256) {
		_ = os.Remove(tmpFile)
		// Diagnóstico: loga os prefixos dos hashes para identificar manifest
		// stale (cliente valida contra hashes de versão antiga do arquivo).
		gotShort := hex.EncodeToString(hasher.Sum(nil))
		if len(gotShort) > 12 {
			gotShort = gotShort[:12]
		}
		wantShort := chunk.SHA256
		if len(wantShort) > 12 {
			wantShort = wantShort[:12]
		}
		return fmt.Errorf("chunk %d: checksum divergente (esperado=%s obtido=%s)", chunk.Index, wantShort, gotShort)
	}

	if err := os.Rename(tmpFile, destFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("chunk %d: renomear arquivo: %w", chunk.Index, err)
	}
	return nil
}

// libp2pDownloadArtifact faz download simples (arquivo inteiro) via /artifact/get/1.0.0.
// onProgress é opcional —" quando != nil, é chamado com (bytesLidos, totalBytes) durante a transferência.
func libp2pDownloadArtifact(ctx context.Context, h host.Host, peerID peer.ID, access P2PArtifactAccess, destDir string, onProgress func(readSoFar, total int64)) (string, int64, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return "", 0, fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	// Deadline inicial: 2 min para handshake + header. Ser reajustado aps ler o header.
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
	if err := platform.RenameAtomic(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	_ = platform.EnsureWorldReadable(targetPath)
	return targetPath, size, nil
}
