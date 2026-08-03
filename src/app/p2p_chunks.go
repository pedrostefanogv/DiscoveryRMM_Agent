package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"discovery/internal/platform"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	defaultChunkSizeBytes = 8 * 1024 * 1024 // 8 MB
	minChunkSizeBytes     = 1 * 1024 * 1024 // 1 MB
	minParallelChunks     = 2               // piso do paralelismo adaptativo
	maxParallelChunks     = 4               // teto padrão (pode ser elevado dinamicamente até 8)
	maxChunkRetries       = 3               // tentativas por chunk antes de desistir
	chunkRetryBaseDelay   = 1 * time.Second // backoff inicial entre retries
)

// libp2pPeer identifica um peer pelo agentID e pelo peer.ID do libp2p.
type libp2pPeer struct {
	agentID string
	peerID  peer.ID
}

// p2pChunkScheduler tracks per-peer error counts for scored peer selection.
type p2pChunkScheduler struct {
	mu          sync.Mutex
	errorCounts map[string]int
}

// newP2PChunkScheduler creates a scheduler.
func newP2PChunkScheduler() *p2pChunkScheduler {
	return &p2pChunkScheduler{errorCounts: make(map[string]int)}
}

// pickPeer selects the peer with the fewest recorded errors.
// Falls back to round-robin when error counts are equal (e.g. first request).
func (s *p2pChunkScheduler) pickPeer(chunkIdx int, peers []libp2pPeer) libp2pPeer {
	if len(peers) == 1 {
		return peers[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	best := peers[chunkIdx%len(peers)]
	bestErr := s.errorCounts[best.peerID.String()]
	for _, p := range peers {
		if e := s.errorCounts[p.peerID.String()]; e < bestErr {
			bestErr = e
			best = p
		}
	}
	return best
}

// recordError increments the error tally for the given peer.
func (s *p2pChunkScheduler) recordError(peerID peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorCounts[peerID.String()]++
}

// P2PChunkManifest describes how an artifact is divided for swarm download.
type P2PChunkManifest struct {
	ArtifactID   string     `json:"artifactId"`
	ArtifactName string     `json:"artifactName"`
	TotalSize    int64      `json:"totalSize"`
	ChunkSize    int64      `json:"chunkSize"`
	TotalChunks  int        `json:"totalChunks"`
	SHA256       string     `json:"sha256"`
	SourceMTime  int64      `json:"sourceMtime"` // UnixNano do mtime do arquivo fonte
	Chunks       []P2PChunk `json:"chunks"`
}

// P2PChunk describes a single chunk within a manifest.
type P2PChunk struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// buildChunkManifest computes a P2PChunkManifest for a file on disk.
// It reads the file once: per-chunk SHA256 and full-file SHA256 in one pass.
//
// ctx permite cancelamento cooperativo. A cada chunkProcessedThreshold chunks,
// a goroutine faz yield (runtime.Gosched) e, se cpuCheck não for nil, aplica
// throttling adaptativo baseado na métrica de CPU retornada pelo callback
// (percentual 0-100). Acima de cpuThrottleThreshold, dorme por cpuThrottleSleep.
//
// onProgress é opcional — se não-nil, chamado com (chunksProcessados, totalChunks, bytesProcessados).
func buildChunkManifest(ctx context.Context, path, artifactID string, chunkSize int64,
	onProgress func(processedChunks, totalChunks int, bytesProcessed int64),
	cpuCheck func() float64) (P2PChunkManifest, error) {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSizeBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return P2PChunkManifest{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return P2PChunkManifest{}, err
	}
	totalSize := info.Size()

	// Pré-alocar slice de chunks para evitar realocações no loop.
	totalChunksEstimate := int(totalSize/chunkSize) + 1
	if totalChunksEstimate < 1 {
		totalChunksEstimate = 1
	}
	if totalChunksEstimate > 100000 {
		totalChunksEstimate = 100000 // sanity cap
	}

	fullHash := sha256.New()
	chunks := make([]P2PChunk, 0, totalChunksEstimate)
	var offset int64

	buf := make([]byte, chunkSize)
	for {
		// Verificar cancelamento a cada chunk.
		select {
		case <-ctx.Done():
			return P2PChunkManifest{}, ctx.Err()
		default:
		}

		n, readErr := io.ReadFull(f, buf)
		if n == 0 {
			break
		}
		data := buf[:n]
		fullHash.Write(data)

		chunkHash := sha256.Sum256(data)
		chunks = append(chunks, P2PChunk{
			Index:  len(chunks),
			Offset: offset,
			Size:   int64(n),
			SHA256: hex.EncodeToString(chunkHash[:]),
		})
		offset += int64(n)

		// Throttling adaptativo: a cada chunkProcessedThreshold chunks,
		// yield para não travar a UI e reduzir CPU se necessário.
		if len(chunks)%chunkProcessedThreshold == 0 {
			runtime.Gosched()

			if cpuCheck != nil {
				cpu := cpuCheck()
				if cpu > cpuThrottleThreshold {
					time.Sleep(cpuThrottleSleep)
				}
			}

			if onProgress != nil {
				onProgress(len(chunks), totalChunksEstimate, offset)
			}
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return P2PChunkManifest{}, readErr
		}
	}

	// Último callback de progresso (garante 100% reportado).
	if onProgress != nil {
		onProgress(len(chunks), len(chunks), totalSize)
	}

	return P2PChunkManifest{
		ArtifactID:   artifactID,
		ArtifactName: sanitizeArtifactName(filepath.Base(path)),
		TotalSize:    totalSize,
		ChunkSize:    chunkSize,
		TotalChunks:  len(chunks),
		SHA256:       hex.EncodeToString(fullHash.Sum(nil)),
		SourceMTime:  info.ModTime().UnixNano(),
		Chunks:       chunks,
	}, nil
}

// manifestProgressThreshold define a frequência de yield e throttling.
// A cada N chunks, a goroutine faz yield e verifica CPU.
const (
	chunkProcessedThreshold = 16          // yield a cada 16 chunks (~128 MB com 8 MB)
	cpuThrottleThreshold    = float64(60) // CPU > 60% → throttle
	cpuThrottleSleep        = 100 * time.Millisecond
)

// downloadChunkedLibp2p downloads an artifact from multiple peers in parallel chunks
// via libp2p streams. maxParallel controla o teto de chunks simultâneos.
// Goroutines que já adquiriram slot não são abortadas se o teto reduzir depois —
// apenas novas goroutines esperam. Usa minParallelChunks como piso.
// onChunkProgress é opcional — quando != nil, chamado com (chunkIndex, bytesLidos, totalChunk, totalChunks).
// onChunkComplete é opcional — quando != nil, chamado com (completed, total) após cada chunk concluído.
// onPhase é opcional — quando != nil, chamado com fases ("assembling", "verifying") durante a remontagem.
// logf é opcional — quando != nil, chamado para logging de progresso e erros de chunks.
func downloadChunkedLibp2p(
	ctx context.Context,
	h host.Host,
	peers []libp2pPeer,
	manifest P2PChunkManifest,
	artifactName, requesterID, destDir string,
	sched *p2pChunkScheduler,
	maxParallel int,
	onChunkProgress func(chunkIdx int, readSoFar, chunkSize int64, totalChunks int),
	onChunkComplete func(completed, total int),
	onPhase func(phase string),
	logf func(string),
) (string, int64, error) {
	if len(peers) == 0 {
		return "", 0, fmt.Errorf("nenhum peer disponivel para download")
	}
	if maxParallel < minParallelChunks {
		maxParallel = minParallelChunks
	}

	partsDir := filepath.Join(destDir, manifest.ArtifactName+".parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		return "", 0, err
	}

	type chunkResult struct {
		index int
		err   error
	}

	sem := make(chan struct{}, maxParallel)
	results := make(chan chunkResult, len(manifest.Chunks))
	var wg sync.WaitGroup

	for i, chunk := range manifest.Chunks {
		wg.Add(1)
		go func(i int, chunk P2PChunk) {
			defer wg.Done()
			// Adquire slot — bloqueia se todos ocupados, mas aborta se o contexto
			// for cancelado enquanto espera (cancelamento imediato).
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results <- chunkResult{index: i, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			chunkFile := filepath.Join(partsDir, fmt.Sprintf("chunk-%04d", i))

			// Resume: if chunk file exists and hash matches, skip.
			// Usa hash em streaming para não carregar o chunk inteiro em memória.
			if chunkFileHashMatches(chunkFile, chunk.SHA256) {
				results <- chunkResult{index: i, err: nil}
				return
			}
			// Arquivo existe mas hash não bate (ou não existe) — remove e re-download.
			_ = os.Remove(chunkFile)

			chunkIdx := i
			chunkSize := chunk.Size
			totalChunks := len(manifest.Chunks)

			// Retry loop: tenta o chunk até maxChunkRetries vezes com backoff.
			var lastErr error
			for attempt := 0; attempt < maxChunkRetries; attempt++ {
				// Pick peer considerando erros acumulados em tentativas anteriores.
				lp := sched.pickPeer(i, peers)

				lastErr = libp2pDownloadChunk(ctx, h, lp.peerID, artifactName, requesterID, chunk, chunkFile, func(readSoFar, total int64) {
					if onChunkProgress != nil {
						onChunkProgress(chunkIdx, readSoFar, chunkSize, totalChunks)
					}
				})
				if lastErr == nil {
					// Sucesso: verifica hash salvo já foi feito em libp2pDownloadChunk.
					results <- chunkResult{index: i, err: nil}
					return
				}
				sched.recordError(lp.peerID)
				if logf != nil {
					logf(fmt.Sprintf("[p2p][chunk] chunk %d/%d tentativa %d/%d falhou para artifact=%s: %v",
						chunkIdx, totalChunks, attempt+1, maxChunkRetries, artifactName, lastErr))
				}
				// Backoff: 1s, 2s, 4s entre tentativas.
				if attempt < maxChunkRetries-1 {
					delay := chunkRetryBaseDelay * (1 << attempt)
					select {
					case <-ctx.Done():
						results <- chunkResult{index: i, err: ctx.Err()}
						return
					case <-time.After(delay):
					}
				}
			}
			results <- chunkResult{index: i, err: lastErr}
		}(i, chunk)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	var failedChunks int
	var completedChunks int
	totalChunks := len(manifest.Chunks)
	for res := range results {
		if res.err != nil {
			failedChunks++
			if logf != nil {
				logf(fmt.Sprintf("[p2p][chunk] chunk %d/%d falhou para artifact=%s: %v", res.index, len(manifest.Chunks), artifactName, res.err))
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("chunk %d: %w", res.index, res.err)
			}
		} else {
			completedChunks++
			if onChunkComplete != nil {
				onChunkComplete(completedChunks, totalChunks)
			}
		}
	}
	if firstErr != nil {
		// Limpa o diretório de partes em falha para não acumular lixo
		// (o GC de 1h só limparia depois).
		_ = os.RemoveAll(partsDir)
		return "", 0, fmt.Errorf("%d/%d chunks falharam; primeiro erro: %w",
			failedChunks, len(manifest.Chunks), firstErr)
	}

	// Assemble chunks into final file.
	if onPhase != nil {
		onPhase("assembling")
	}
	targetPath := filepath.Join(destDir, manifest.ArtifactName)
	tmpPath := targetPath + ".partial"
	out, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}

	fullHash := sha256.New()
	var totalBytes int64
	for i := range manifest.Chunks {
		chunkFile := filepath.Join(partsDir, fmt.Sprintf("chunk-%04d", i))
		data, err := os.ReadFile(chunkFile)
		if err != nil {
			out.Close()
			_ = os.Remove(tmpPath)
			return "", 0, fmt.Errorf("leitura do chunk %d: %w", i, err)
		}
		if _, err := out.Write(data); err != nil {
			out.Close()
			_ = os.Remove(tmpPath)
			return "", 0, fmt.Errorf("escrita do chunk %d: %w", i, err)
		}
		fullHash.Write(data)
		totalBytes += int64(len(data))
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}

	// Verify full file hash.
	if onPhase != nil {
		onPhase("verifying")
	}
	assembled := hex.EncodeToString(fullHash.Sum(nil))
	if strings.TrimSpace(manifest.SHA256) != "" && !strings.EqualFold(assembled, manifest.SHA256) {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("checksum do arquivo final divergente")
	}

	if err := platform.RenameAtomic(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	_ = platform.EnsureWorldReadable(targetPath)

	_ = os.RemoveAll(partsDir)
	return targetPath, totalBytes, nil
}

// recordChunkedDownload increments the chunked-download metrics atomically.
func (c *p2pCoordinator) recordChunkedDownload(chunks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics.ChunkedDownloads++
	c.metrics.ChunksDownloaded += int64(chunks)
}

// chunkFileHashMatches verifica se o arquivo de chunk em disco tem o SHA256
// esperado, calculando o hash em streaming (sem carregar o arquivo inteiro
// em memória). Retorna false se o arquivo não existir ou o hash divergir.
func chunkFileHashMatches(path, expectedSHA256 string) bool {
	if strings.TrimSpace(expectedSHA256) == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedSHA256)
}
