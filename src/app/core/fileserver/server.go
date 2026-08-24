package fileserver

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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
	Action      string `json:"action"` // list|get|put|delete|rename|mkdir|move|copy|stat|zip|unzip
	Path        string `json:"path"`
	NewPath     string `json:"newPath,omitempty"` // rename/move/copy destino / zip destino / unzip destino
	Paths       []string `json:"paths,omitempty"` // zip múltiplo (origens)
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
	// progressFn é chamado durante operações longas (copy/move/zip) para
	// reportar progresso (bytes processados / total). Pode ser nil.
	progressFn func(loaded, total int64)
}

// NewServer cria um novo servidor de arquivos.
func NewServer(basePath string) *Server {
	return &Server{basePath: filepath.Clean(basePath)}
}

// SetProgressCallback define o callback de progresso para operações longas.
func (s *Server) SetProgressCallback(fn func(loaded, total int64)) {
	s.progressFn = fn
}

// reportProgress invoca o callback de progresso, se definido.
func (s *Server) reportProgress(loaded, total int64) {
	if s.progressFn != nil {
		s.progressFn(loaded, total)
	}
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
		resp := s.dispatch(legacy.Action, legacy.Path, "", legacy.Data, nil, 0, 0, nil)
		// Converte para FileResponse legado
		legacyResp := FileResponse{Success: resp.Success, Error: resp.Error, Files: resp.Entries, Data: resp.Data, Size: resp.Size}
		b, _ := json.Marshal(legacyResp)
		return b
	}

	return s.marshal(s.dispatch(req.Action, req.Path, req.NewPath, req.Data, req.ChunkIndex, req.ChunkSize, req.TotalChunks, req.Paths))
}

func (s *Server) dispatch(action, path, newPath string, data []byte, chunkIndex *int, chunkSize, totalChunks int, paths []string) FileSessionResponse {
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
	case "zip":
		resp = s.handleZipMany(path, newPath, paths)
	case "unzip":
		resp = s.handleUnzip(path, newPath)
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
//
// O upload é feito de forma "atomica": todo o conteudo e gravado num arquivo
// temporario (<destino>.tmp) na mesma pasta do destino. Somente quando o
// ultimo chunk e recebido (chunkIndex+1 >= totalChunks) ou em uploads de um
// unico chunk (chunkIndex == nil) o arquivo e renomeado para o nome final.
// Isso evita que ferramentas (Defender, Explorer, instaladores, etc.) abram
// um arquivo parcial com o nome definitivo (.exe) durante a transferencia,
// e tambem impede que um upload novo menor deixe restos de um arquivo
// anterior maior (o truncamento agora ocorre somente no .tmp).
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

	// Destino temporario na mesma pasta do destino final (rename atômico e
	// sem cruzar volume). Derivado apenas do caminho (nao do requestId, que
	// muda a cada chunk no viewer) para que chunks do mesmo upload reusem o
	// mesmo .tmp. O frontend envia o `path` final real; o .tmp e interno.
	tmpPath := fullPath + ".tmp"

	// Garante que um .tmp orfão de um upload anterior abortado seja removido
	// quando um novo upload do mesmo arquivo começar pelo chunk 0.
	if chunkIndex != nil && *chunkIndex == 0 {
		_ = os.Remove(tmpPath)
	}

	if chunkIndex == nil {
		// Upload único (arquivo pequeno): escreve no .tmp e renomeia.
		if err := os.WriteFile(tmpPath, data, 0644); err != nil {
			_ = os.Remove(tmpPath)
			return s.errorResponse("", "escrever arquivo: "+err.Error())
		}
		if err := os.Rename(tmpPath, fullPath); err != nil {
			_ = os.Remove(tmpPath)
			return s.errorResponse("", "finalizar upload: "+err.Error())
		}
		return s.okResponse(nil, nil, int64(len(data)), 0, 1)
	}

	if chunkSize <= 0 {
		chunkSize = 256 << 10
	}

	// Validação defensiva: totalChunks zerado é tratado como 1 (chamadores
	// legados); um chunkIndex fora do range indica inconsistência do viewer e
	// deve ser rejeitado — renomear aqui renomearia um arquivo incompleto.
	if totalChunks <= 0 {
		totalChunks = 1
	}
	if *chunkIndex >= totalChunks {
		return s.errorResponse("", fmt.Sprintf("chunk %d fora do range (0-%d)", *chunkIndex, totalChunks-1))
	}

	// Chunk 0: trunca o .tmp (remove restos de uploads anteriores).
	// Chunk > 0 (resume): abre o .tmp que ja existe SEM O_CREATE — se o .tmp
	// nao existir, significa que o upload anterior foi abortado/limpo; cria-lo
	// do zero aqui geraria um arquivo com "buracos" no disco. Forcar o viewer
	// a recomecar do chunk 0 e o comportamento correto.
	flags := os.O_WRONLY
	if *chunkIndex == 0 {
		flags |= os.O_CREATE | os.O_TRUNC
	}

	f, err := os.OpenFile(tmpPath, flags, 0644)
	if err != nil {
		if *chunkIndex > 0 && os.IsNotExist(err) {
			return s.errorResponse("", "upload interrompido: recomece do chunk 0")
		}
		return s.errorResponse("", "abrir arquivo: "+err.Error())
	}

	offset := int64(*chunkIndex * chunkSize)
	if _, err := f.Seek(offset, 0); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return s.errorResponse("", "seek: "+err.Error())
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return s.errorResponse("", "escrever chunk: "+err.Error())
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return s.errorResponse("", "fechar arquivo: "+err.Error())
	}

	// Ultimo chunk: renomeia o .tmp para o nome final (arquivo completo).
	if *chunkIndex+1 >= totalChunks {
		if err := os.Rename(tmpPath, fullPath); err != nil {
			_ = os.Remove(tmpPath)
			return s.errorResponse("", "finalizar upload: "+err.Error())
		}
	}

	return s.okResponse(nil, nil, int64(len(data)), *chunkIndex, totalChunks)
}

