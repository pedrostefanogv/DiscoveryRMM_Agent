package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"discovery/internal/platform"
)

func (c *p2pCoordinator) ListArtifacts() ([]P2PArtifactView, error) {
	dir := c.app.p2pTempDir()
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

		// Garantir que o manifest está gerado e cacheado para download chunked.
		go c.ensureManifestForArtifact(name, path, info.ModTime())
	}
	return artifacts, nil
}

// ensureManifestForArtifact verifica se um manifest cacheado existe e é válido;
// caso contrário, gera e persiste um novo manifest para o artifact.
func (c *p2pCoordinator) ensureManifestForArtifact(artifactName, path string, modTime time.Time) {
	if c.transferServer == nil {
		return
	}
	manifestDir := c.transferServer.manifestDir()
	if manifestDir == "" {
		return
	}

	existing := loadCachedManifest(manifestDir, artifactName, path)
	if existing != nil && manifestMatchesFile(existing, path) {
		return // já válido
	}

	artifactID := CanonicalArtifactID("", artifactName, "")
	var chunkSize int64 = defaultChunkSizeBytes
	if cfg := c.app.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
		chunkSize = cfg.ChunkSizeBytes
	}
	manifest, err := buildChunkManifest(path, artifactID, chunkSize)
	if err != nil {
		c.app.logs.append(fmt.Sprintf("[p2p] aviso: falha ao gerar manifest para %s: %v", artifactName, err))
		return
	}
	if err := saveCachedManifest(manifestDir, artifactName, manifest); err != nil {
		c.app.logs.append(fmt.Sprintf("[p2p] aviso: falha ao salvar manifest para %s: %v", artifactName, err))
		return
	}
	c.app.logs.append(fmt.Sprintf("[p2p] manifest gerado: %s chunks=%d size=%d", artifactName, manifest.TotalChunks, manifest.TotalSize))
}

func (c *p2pCoordinator) PublishTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact inválido")
	}
	dir := c.app.p2pTempDir()
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
	sourceChecksum, err := computeFileSHA256(sourcePath)
	if err != nil {
		return P2PArtifactView{}, fmt.Errorf("falha ao calcular checksum de origem: %w", err)
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	defer sourceFile.Close()

	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	targetPath := filepath.Join(dir, artifactName)
	tmpPath := targetPath + ".importing"
	targetFile, err := os.Create(tmpPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		targetFile.Close()
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := targetFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	_ = platform.EnsureWorldReadable(targetPath)

	info, err := os.Stat(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if sourceInfo.Size() != info.Size() {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("arquivo importado com tamanho divergente")
	}
	if !strings.EqualFold(sourceChecksum, checksum) {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("checksum divergente apos mover arquivo para temp")
	}
	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()
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
	sourceChecksum, err := computeFileSHA256(sourcePath)
	if err != nil {
		return P2PArtifactView{}, fmt.Errorf("falha ao calcular checksum de origem: %w", err)
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	defer sourceFile.Close()

	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	// Nome do arquivo: artifactID + extensão original (ex.: "rel-abc.exe")
	targetName := sanitizeArtifactName(artifactID + filepath.Ext(sourcePath))
	targetPath := filepath.Join(dir, targetName)
	tmpPath := targetPath + ".importing"
	targetFile, err := os.Create(tmpPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		targetFile.Close()
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := targetFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	_ = platform.EnsureWorldReadable(targetPath)

	info, err := os.Stat(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if sourceInfo.Size() != info.Size() {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("arquivo importado com tamanho divergente")
	}
	if !strings.EqualFold(sourceChecksum, checksum) {
		_ = os.Remove(targetPath)
		return P2PArtifactView{}, fmt.Errorf("checksum divergente apos mover arquivo para temp")
	}
	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()
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

// CleanupStaleArtifacts remove artifacts do diretório local que não foram
// acessados/modificados há mais de 7 dias.
func (c *p2pCoordinator) CleanupStaleArtifacts() {
	dir := c.app.p2pTempDir()
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
		c.app.logs.append(fmt.Sprintf("[p2p] cleanup: %d artifacts removidos (mais de 7 dias)", removed))
	}
}

func (c *p2pCoordinator) ReplicateArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}
