package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	}
	return artifacts, nil
}

func (c *p2pCoordinator) PublishTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact invalido")
	}
	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	path := filepath.Join(dir, artifactName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return P2PArtifactView{}, err
	}
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

func (c *p2pCoordinator) ReplicateArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}