func (s *Server) handleDelete(path string) FileSessionResponse {
	fullPath, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}

	// Limpa um eventual .tmp órfão (upload cancelado/abortado) associado ao
	// arquivo. Se o .tmp estiver a ser escrito por um upload concorrente, o
	// os.Remove falha silenciosamente (arquivo em uso no Windows) — aceitável.
	removedTmp := false
	if _, terr := os.Stat(fullPath + ".tmp"); terr == nil {
		if err := os.Remove(fullPath + ".tmp"); err == nil {
			removedTmp = true
		}
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		// Arquivo final não existe. Se havia um .tmp órfão (upload cancelado no
		// meio, onde o arquivo final ainda não foi criado) e ele foi removido,
		// o objetivo — não deixar resíduo — foi atingido: retorna sucesso.
		if removedTmp {
			return s.okResponse(nil, nil, 0, 0, 0)
		}
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
		// Fallback cross-volume (EXDEV): copia e remove a origem. Em Windows,
		// os.Rename usa MoveFileEx (já suporta cross-volume), mas este fallback
		// cobre volumes/montagens onde MoveFileEx falha.
		if isCrossDeviceError(err) {
			info, statErr := os.Stat(oldFull)
			if statErr != nil {
				return s.errorResponse("", "mover: "+err.Error())
			}
			var total int64
			if info.IsDir() {
				total = dirSize(oldFull)
			} else {
				total = info.Size()
			}
			var loaded int64
			if info.IsDir() {
				if copyErr := copyDir(oldFull, newFull, &loaded, total, s.reportProgress); copyErr != nil {
					return s.errorResponse("", "mover (copiar): "+copyErr.Error())
				}
			} else {
				if copyErr := copyFile(oldFull, newFull, &loaded, total, s.reportProgress); copyErr != nil {
					return s.errorResponse("", "mover (copiar): "+copyErr.Error())
				}
			}
			s.reportProgress(total, total)
			if info.IsDir() {
				if rmErr := os.RemoveAll(oldFull); rmErr != nil {
					return s.errorResponse("", "mover (remover origem): "+rmErr.Error())
				}
			} else {
				if rmErr := os.Remove(oldFull); rmErr != nil {
					return s.errorResponse("", "mover (remover origem): "+rmErr.Error())
				}
			}
			return s.okResponse(nil, nil, 0, 0, 0)
		}
		return s.errorResponse("", "mover: "+err.Error())
	}
	return s.okResponse(nil, nil, 0, 0, 0)
}

