package fileserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Transfer gerencia upload/download chunked com resume.
type Transfer struct {
	basePath string
}

func NewTransfer(basePath string) *Transfer {
	return &Transfer{basePath: filepath.Clean(basePath)}
}

// ChunkResponse representa um chunk de arquivo.
type ChunkResponse struct {
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
	ChunkIndex int    `json:"chunkIndex"`
	TotalChunks int   `json:"totalChunks"`
	Data       []byte `json:"data,omitempty"`
	ChunkSize  int    `json:"chunkSize"`
	TotalSize  int64  `json:"totalSize"`
}

// GetFileChunked retorna um arquivo em chunks.
func (t *Transfer) GetFileChunked(raw []byte) []byte {
	var req struct {
		Path       string `json:"path"`
		ChunkIndex int    `json:"chunkIndex"`
		ChunkSize  int    `json:"chunkSize"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return t.errorChunk(err.Error())
	}
	if req.ChunkSize <= 0 {
		req.ChunkSize = 256 << 10 // 256KB default
	}

	fullPath := t.safePath(req.Path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return t.errorChunk("arquivo nao encontrado: " + err.Error())
	}

	totalChunks := int((info.Size() + int64(req.ChunkSize) - 1) / int64(req.ChunkSize))
	if req.ChunkIndex >= totalChunks {
		return t.errorChunk(fmt.Sprintf("chunk %d fora do range (0-%d)", req.ChunkIndex, totalChunks-1))
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return t.errorChunk("abrir arquivo: " + err.Error())
	}
	defer f.Close()

	offset := int64(req.ChunkIndex * req.ChunkSize)
	if _, err := f.Seek(offset, 0); err != nil {
		return t.errorChunk("seek: " + err.Error())
	}

	buf := make([]byte, req.ChunkSize)
	n, _ := f.Read(buf)

	resp := ChunkResponse{
		Success:     true,
		ChunkIndex:  req.ChunkIndex,
		TotalChunks: totalChunks,
		Data:        buf[:n],
		ChunkSize:   n,
		TotalSize:   info.Size(),
	}
	b, _ := json.Marshal(resp)
	return b
}

// PutFileChunked recebe um chunk e o escreve (resume).
func (t *Transfer) PutFileChunked(raw []byte) []byte {
	var req ChunkResponse
	if err := json.Unmarshal(raw, &req); err != nil {
		return t.errorChunk(err.Error())
	}

	fullPath := t.safePath(req.Path)
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return t.errorChunk("criar diretorio: " + err.Error())
	}

	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return t.errorChunk("abrir arquivo: " + err.Error())
	}
	defer f.Close()

	offset := int64(req.ChunkIndex * req.TotalChunks)
	if _, err := f.Seek(offset, 0); err != nil {
		return t.errorChunk("seek: " + err.Error())
	}

	if _, err := f.Write(req.Data); err != nil {
		return t.errorChunk("escrever: " + err.Error())
	}

	resp := ChunkResponse{Success: true, ChunkIndex: req.ChunkIndex}
	b, _ := json.Marshal(resp)
	return b
}

func (t *Transfer) safePath(rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	clean = strings.TrimLeft(clean, "/\\")
	full := filepath.Join(t.basePath, clean)
	if !strings.HasPrefix(filepath.Clean(full), t.basePath) {
		return t.basePath
	}
	return full
}

func (t *Transfer) errorChunk(msg string) []byte {
	r := ChunkResponse{Success: false, Error: msg}
	b, _ := json.Marshal(r)
	return b
}

var _ = fmt.Println
