package app

import (
	"io"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// p2pTransferProgress representa o progresso de uma transferência P2P em tempo real.
type p2pTransferProgress struct {
	ArtifactName string `json:"artifactName"`
	PeerID       string `json:"peerId"`
	BytesRead    int64  `json:"bytesRead"`
	TotalBytes   int64  `json:"totalBytes"`
	Operation    string `json:"operation"`            // "pull", "swarm-pull", "chunk"
	Direction    string `json:"direction,omitempty"`  // "download" | "upload"
	ChunkIndex   int    `json:"chunkIndex,omitempty"` // preenchido apenas em download chunked
	TotalChunks  int    `json:"totalChunks,omitempty"`
	Done         bool   `json:"done"`
	Error        string `json:"error,omitempty"`
}

// emitTransferProgress emite um evento Wails p2p:transfer-progress para o frontend.
func (c *p2pCoordinator) emitTransferProgress(p p2pTransferProgress) {
	if c.app == nil || c.app.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(c.app.ctx, "p2p:transfer-progress", p)
}

// emitTransferDone emite evento de conclusão e limpa o progresso.
func (c *p2pCoordinator) emitTransferDone(artifactName, peerID, operation string, err error) {
	p := p2pTransferProgress{
		ArtifactName: artifactName,
		PeerID:       peerID,
		Operation:    operation,
		Done:         true,
	}
	if err != nil {
		p.Error = err.Error()
	}
	c.emitTransferProgress(p)
}

// progressReader é um io.Reader que reporta progresso via callback.
type progressReader struct {
	r      io.Reader
	onRead func(readSoFar int64)
	total  int64
	read   atomic.Int64
}

func newProgressReader(r io.Reader, total int64, onRead func(readSoFar int64)) *progressReader {
	pr := &progressReader{r: r, total: total, onRead: onRead}
	return pr
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.read.Add(int64(n))
		if pr.onRead != nil {
			pr.onRead(pr.read.Load())
		}
	}
	return n, err
}
