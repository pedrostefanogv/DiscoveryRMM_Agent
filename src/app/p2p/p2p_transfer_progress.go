package p2p

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
func (c *Coordinator) emitTransferProgress(p p2pTransferProgress) {
	if c.deps == nil {
		return
	}
	c.deps.EmitEvent("p2p:transfer-progress", p)
}

// emitTransferDone emite evento de conclusão e limpa o progresso.
func (c *Coordinator) emitTransferDone(artifactName, peerID, operation string, err error) {
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

// completedChunksBytes soma o tamanho real dos chunks concluídos.
// O último chunk geralmente é menor que manifest.ChunkSize, então usar
// completed*ChunkSize faria a barra "pular" no final. Este helper garante
// progresso monotônico e preciso.
//
// IMPORTANTE: os chunks são baixados em paralelo e completam FORA de ordem
// (ex.: chunk 5 pode chegar antes do chunk 2). Por isso, em vez de somar
// Chunks[0..completed-1] (que assumiria ordem sequencial e daria progresso
// incorreto), recebemos um mapa dos índices efetivamente concluídos e somamos
// apenas os tamanhos desses chunks.
func completedChunksBytes(manifest P2PChunkManifest, completedIdx map[int]bool) int64 {
	var bytesRead int64
	for idx := range completedIdx {
		if idx >= 0 && idx < len(manifest.Chunks) {
			bytesRead += manifest.Chunks[idx].Size
		}
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
