package fileserver

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// FileSessionRequest representa uma requisicao de arquivo versionada (v1).
// O requestId permite ao viewer correlacionar a resposta com a requisicao.
type FileSessionRequest struct {
	Version     int    `json:"version"`
	RequestID   string `json:"requestId"`
	Action      string `json:"action"` // list|get|put|delete|rename|mkdir|move|copy|stat
	Path        string `json:"path"`
	NewPath     string `json:"newPath,omitempty"` // rename/move/copy destino
	Data        []byte `json:"data,omitempty"`
	ChunkIndex  *int   `json:"chunkIndex,omitempty"` // nil = sem chunk (arquivo inteiro)
	ChunkSize   int    `json:"chunkSize,omitempty"`
	TotalChunks int    `json:"totalChunks,omitempty"`
}

// FileSessionResponse representa uma resposta versionada (v1).
type FileSessionResponse struct {
	Version     int        `json:"version"`
	RequestID   string     `json:"requestId"`
	Success     bool       `json:"success"`
	Error       string     `json:"error,omitempty"`
	Entries     []FileInfo `json:"entries"` // sempre serializado ([] quando vazio)
	Data        []byte     `json:"data,omitempty"`
	Size        int64      `json:"size,omitempty"`
	ChunkIndex  int        `json:"chunkIndex,omitempty"`
	TotalChunks int        `json:"totalChunks,omitempty"`
}

// FileRequest representa uma requisicao de arquivo legada (list/get/put/delete).
// Mantido para compatibilidade com chamadores antigos.
type FileRequest struct {
	Action string `json:"action"` // list, get, put, delete
	Path   string `json:"path"`   // caminho relativo ao diretorio base
	Data   []byte `json:"data,omitempty"` // conteudo para upload
}

// FileResponse representa uma resposta legada.
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

// RootPath retorna o diretório raiz (sandbox) do servidor.
func (s *Server) RootPath() string {
	return s.basePath
}

// HandleRequest processa uma requisicao de arquivo (protocolo versionado v1).
// Aceita tanto FileSessionRequest (com requestId) quanto FileRequest legado.
func (s *Server) HandleRequest(raw []byte) []byte {
	var req FileSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.marshal(s.errorResponse("", "payload json invalido: "+err.Error()))
	}

	// Fallback legado: se nao veio version/requestId, tenta FileRequest
	if req.Version == 0 && req.Action == "" {
		var legacy FileRequest
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return s.marshal(s.errorResponse("", "payload json invalido: "+err.Error()))
		}
		resp := s.dispatch(legacy.Action, legacy.Path, "", legacy.Data, nil, 0, 0)
		// Converte para FileResponse legado
		legacyResp := FileResponse{Success: resp.Success, Error: resp.Error, Files: resp.Entries, Data: resp.Data, Size: resp.Size}
		b, _ := json.Marshal(legacyResp)
		return b
	}

	return s.marshal(s.dispatch(req.Action, req.Path, req.NewPath, req.Data, req.ChunkIndex, req.ChunkSize, req.TotalChunks))
}

func (s *Server) dispatch(action, path, newPath string, data []byte, chunkIndex *int, chunkSize, totalChunks int) FileSessionResponse {
	var resp FileSessionResponse
	switch action {
	case "list":
		resp = s.handleList(path)
	case "get":
		resp = s.handleGet(path, chunkIndex, chunkSize)
	case "put":
		resp = s.handlePut(path, data, chunkIndex, chunkSize, totalChunks)
	case "delete":
		resp = s.handleDelete(path)
	case "rename":
		resp = s.handleRename(path, newPath)
	case "mkdir":
		resp = s.handleMkdir(path)
	case "move":
		resp = s.handleMove(path, newPath)
	case "copy":
		resp = s.handleCopy(path, newPath)
	case "stat":
		resp = s.handleStat(path)
	default:
		resp = s.errorResponse("", "acao desconhecida: "+action)
	}
	return resp
}

func (s *Server) marshal(resp FileSessionResponse) []byte {
	b, _ := json.Marshal(resp)
	return b
}