// isCrossDeviceError detecta erro de cross-device (EXDEV) em os.Rename.
// Em Windows, MoveFileEx normalmente resolve; em Unix, EXDEV é o sinal típico.
func isCrossDeviceError(err error) bool {
	if err == nil {
		return false
	}
	if errno, ok := err.(interface{ Is(error) bool }); ok {
		// Erros syscall modernos (Go 1.13+) expõem Is.
		return errno.Is(syscall.EXDEV)
	}
	// Fallback por string (portável entre plataformas).
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cross-device") ||
		strings.Contains(msg, "different device") ||
		strings.Contains(msg, "cannot move") ||
		strings.Contains(msg, "no está permitido") ||
		strings.Contains(msg, "invalid cross-device")
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

	// Total de bytes a copiar (para progresso).
	var total int64
	if info.IsDir() {
		total = dirSize(src)
	} else {
		total = info.Size()
	}
	var loaded int64

	if info.IsDir() {
		if err := copyDir(src, dst, &loaded, total, s.reportProgress); err != nil {
			return s.errorResponse("", "copiar diretorio: "+err.Error())
		}
	} else {
		if err := copyFile(src, dst, &loaded, total, s.reportProgress); err != nil {
			return s.errorResponse("", "copiar arquivo: "+err.Error())
		}
	}
	s.reportProgress(total, total)
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

// handleZipMany compacta um ou mais arquivos/diretorios em um arquivo .zip.
// path = origem (compat. legada, quando paths estiver vazio).
// paths = origens (suporta múltiplas).
// newPath = destino do .zip (ex: C:\x\foo.zip).
func (s *Server) handleZipMany(path, newPath string, paths []string) FileSessionResponse {
	if newPath == "" {
		return s.errorResponse("", "zip requer newPath (destino .zip)")
	}
	if len(paths) == 0 && path != "" {
		paths = []string{path}
	}
	if len(paths) == 0 {
		return s.errorResponse("", "zip requer ao menos uma origem")
	}

	dst, err := s.safePath(newPath)
	if err != nil {
		return s.errorResponse("", err.Error())
	}

	// Garante extensão .zip no destino.
	if !strings.HasSuffix(strings.ToLower(dst), ".zip") {
		dst += ".zip"
	}

	// Resolve todas as origens antes de criar o zip.
	type srcEntry struct {
		full string
		name string
	}
	var srcs []srcEntry
	for _, p := range paths {
		full, err := s.safePath(p)
		if err != nil {
			return s.errorResponse("", err.Error())
		}
		if _, err := os.Stat(full); err != nil {
			return s.errorResponse("", "zip: "+err.Error())
		}
		// Nome dentro do zip: para múltiplos itens, usa apenas o base (achata),
		// preservando a hierarquia interna de cada pasta.
		srcs = append(srcs, srcEntry{full: full, name: filepath.Base(full)})
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return s.errorResponse("", "zip: criar destino: "+err.Error())
	}

	f, err := os.Create(dst)
	if err != nil {
		return s.errorResponse("", "zip: criar arquivo: "+err.Error())
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	var totalBytes int64
	for _, src := range srcs {
		info, err := os.Stat(src.full)
		if err != nil {
			_ = zw.Close()
			_ = os.Remove(dst)
			return s.errorResponse("", "zip: "+err.Error())
		}
		if info.IsDir() {
			totalBytes += dirSize(src.full)
		} else {
			totalBytes += info.Size()
		}
	}
	var loadedBytes int64
	for _, src := range srcs {
		info, err := os.Stat(src.full)
		if err != nil {
			_ = zw.Close()
			_ = os.Remove(dst)
			return s.errorResponse("", "zip: "+err.Error())
		}
		if info.IsDir() {
			n, err := zipDirProgress(zw, src.full, src.name, &loadedBytes, totalBytes, s.reportProgress)
			loadedBytes = n
			if err != nil {
				_ = zw.Close()
				_ = os.Remove(dst)
				return s.errorResponse("", "zip: "+err.Error())
			}
		} else {
			n, err := zipFileProgress(zw, src.full, src.name, &loadedBytes, totalBytes, s.reportProgress)
			loadedBytes = n
			if err != nil {
				_ = zw.Close()
				_ = os.Remove(dst)
				return s.errorResponse("", "zip: "+err.Error())
			}
		}
	}
	if err := zw.Close(); err != nil {
		_ = os.Remove(dst)
		return s.errorResponse("", "zip: fechar: "+err.Error())
	}
	s.reportProgress(totalBytes, totalBytes)

	return s.okResponse(nil, nil, 0, 0, 0)
}

// handleUnzip extrai um arquivo .zip para um diretorio destino.
// path = arquivo .zip; newPath = diretorio destino (criado se nao existir).
func (s *Server) handleUnzip(path, newPath string) FileSessionResponse {
	if newPath == "" {
		return s.errorResponse("", "unzip requer newPath (diretorio destino)")
	}
	src, err := s.safePath(path)
	if err != nil {
		return s.errorResponse("", err.Error())
	}
	dst, err := s.safePath(newPath)
	if err != nil {
		return s.errorResponse("", err.Error())
	}

	zr, err := zip.OpenReader(src)
	if err != nil {
		return s.errorResponse("", "unzip: abrir: "+err.Error())
	}
	defer zr.Close()

	if err := os.MkdirAll(dst, 0755); err != nil {
		return s.errorResponse("", "unzip: criar destino: "+err.Error())
	}

	// Total de bytes a extrair (soma dos tamanhos não-compactados).
	var totalBytes int64
	for _, zf := range zr.File {
		totalBytes += int64(zf.UncompressedSize64)
	}
	var loadedBytes int64

	for _, zf := range zr.File {
		// Previne path traversal dentro do zip (zip-slip).
		cleanName := filepath.Clean(zf.Name)
		if cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanName) {
			return s.errorResponse("", "unzip: entrada invalida no zip: "+zf.Name)
		}
		target := filepath.Join(dst, cleanName)
		// Garante que o destino permanece dentro de dst.
		if rel, err := filepath.Rel(dst, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return s.errorResponse("", "unzip: entrada fora do destino: "+zf.Name)
		}

		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return s.errorResponse("", "unzip: criar dir: "+err.Error())
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return s.errorResponse("", "unzip: criar dir pai: "+err.Error())
		}
		rc, err := zf.Open()
		if err != nil {
			return s.errorResponse("", "unzip: abrir entrada: "+err.Error())
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			return s.errorResponse("", "unzip: criar arquivo: "+err.Error())
		}
		n, err := io.Copy(out, rc)
		_ = rc.Close()
		_ = out.Close()
		if err != nil {
			return s.errorResponse("", "unzip: extrair: "+err.Error())
		}
		loadedBytes += n
		s.reportProgress(loadedBytes, totalBytes)
	}
	s.reportProgress(totalBytes, totalBytes)

	return s.okResponse(nil, nil, 0, 0, 0)
}

