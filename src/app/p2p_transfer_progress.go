package app

import (
	"io"
	"sync/atomic"
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
	// CompletedChunks é o número de chunks já concluídos com sucesso.
	// Diferente de BytesRead (que flutua por chunk), CompletedChunks é
	// monotônico e representa progresso real consolidado.
	CompletedChunks int    `json:"completedChunks,omitempty"`
	Phase           string `json:"phase,omitempty"` // "transfer", "assembling", "verifying", "done"
}

// emitTransferProgress emite um evento Wails p2p:transfer-progress para o frontend.
func (c *p2pCoordinator) emitTransferProgress(p p2pTransferProgress) {
	if c.deps == nil {
		return
	}
	c.deps.EmitEvent("p2p:transfer-progress", p)
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

// completedChunksBytes soma o tamanho real dos chunks concluídos (0..completed).
// O último chunk geralmente é menor que manifest.ChunkSize, então usar
// completed*ChunkSize faria a barra "pular" no final. Este helper garante
// progresso monotônico e preciso.
func completedChunksBytes(manifest P2PChunkManifest, completed int) int64 {
	var bytesRead int64
	for i := 0; i < completed && i < len(manifest.Chunks); i++ {
		bytesRead += manifest.Chunks[i].Size
	}
	return bytesRead
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
