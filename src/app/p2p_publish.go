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
	"time"

	"discovery/app/core/platform"
)

const (
	// p2pImportMaxBytesPerSec limita a taxa de leitura durante importação
	// para não saturar o disco. 100 MB/s mantém throughput razoável com
	// ~20-30% de headroom para outros processos.
	p2pImportMaxBytesPerSec = 100 << 20 // 100 MB/s
)

func (c *p2pCoordinator) ListArtifacts() ([]P2PArtifactView, error) {
	dir := c.deps.P2PTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	artifacts := make([]P2PArtifactView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := sanitizeArtifactName(entry.Name())
		if name == "" {
			continue
		}
		// Ignorar arquivos com sufixo .importing — ainda estão sendo copiados.
		if strings.HasSuffix(name, ".importing") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		checksum, err := c.cachedFileSHA256(path, info.ModTime())
		if err != nil {
			continue
		}
		artifacts = append(artifacts, P2PArtifactView{
			ArtifactID:       CanonicalArtifactID("", name, ""),
			ArtifactName:     name,
			Version:          "",
			SizeBytes:        info.Size(),
			ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
			ChecksumSHA256:   checksum,
			Available:        true,
			LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
		})
	}
	return artifacts, nil
}

// deleteArtifact remove um único artifact do diretório P2P:
// arquivo, manifest cacheado e entrada do sha256Cache.
func (c *p2pCoordinator) deleteArtifact(artifactName string) error {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return fmt.Errorf("nome de artifact inválido")
	}

	dir := c.deps.P2PTempDir()
	path := filepath.Join(dir, artifactName)

	// Remove o arquivo.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("falha ao remover arquivo: %w", err)
	}

	// Remove o manifest cacheado.
	if c.transferServer != nil {
		manifestDir := c.transferServer.manifestDir()
		if manifestDir != "" {
			manifestPath := cachedManifestPath(manifestDir, artifactName)
			if manifestPath != "" {
				_ = os.Remove(manifestPath)
			}
		}
	}

	// Remove o cache SHA256 em memória.
	c.sha256CacheMu.Lock()
	delete(c.sha256Cache, path)
	c.sha256CacheMu.Unlock()

	c.deps.Log(fmt.Sprintf("[p2p] artifact apagado: %s", artifactName))
	return nil
}