func (s *Server) handleList(path string) FileSessionResponse {
	dir, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s.errorResponse("", "listar diretorio: "+err.Error())
	}

	// Ordena: diretorios primeiro, depois arquivos, ambos alfabeticamente.
	// (os.ReadDir ja ordena por nome, mas nao garante diretorios primeiro.)
	files := make([]FileInfo, 0, len(entries))
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

	sort.SliceStable(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	return s.okResponse(files, nil, 0, 0, 0)
}

// handleGet retorna um arquivo. Se chunkIndex != nil, retorna apenas o chunk
// solicitado (chunked download com resume). Caso contrario (nil), retorna o
// arquivo inteiro se <= 1MB, ou o primeiro chunk se maior.
func (s *Server) handleGet(path string, chunkIndex *int, chunkSize int) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return s.errorResponse("", "ler arquivo: "+err.Error())
	}
	if info.IsDir() {
		return s.errorResponse("", "e um diretorio: "+path)
	}

	if chunkSize <= 0 {
		chunkSize = 256 << 10 // 256KB default
	}
	totalChunks := int((info.Size() + int64(chunkSize) - 1) / int64(chunkSize))
	if totalChunks < 1 {
		totalChunks = 1
	}

	// Sem chunkIndex: arquivo pequeno retorna inteiro; grande retorna primeiro chunk
	if chunkIndex == nil {
		if info.Size() <= 1<<20 {
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return s.errorResponse("", "ler arquivo: "+err.Error())
			}
			return s.okResponse(nil, data, info.Size(), 0, 1)
		}
		zero := 0
		chunkIndex = &zero
	}

	if *chunkIndex >= totalChunks {
		return s.errorResponse("", fmt.Sprintf("chunk %d fora do range (0-%d)", *chunkIndex, totalChunks-1))
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return s.errorResponse("", "abrir arquivo: "+err.Error())
	}
	defer f.Close()

	offset := int64(*chunkIndex * chunkSize)
	if _, err := f.Seek(offset, 0); err != nil {
		return s.errorResponse("", "seek: "+err.Error())
	}

	buf := make([]byte, chunkSize)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return s.errorResponse("", "ler chunk: "+err.Error())
	}

	return s.okResponse(nil, buf[:n], info.Size(), *chunkIndex, totalChunks)
}

// handlePut escreve um arquivo. Com chunkIndex != nil, escreve o chunk na
// posicao correta (upload chunked com resume). Sem chunkIndex (nil), escreve
// o arquivo inteiro de uma vez.
func (s *Server) handlePut(path string, data []byte, chunkIndex *int, chunkSize, totalChunks int) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}

	// Garante que diretorio pai existe
	parent := filepath.Dir(fullPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return s.errorResponse("", "criar diretorio: "+err.Error())
	}

	if chunkIndex == nil {
		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			return s.errorResponse("", "escrever arquivo: "+err.Error())
		}
		return s.okResponse(nil, nil, int64(len(data)), 0, 1)
	}

	if chunkSize <= 0 {
		chunkSize = 256 << 10
	}

	// Chunk 0: trunca o arquivo (remove restos de uploads anteriores).
	// Chunks seguintes: append/seek sem truncar.
	flags := os.O_CREATE | os.O_WRONLY
	if *chunkIndex == 0 {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(fullPath, flags, 0644)
	if err != nil {
		return s.errorResponse("", "abrir arquivo: "+err.Error())
	}
	defer f.Close()

	offset := int64(*chunkIndex * chunkSize)
	if _, err := f.Seek(offset, 0); err != nil {
		return s.errorResponse("", "seek: "+err.Error())
	}

	if _, err := f.Write(data); err != nil {
		return s.errorResponse("", "escrever chunk: "+err.Error())
	}

	if totalChunks <= 0 {
		totalChunks = 1
	}
	return s.okResponse(nil, nil, int64(len(data)), *chunkIndex, totalChunks)
}

