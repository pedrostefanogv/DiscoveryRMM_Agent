package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestDirName = "manifests"
)

// manifestDir retorna o diretório onde manifests cacheados são armazenados.
func (s *p2pTransferServer) manifestDir() string {
	s.mu.RLock()
	base := s.tempDir
	s.mu.RUnlock()
	if base == "" {
		return ""
	}
	return filepath.Join(base, manifestDirName)
}

// cachedManifestPath retorna o caminho completo para o arquivo de cache do manifest.
func cachedManifestPath(manifestDir, artifactName string) string {
	safe := sanitizeArtifactName(artifactName)
	if safe == "" {
		return ""
	}
	return filepath.Join(manifestDir, safe+".json")
}

// saveCachedManifest persiste um manifest em disco para reuso futuro.
func saveCachedManifest(manifestDir string, artifactName string, manifest P2PChunkManifest) error {
	if manifestDir == "" {
		return fmt.Errorf("manifestDir não definido")
	}
	path := cachedManifestPath(manifestDir, artifactName)
	if path == "" {
		return fmt.Errorf("artifactName inválido")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// loadCachedManifest carrega um manifest do cache, validando que o arquivo artifact
// referenciado ainda existe. Retorna nil se o cache não existir ou estiver obsoleto.
func loadCachedManifest(manifestDir, artifactName, artifactPath string) *P2PChunkManifest {
	path := cachedManifestPath(manifestDir, artifactName)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var manifest P2PChunkManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		_ = os.Remove(path)
		return nil
	}
	// Validar que o artifact referenciado ainda existe.
	if _, err := os.Stat(artifactPath); err != nil {
		_ = os.Remove(path)
		return nil
	}
	return &manifest
}

// manifestMatchesFile verifica se um manifest é consistente com o arquivo em disco.
func manifestMatchesFile(manifest *P2PChunkManifest, path string) bool {
	if manifest == nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Size() != manifest.TotalSize {
		return false
	}
	return true
}

// finalizeDownloadedArtifact executa o pipeline pós-download:
// 1. Valida SHA-256
// 2. Gera/valida manifesto
// 3. Cacheia manifest local
// 4. Anuncia artifact como available (via P2P metadata em memória)
func (c *p2pCoordinator) finalizeDownloadedArtifact(artifactName, path, expectedSHA256 string) error {
	if strings.TrimSpace(expectedSHA256) != "" {
		checksum, err := computeFileSHA256(path)
		if err != nil {
			return fmt.Errorf("falha ao calcular checksum: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(checksum), strings.TrimSpace(expectedSHA256)) {
			return fmt.Errorf("checksum divergente: esperado=%s obtido=%s", expectedSHA256, checksum)
		}
	}

	// Tentar carregar manifest existente
	transfer := c.transferServer
	if transfer != nil {
		manifestDir := transfer.manifestDir()
		manifest := loadCachedManifest(manifestDir, artifactName, path)

		if manifest == nil || !manifestMatchesFile(manifest, path) {
			artifactID := CanonicalArtifactID("", artifactName, "")
			var chunkSize int64 = defaultChunkSizeBytes
			if cfg := c.app.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
				chunkSize = cfg.ChunkSizeBytes
			}
			generated, err := buildChunkManifest(path, artifactID, chunkSize)
			if err != nil {
				return fmt.Errorf("falha ao gerar manifest: %w", err)
			}
			manifest = &generated
		}

		if err := saveCachedManifest(manifestDir, artifactName, *manifest); err != nil {
			c.app.logs.append(fmt.Sprintf("[p2p] aviso: falha ao salvar cache de manifest para %s: %v", artifactName, err))
		}
	}

	return nil
}

// updateManifestCacheAfterDownload é chamado após um artifact ser baixado com sucesso
// para gerar/cachear o manifest e atualizar o tempo de modificação.
func (c *p2pCoordinator) updateManifestCacheAfterDownload(artifactName, path string) {
	if c.transferServer == nil {
		return
	}
	manifestDir := c.transferServer.manifestDir()
	if manifestDir == "" {
		return
	}

	if _, err := os.Stat(path); err != nil {
		return
	}

	// Só atualiza cache se o manifest não existir ou estiver desatualizado
	existing := loadCachedManifest(manifestDir, artifactName, path)
	if existing != nil && manifestMatchesFile(existing, path) {
		return
	}

	artifactID := CanonicalArtifactID("", artifactName, "")
	var chunkSize int64 = defaultChunkSizeBytes
	if cfg := c.app.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
		chunkSize = cfg.ChunkSizeBytes
	}
	manifest, err := buildChunkManifest(path, artifactID, chunkSize)
	if err != nil {
		c.app.logs.append(fmt.Sprintf("[p2p] aviso: falha ao gerar manifest pos-download para %s: %v", artifactName, err))
		return
	}
	if err := saveCachedManifest(manifestDir, artifactName, manifest); err != nil {
		c.app.logs.append(fmt.Sprintf("[p2p] aviso: falha ao salvar manifest pos-download para %s: %v", artifactName, err))
		return
	}
	c.app.logs.append(fmt.Sprintf("[p2p] manifest cacheado pos-download: %s chunks=%d sha256=%s",
		artifactName, manifest.TotalChunks, manifest.SHA256))
}
