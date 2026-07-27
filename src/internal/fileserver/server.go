package fileserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileInfo representa metadados de um arquivo/diretorio.
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// FileRequest representa uma requisicao de arquivo (list/get/put/delete).
type FileRequest struct {
	Action string `json:"action"` // list, get, put, delete
	Path   string `json:"path"`   // caminho relativo ao diretorio base
	Data   []byte `json:"data,omitempty"` // conteudo para upload
}

// FileResponse representa uma resposta.
type FileResponse struct {
	Success bool       `json:"success"`
	Error   string     `json:"error,omitempty"`
	Files   []FileInfo `json:"files,omitempty"`
	Data    []byte     `json:"data,omitempty"`
	Size    int64      `json:"size,omitempty"`
}

// Server gerencia transferencia de arquivos com sandboxing.
type Server struct {
	basePath string // diretorio raiz permitido
}

// NewServer cria um novo servidor de arquivos.
func NewServer(basePath string) *Server {
	return &Server{basePath: filepath.Clean(basePath)}
}

// HandleRequest processa uma requisicao de arquivo.
func (s *Server) HandleRequest(raw []byte) []byte {
	var req FileRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.errorResponse("payload json invalido: " + err.Error())
	}

	switch req.Action {
	case "list":
		return s.handleList(req.Path)
	case "get":
		return s.handleGet(req.Path)
	case "put":
		return s.handlePut(req.Path, req.Data)
	case "delete":
		return s.handleDelete(req.Path)
	default:
		return s.errorResponse("acao desconhecida: " + req.Action)
	}
}

func (s *Server) handleList(path string) []byte {
	dir := s.safePath(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s.errorResponse("listar diretorio: " + err.Error())
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	return s.okResponse(files, nil, 0)
}

func (s *Server) handleGet(path string) []byte {
	fullPath := s.safePath(path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return s.errorResponse("ler arquivo: " + err.Error())
	}

	info, _ := os.Stat(fullPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	// Chunked: arquivos > 1MB sao enviados em chunks
	if size > 1<<20 {
		return s.okResponse(nil, data[:1<<20], size) // primeiro chunk
	}

	return s.okResponse(nil, data, size)
}

func (s *Server) handlePut(path string, data []byte) []byte {
	fullPath := s.safePath(path)

	// Garante que diretorio pai existe
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return s.errorResponse("criar diretorio: " + err.Error())
	}

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return s.errorResponse("escrever arquivo: " + err.Error())
	}

	return s.okResponse(nil, nil, int64(len(data)))
}

func (s *Server) handleDelete(path string) []byte {
	fullPath := s.safePath(path)
	if err := os.Remove(fullPath); err != nil {
		return s.errorResponse("remover arquivo: " + err.Error())
	}
	return s.okResponse(nil, nil, 0)
}

// safePath resolve o caminho e previne path traversal.
func (s *Server) safePath(rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	clean = strings.TrimLeft(clean, "/\\")
	full := filepath.Join(s.basePath, clean)
	// Garante que nao escapa do diretorio base
	if !strings.HasPrefix(filepath.Clean(full), s.basePath) {
		return s.basePath
	}
	return full
}

func (s *Server) okResponse(files []FileInfo, data []byte, size int64) []byte {
	resp := FileResponse{Success: true, Files: files, Data: data, Size: size}
	b, _ := json.Marshal(resp)
	return b
}

func (s *Server) errorResponse(msg string) []byte {
	resp := FileResponse{Success: false, Error: msg}
	b, _ := json.Marshal(resp)
	return b
}

// Ensure imports
var _ = fmt.Println
var _ = os.DevNull
var _ = filepath.Separator