func (c *p2pCoordinator) PublishTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact inválido")
	}
	dir := c.deps.P2PTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	path := filepath.Join(dir, artifactName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return P2PArtifactView{}, err
	}
	_ = platform.EnsureWorldReadable(path)
	info, err := os.Stat(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()

	// Manifest gerado sob demanda (lazy) no primeiro download — test artifacts
	// são pequenos e raramente baixados por peers, não justifica eager build.

	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID("", artifactName, ""),
		ArtifactName:     artifactName,
		Version:          "",
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

func (c *p2pCoordinator) PublishFile(sourcePath string) (P2PArtifactView, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return P2PArtifactView{}, fmt.Errorf("arquivo nao informado")
	}
	artifactName := sanitizeArtifactName(filepath.Base(sourcePath))
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact invalido")
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return P2PArtifactView{}, err
	}

	dir := c.deps.P2PTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	targetPath := filepath.Join(dir, artifactName)

	checksum, manifest, err := c.publishFileSinglePass(sourcePath, targetPath, artifactName)
	if err != nil {
		c.emitPublishProgress(artifactName, 0, 0, true, err.Error())
		return P2PArtifactView{}, err
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if sourceInfo.Size() != info.Size() {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("arquivo importado com tamanho divergente")
	}

	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()

	// Cacheia o manifest gerado durante a cópia (evita rebuildChunkManifest).
	c.cacheManifestAfterSinglePass(artifactName, manifest)

	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID("", artifactName, ""),
		ArtifactName:     artifactName,
		Version:          "",
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

// PublishFileWithID publica um arquivo no diretório P2P com um ArtifactID
// explícito (ex.: GUID de release, "selfupdate:<sha256>") em vez de derivar do nome.
// O arquivo é copiado para o p2pTempDir com o artifactID como nome de arquivo
// (mantendo a extensão original).
// version: versão do artifact (opcional) — propaga no gossip para validação cross-peer.
func (c *p2pCoordinator) PublishFileWithID(sourcePath string, artifactID string) (P2PArtifactView, error) {
	return c.PublishFileWithIDAndVersion(sourcePath, artifactID, "")
}

// PublishFileWithIDAndVersion publica com versão explícita para validação no gossip.
func (c *p2pCoordinator) PublishFileWithIDAndVersion(sourcePath string, artifactID string, version string) (P2PArtifactView, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return P2PArtifactView{}, fmt.Errorf("arquivo nao informado")
	}
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return P2PArtifactView{}, fmt.Errorf("artifactID nao informado")
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return P2PArtifactView{}, err
	}

	dir := c.deps.P2PTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	// Nome do arquivo: artifactID sanitizado + extensão original (ex.: "rel-abc.exe").
	// Substitui ':' para evitar nomes inválidos no Windows (ex.: "selfupdate:<sha256>"
	// vira "selfupdate-<sha256>.exe").
	targetName := sanitizeArtifactName(strings.NewReplacer(":", "-").Replace(artifactID) + filepath.Ext(sourcePath))
	targetPath := filepath.Join(dir, targetName)

	checksum, manifest, err := c.publishFileSinglePass(sourcePath, targetPath, targetName)
	if err != nil {
		c.emitPublishProgress(targetName, 0, 0, true, err.Error())
		return P2PArtifactView{}, err
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if sourceInfo.Size() != info.Size() {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("arquivo importado com tamanho divergente")
	}

	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()

	// Cacheia o manifest gerado durante a cópia (evita rebuildChunkManifest).
	c.cacheManifestAfterSinglePass(targetName, manifest)

	return P2PArtifactView{
		ArtifactID:       artifactID,
		ArtifactName:     targetName,
		Version:          strings.TrimSpace(version),
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

// publishFileSinglePass copia sourcePath para targetPath enquanto calcula:
//   - SHA256 full-file (streaming)
//   - SHA256 por chunk (para o manifest)
//   - P2PChunkManifest completo
//
// Retorna o SHA256 e o manifest. Lê o arquivo UMA única vez.
// Usa buffer de 4 MB e throttling adaptativo baseado no cpuSampler.
func (c *p2pCoordinator) publishFileSinglePass(sourcePath, targetPath, artifactName string) (sha256Hex string, manifest P2PChunkManifest, err error) {
	// OpenFileSequential usa FILE_FLAG_SEQUENTIAL_SCAN no Windows para
	// reduzir pressão no cache de disco e prioridade de I/O.
	srcFile, err := platform.OpenFileSequential(sourcePath)
	if err != nil {
		return "", P2PChunkManifest{}, err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return "", P2PChunkManifest{}, err
	}
	totalSize := srcInfo.Size()

	tmpPath := targetPath + ".importing"
	dstFile, err := os.Create(tmpPath)
	if err != nil {
		return "", P2PChunkManifest{}, err
	}

	cleanupOnError := func() {
		dstFile.Close()
		os.Remove(tmpPath)
	}

	var chunkSize int64 = defaultChunkSizeBytes
	if cfg := c.deps.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
		chunkSize = cfg.ChunkSizeBytes
	}

	totalChunksEstimate := int(totalSize/chunkSize) + 1
	if totalChunksEstimate < 1 {
		totalChunksEstimate = 1
	}
	if totalChunksEstimate > 100000 {
		totalChunksEstimate = 100000
	}

	fullHash := sha256.New()
	chunks := make([]P2PChunk, 0, totalChunksEstimate)
	// Buffer do tamanho do chunk para que cada leitura gere exatamente um chunk
	// de chunkSize bytes (consistente com buildChunkManifest e manifest.ChunkSize).
	buf := make([]byte, chunkSize)
	multiWriter := io.MultiWriter(dstFile, fullHash)
	var offset int64

	// Rate limiter simples: controla bytes/s para não saturar o disco.
	var rateStart = time.Now()
	var rateBytes int64

	for {
		n, readErr := io.ReadFull(srcFile, buf)
		if n == 0 {
			break
		}
		data := buf[:n]

		if _, writeErr := multiWriter.Write(data); writeErr != nil {
			cleanupOnError()
			return "", P2PChunkManifest{}, writeErr
		}

		chunkHash := sha256.Sum256(data)
		chunks = append(chunks, P2PChunk{
			Index:  len(chunks),
			Offset: offset,
			Size:   int64(n),
			SHA256: hex.EncodeToString(chunkHash[:]),
		})
		offset += int64(n)
		rateBytes += int64(n)

		// Throttling adaptativo:
		// 1. Rate limiter de disco: limita bytes/s para não saturar o disco.
		// 2. CPU throttling: dorme se CPU > threshold.
		// 3. Progresso: emite evento p2p:publish:progress para o frontend.
		if len(chunks)%chunkProcessedThreshold == 0 {
			runtime.Gosched()

			// Emitir progresso para a UI.
			c.emitPublishProgress(artifactName, offset, totalSize, false, "")

			// Rate limiter: se excedeu p2pImportMaxBytesPerSec, dorme o necessário.
			elapsed := time.Since(rateStart)
			if elapsed > 0 && rateBytes > 0 {
				currentRate := float64(rateBytes) / elapsed.Seconds()
				if currentRate > float64(p2pImportMaxBytesPerSec) {
					targetDuration := time.Duration(float64(rateBytes) / float64(p2pImportMaxBytesPerSec) * float64(time.Second))
					if targetDuration > elapsed {
						time.Sleep(targetDuration - elapsed)
					}
				}
			}

			if cpu := c.cpuSampler.Sample(); cpu > cpuThrottleThreshold {
				time.Sleep(cpuThrottleSleep)
			}
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			cleanupOnError()
			return "", P2PChunkManifest{}, readErr
		}
	}

	// Sync garante que todos os bytes foram flushados para o disco antes do rename.
	// Evita que o arquivo fique incompleto se o sistema crashar e previne que AV
	// ou indexador abram o arquivo antes da finalização.
	if syncErr := dstFile.Sync(); syncErr != nil {
		dstFile.Close()
		os.Remove(tmpPath)
		return "", P2PChunkManifest{}, syncErr
	}
	if closeErr := dstFile.Close(); closeErr != nil {
		os.Remove(tmpPath)
		return "", P2PChunkManifest{}, closeErr
	}
	if err := platform.RenameAtomic(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		return "", P2PChunkManifest{}, err
	}
	_ = platform.EnsureWorldReadable(targetPath)

	// Emitir progresso final (100%).
	c.emitPublishProgress(artifactName, totalSize, totalSize, true, "")

	manifest = P2PChunkManifest{
		ArtifactID:   CanonicalArtifactID("", artifactName, ""),
		ArtifactName: artifactName,
		TotalSize:    totalSize,
		ChunkSize:    chunkSize,
		TotalChunks:  len(chunks),
		SHA256:       hex.EncodeToString(fullHash.Sum(nil)),
		SourceMTime:  srcInfo.ModTime().UnixNano(),
		Chunks:       chunks,
	}

	return manifest.SHA256, manifest, nil
}

// cacheManifestAfterSinglePass salva o manifest gerado durante o single-pass
// no cache de manifest, evitando que generateManifestEager precise re-ler o arquivo.
func (c *p2pCoordinator) cacheManifestAfterSinglePass(artifactName string, manifest P2PChunkManifest) {
	if c.transferServer == nil {
		return
	}
	manifestDir := c.transferServer.manifestDir()
	if manifestDir == "" {
		return
	}
	if err := saveCachedManifest(manifestDir, artifactName, manifest); err != nil {
		c.deps.Log(fmt.Sprintf("[p2p] aviso: falha ao salvar manifest para %s: %v", artifactName, err))
		return
	}
	c.deps.Log(fmt.Sprintf("[p2p] manifest gerado: %s chunks=%d size=%d", artifactName, manifest.TotalChunks, manifest.TotalSize))
}

// p2pPublishProgress representa o progresso de importação de um arquivo para o P2P.
type p2pPublishProgress struct {
	ArtifactName   string `json:"artifactName"`
	BytesProcessed int64  `json:"bytesProcessed"`
	TotalBytes     int64  `json:"totalBytes"`
	Percent        int    `json:"percent"`
	Done           bool   `json:"done"`
	Error          string `json:"error,omitempty"`
}

// emitPublishProgress emite evento Wails p2p:publish:progress para o frontend.
func (c *p2pCoordinator) emitPublishProgress(artifactName string, processed, total int64, done bool, errMsg string) {
	if c.deps == nil {
		return
	}
	percent := 0
	if total > 0 {
		percent = int(processed * 100 / total)
	}
	c.deps.EmitEvent("p2p:publish:progress", p2pPublishProgress{
		ArtifactName:   artifactName,
		BytesProcessed: processed,
		TotalBytes:     total,
		Percent:        percent,
		Done:           done,
		Error:          errMsg,
	})
}

// generateManifestEager constrói e cacheia o manifest para um artifact recém-publicado.
// Executado em goroutine para não bloquear o retorno ao chamador.
// Usa manifestInFlight para evitar geração duplicada concorrente do mesmo artifact.
func (c *p2pCoordinator) generateManifestEager(path, artifactName string) {
	if c.transferServer == nil {
		return
	}
	manifestDir := c.transferServer.manifestDir()
	if manifestDir == "" {
		return
	}

	// Dedup: se já existe uma goroutine gerando manifest para este artifact,
	// esta goroutine aguarda a anterior terminar e retorna sem fazer nada.
	readyCh := make(chan struct{})
	if actual, loaded := c.manifestInFlight.LoadOrStore(artifactName, readyCh); loaded {
		// Outra goroutine já está gerando — aguarda e retorna.
		<-actual.(chan struct{})
		return
	}
	defer func() {
		close(readyCh)
		c.manifestInFlight.Delete(artifactName)
	}()

	artifactID := CanonicalArtifactID("", artifactName, "")
	var chunkSize int64 = defaultChunkSizeBytes
	if cfg := c.deps.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
		chunkSize = cfg.ChunkSizeBytes
	}
	cpuFn := func() float64 { return c.cpuSampler.Sample() }
	ctx := context.Background()
	if c.deps.Context() != nil {
		ctx = c.deps.Context()
	}
	manifest, err := buildChunkManifest(ctx, path, artifactID, chunkSize, nil, cpuFn)
	if err != nil {
		c.deps.Log(fmt.Sprintf("[p2p] aviso: falha ao gerar manifest eager para %s: %v", artifactName, err))
		return
	}
	if err := saveCachedManifest(manifestDir, artifactName, manifest); err != nil {
		c.deps.Log(fmt.Sprintf("[p2p] aviso: falha ao salvar manifest eager para %s: %v", artifactName, err))
		return
	}
	c.deps.Log(fmt.Sprintf("[p2p] manifest eager gerado: %s chunks=%d size=%d", artifactName, manifest.TotalChunks, manifest.TotalSize))
}

// CleanupStaleArtifacts remove artifacts do diretório local que não foram
// acessados/modificados há mais de 7 dias.
func (c *p2pCoordinator) CleanupStaleArtifacts() {
	dir := c.deps.P2PTempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		c.deps.Log(fmt.Sprintf("[p2p] cleanup: %d artifacts removidos (mais de 7 dias)", removed))
	}
}

func (c *p2pCoordinator) ReplicateArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}
