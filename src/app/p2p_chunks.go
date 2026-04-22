package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	defaultChunkSizeBytes = 8 * 1024 * 1024 // 8 MB
	minChunkSizeBytes     = 1 * 1024 * 1024 // 1 MB
	maxParallelChunks     = 4
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

// downloadChunkedLibp2p downloads an artifact from multiple peers in parallel chunks
// via libp2p streams. peers must contain at least one element.
// Returns the final assembled file path and total bytes written.
func downloadChunkedLibp2p(
	ctx context.Context,
	h host.Host,
	peers []libp2pPeer,
	manifest P2PChunkManifest,
	artifactName, requesterID, destDir string,
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

			// Resume: if chunk file exists and hash matches, skip.
			if data, err := os.ReadFile(chunkFile); err == nil {
				h := sha256.Sum256(data)
				if strings.EqualFold(hex.EncodeToString(h[:]), chunk.SHA256) {
					results <- chunkResult{index: i, err: nil}
					return
				}
			}

			lp := sched.pickPeer(i, peers)
			err := libp2pDownloadChunk(ctx, h, lp.peerID, artifactName, requesterID, chunk, chunkFile)
			if err != nil {
				sched.recordError(lp.peerID)
			}
			results <- chunkResult{index: i, err: err}
		}(i, chunk)
	}

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