func (s *Server) handleDelete(path string) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return s.errorResponse("", "remover: "+err.Error())
	}
	if info.IsDir() {
		if err := os.RemoveAll(fullPath); err != nil {
			return s.errorResponse("", "remover diretorio: "+err.Error())
		}
	} else {
		if err := os.Remove(fullPath); err != nil {
			return s.errorResponse("", "remover arquivo: "+err.Error())
		}
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

func (s *Server) handleRename(path, newPath string) FileSessionResponse {
	if newPath == "" {
		return s.errorResponse("", "rename requer newPath")
	}
	oldFull, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	newFull, err := s.safePath(newPath)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		return s.errorResponse("", "renomear: "+err.Error())
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

func (s *Server) handleMkdir(path string) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return s.errorResponse("", "criar diretorio: "+err.Error())
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

func (s *Server) handleMove(path, newPath string) FileSessionResponse {
	if newPath == "" {
		return s.errorResponse("", "move requer newPath")
	}
	oldFull, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	newFull, err := s.safePath(newPath)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	// Garante diretorio pai do destino
	if err := os.MkdirAll(filepath.Dir(newFull), 0755); err != nil {
		return s.errorResponse("", "criar diretorio destino: "+err.Error())
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		return s.errorResponse("", "mover: "+err.Error())
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

func (s *Server) handleCopy(path, newPath string) FileSessionResponse {
	if newPath == "" {
		return s.errorResponse("", "copy requer newPath")
	}
	src, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	dst, err := s.safePath(newPath)
	if err != nil {
		return s.errorResponse("", err.Error())
	}

	info, err := os.Stat(src)
	if err != nil {
		return s.errorResponse("", "copiar: "+err.Error())
	}

	if info.IsDir() {
		if err := copyDir(src, dst); err != nil {
			return s.errorResponse("", "copiar diretorio: "+err.Error())
		}
	} else {
		if err := copyFile(src, dst); err != nil {
			return s.errorResponse("", "copiar arquivo: "+err.Error())
		}
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

func (s *Server) handleStat(path string) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return s.errorResponse("", "stat: "+err.Error())
	}
	entries := []FileInfo{{
		Name:    info.Name(),
		Path:    path,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	}}
	return s.okResponse(entries, nil, info.Size(), 0, 0)
}

// safePath resolve o caminho e previne path traversal.
//
// Aceita caminhos relativos ("Users\\foo") e absolutos ("C:\\Users\\foo"),
// desde que permaneçam DENTRO do basePath. Usa filepath.Rel para validar
// de forma robusta (o prefixo ingênuo de string falha: base="C:\" casa com
// qualquer full="C:\..." e "..\..\" escapa do sandbox).
func (s *Server) safePath(rel string) (string, error) {
	if rel == "" {
		return s.basePath, nil
	}

	clean := filepath.Clean(strings.TrimSpace(rel))

	// Se o caminho é absoluto, usa diretamente (sem Join com basePath).
	// Senão, resolve contra basePath. (Join com absoluto produziria "C:\C:\...").
	var full string
	if filepath.IsAbs(clean) {
		full = clean
	} else {
		full = filepath.Join(s.basePath, clean)
	}

	// Valida que full está dentro de basePath via filepath.Rel:
	// retorna erro em volumes diferentes (ex: D:\ vs C:\) e ".." quando escapa.
	if relPath, err := filepath.Rel(s.basePath, full); err == nil {
		if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			return s.basePath, fmt.Errorf("path traversal bloqueado: %s", rel)
		}
		return full, nil
	}

	// Volume diferente ou erro — bloqueia
	return s.basePath, fmt.Errorf("caminho fora do sandbox: %s", rel)
}

func (s *Server) okResponse(files []FileInfo, data []byte, size int64, chunkIndex, totalChunks int) FileSessionResponse {
	// Garante entries serializado como [] (nunca null/ausente) — o frontend
	// trata `success=true` sem `entries` como erro; asterisco diretório vazio
	// deve resultar em lista vazia, não em erro.
	if files == nil {
		files = []FileInfo{}
	}
	return FileSessionResponse{
		Version:     1,
		Success:     true,
		Entries:     files,
		Data:        data,
		Size:        size,
		ChunkIndex:  chunkIndex,
		TotalChunks: totalChunks,
	}
}

func (s *Server) errorResponse(requestID, msg string) FileSessionResponse {
	return FileSessionResponse{
		Version:   1,
		RequestID: requestID,
		Success:   false,
		Error:     msg,
		Entries:   []FileInfo{},
	}
}

// copyFile copia um arquivo preservando permissões.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	info, err := in.Stat()
	if err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return out.Sync()
}

// copyDir copia um diretorio recursivamente.
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ensure imports
var _ = fmt.Println
var _ = os.DevNull
var _ = filepath.Separator