// zipFile adiciona um arquivo ao zip, reportando progresso.
func zipFileProgress(zw *zip.Writer, src, name string, loaded *int64, total int64, report func(int64, int64)) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return *loaded, err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return *loaded, err
	}
	hdr.Name = filepath.ToSlash(name)
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return *loaded, err
	}
	f, err := os.Open(src)
	if err != nil {
		return *loaded, err
	}
	defer f.Close()
	n, err := io.Copy(w, f)
	*loaded += n
	if report != nil {
		report(*loaded, total)
	}
	return *loaded, err
}

// zipDir adiciona um diretorio (recursivo) ao zip, reportando progresso.
func zipDirProgress(zw *zip.Writer, dir, base string, loaded *int64, total int64, report func(int64, int64)) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return *loaded, err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(dir, entry.Name())
		name := filepath.Join(base, entry.Name())
		if entry.IsDir() {
			if _, err := zipDirProgress(zw, srcPath, name, loaded, total, report); err != nil {
				return *loaded, err
			}
		} else {
			if _, err := zipFileProgress(zw, srcPath, name, loaded, total, report); err != nil {
				return *loaded, err
			}
		}
	}
	return *loaded, nil
}

// dirSize soma o tamanho de todos os arquivos dentro de um diretorio (recursivo).
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
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

// copyFile copia um arquivo preservando permissões, reportando progresso.
func copyFile(src, dst string, loaded *int64, total int64, report func(int64, int64)) error {
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

	n, err := io.Copy(out, in)
	if err != nil {
		return err
	}
	*loaded += n
	if report != nil {
		report(*loaded, total)
	}
	info, err := in.Stat()
	if err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return out.Sync()
}

// copyDir copia um diretorio recursivamente, reportando progresso.
func copyDir(src, dst string, loaded *int64, total int64, report func(int64, int64)) error {
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
			if err := copyDir(srcPath, dstPath, loaded, total, report); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath, loaded, total, report); err != nil {
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
