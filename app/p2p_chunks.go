package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultChunkSizeBytes = 8 * 1024 * 1024 // 8 MB
	minChunkSizeBytes     = 1 * 1024 * 1024 // 1 MB
	maxParallelChunks     = 4
	// p2pBandwidthBurst is the token-bucket burst size for download rate limiting.
	// Set to 4 MB so that any realistic single HTTP read (≤ 64 KB) fits inside
	// the burst window, preventing spurious WaitN errors.
	p2pBandwidthBurst = 4 * 1024 * 1024
)

// p2pChunkScheduler tracks per-peer error counts and enforces an optional
// bandwidth rate limit across all parallel chunk downloads.
type p2pChunkScheduler struct {
	mu          sync.Mutex
	errorCounts map[string]int
	limiter     *rate.Limiter
}

// newP2PChunkScheduler creates a scheduler; bytesPerSec == 0 means unlimited.
func newP2PChunkScheduler(bytesPerSec int64) *p2pChunkScheduler {
	s := &p2pChunkScheduler{errorCounts: make(map[string]int)}
	if bytesPerSec > 0 {
		s.limiter = rate.NewLimiter(rate.Limit(bytesPerSec), p2pBandwidthBurst)
	}
	return s
}

// pickPeer selects the peer with the fewest recorded errors.
// Falls back to round-robin when error counts are equal (e.g. first request).
func (s *p2pChunkScheduler) pickPeer(chunkIdx int, peers []P2PArtifactAccess) P2PArtifactAccess {
	if len(peers) == 1 {
		return peers[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Default: round-robin anchor so peers without errors are distributed evenly.
	best := peers[chunkIdx%len(peers)]
	bestErr := s.errorCounts[best.URL]
	for _, p := range peers {
		if e := s.errorCounts[p.URL]; e < bestErr {
			bestErr = e
			best = p
		}
	}
	return best
}

// recordError increments the error tally for the given peer URL.
func (s *p2pChunkScheduler) recordError(peerURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorCounts[peerURL]++
}

// waitBandwidth blocks until the rate limiter grants n bytes.
// It splits large n values into burst-sized chunks to be safe with WaitN.
func (s *p2pChunkScheduler) waitBandwidth(ctx context.Context, n int) {
	if s.limiter == nil || n <= 0 {
		return
	}
	remaining := n
	for remaining > 0 {
		chunk := remaining
		if chunk > p2pBandwidthBurst {
			chunk = p2pBandwidthBurst
		}
		if err := s.limiter.WaitN(ctx, chunk); err != nil {
			return // context cancelled
		}
		remaining -= chunk
	}
}

// P2PChunkManifest describes how an artifact is divided for swarm download.
type P2PChunkManifest struct {
	ArtifactID   string     `json:"artifactId"`
	ArtifactName string     `json:"artifactName"`
	TotalSize    int64      `json:"totalSize"`
	ChunkSize    int64      `json:"chunkSize"`
	TotalChunks  int        `json:"totalChunks"`
	SHA256       string     `json:"sha256"`
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
func buildChunkManifest(path, artifactID string, chunkSize int64) (P2PChunkManifest, error) {
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

	fullHash := sha256.New()
	var chunks []P2PChunk
	var offset int64

	buf := make([]byte, chunkSize)
	for {
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

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return P2PChunkManifest{}, readErr
		}
	}

	return P2PChunkManifest{
		ArtifactID:   artifactID,
		ArtifactName: sanitizeArtifactName(filepath.Base(path)),
		TotalSize:    totalSize,
		ChunkSize:    chunkSize,
		TotalChunks:  len(chunks),
		SHA256:       hex.EncodeToString(fullHash.Sum(nil)),
		Chunks:       chunks,
	}, nil
}

// downloadChunk downloads a single chunk from a peer using an HTTP Range request.
// The destFile path is the target file to write.
// sched is optional; when non-nil it applies bandwidth rate limiting.
func downloadChunk(ctx context.Context, baseURL string, chunk P2PChunk, destFile string, sched *p2pChunkScheduler) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return err
	}
	// Add standard Range header; http.ServeFile on the server side honors it automatically.
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", chunk.Offset, chunk.Offset+chunk.Size-1))

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Accept both 206 Partial Content and 200 OK.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chunk HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// Limit read to expected chunk size to prevent oversized responses.
	reader := io.LimitReader(resp.Body, chunk.Size+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	// Verify chunk hash.
	got := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(got[:]), chunk.SHA256) {
		return fmt.Errorf("chunk %d: checksum divergente", chunk.Index)
	}

	// Throttle bandwidth: wait for rate-limiter tokens proportional to chunk size.
	if sched != nil {
		sched.waitBandwidth(ctx, len(data))
	}

	return os.WriteFile(destFile, data, 0o644)
}

// downloadChunked downloads an artifact from multiple peers in parallel chunks.
// peers must contain at least one element. When len(peers) > 1, chunks are
// distributed using scored peer selection (fewest errors wins); with a single
// peer it degrades gracefully to sequential.
// sched is optional; when non-nil it provides scored peer selection and bandwidth
// rate limiting. Pass nil for a plain round-robin / unlimited download.
// Returns the final assembled file path and total bytes written.
func downloadChunked(
	ctx context.Context,
	peers []P2PArtifactAccess,
	manifest P2PChunkManifest,
	destDir string,
	sched *p2pChunkScheduler,
) (string, int64, error) {
	if len(peers) == 0 {
		return "", 0, fmt.Errorf("nenhum peer disponivel para download")
	}

	partsDir := filepath.Join(destDir, manifest.ArtifactName+".parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		return "", 0, err
	}

	type chunkResult struct {
		index int
		err   error
	}

	sem := make(chan struct{}, maxParallelChunks)
	results := make(chan chunkResult, len(manifest.Chunks))
	var wg sync.WaitGroup

	for i, chunk := range manifest.Chunks {
		wg.Add(1)
		go func(i int, chunk P2PChunk) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			chunkFile := filepath.Join(partsDir, fmt.Sprintf("chunk-%04d", i))

			// Resume: if chunk file exists and its hash matches, skip.
			if data, err := os.ReadFile(chunkFile); err == nil {
				h := sha256.Sum256(data)
				if strings.EqualFold(hex.EncodeToString(h[:]), chunk.SHA256) {
					results <- chunkResult{index: i, err: nil}
					return
				}
			}

			// Pick peer by score (fewest errors) falling back to round-robin.
			var access P2PArtifactAccess
			if sched != nil {
				access = sched.pickPeer(i, peers)
			} else {
				access = peers[i%len(peers)]
			}
			err := downloadChunk(ctx, access.URL, chunk, chunkFile, sched)
			if err != nil && sched != nil {
				sched.recordError(access.URL)
			}
			results <- chunkResult{index: i, err: err}
		}(i, chunk)
	}

	// Close results channel after all goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for res := range results {
		if res.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("chunk %d: %w", res.index, res.err)
		}
	}
	if firstErr != nil {
		return "", 0, firstErr
	}

	// Assemble chunks into final file.
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
	assembled := hex.EncodeToString(fullHash.Sum(nil))
	if strings.TrimSpace(manifest.SHA256) != "" && !strings.EqualFold(assembled, manifest.SHA256) {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("checksum do arquivo final divergente")
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}

	// Clean up parts directory.
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
