package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"discovery/internal/platform"
)

func parsePortFromURL(raw string) (int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("url sem porta")
	}
	portPart := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(portPart, "/") {
		chunks := strings.Split(portPart, "/")
		portPart = chunks[0]
	}
	return strconv.Atoi(portPart)
}

func resolveP2PTempDir(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return platform.P2PTempDir()
	}
	return filepath.Join(GetDataDir(), "TempP2P")
}

func (a *App) p2pTempDir() string {
	dir := resolveP2PTempDir(runtime.GOOS)
	// Garantir que o diretório exista e tenha permissão para todos os usuários
	// da máquina (Windows: Everyone Full Control com herança; Linux: no-op).
	_ = platform.EnsureWorldAccess(dir)
	return dir
}

func (a *App) cleanupExpiredP2PTempArtifacts(now time.Time) (int, error) {
	cfg := a.GetP2PConfig()
	ttl := time.Duration(cfg.TempTTLHours) * time.Hour
	dir := a.p2pTempDir()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	// TTL curto para artefatos temporários de transferência (partial files, parts dirs).
	// Se a transferência falhou ou o RenameAtomic deixou orfão, limpamos em 1 hora.
	orphanTTL := 1 * time.Hour
	if orphanTTL > ttl {
		orphanTTL = ttl
	}

	removed := 0
	emptyDirs := make(map[string]struct{})
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == dir {
			return nil
		}

		// Diretórios .parts: limpar se expirados (transferência abortada/crash).
		if d.IsDir() && strings.HasSuffix(d.Name(), ".parts") {
			info, infoErr := d.Info()
			if infoErr != nil || now.Sub(info.ModTime()) < orphanTTL {
				emptyDirs[path] = struct{}{}
				return nil
			}
			if err := os.RemoveAll(path); err == nil {
				removed++
				a.logs.append(fmt.Sprintf("[p2p] limpeza: diretorio de chunks orfao removido: %s", d.Name()))
			}
			return nil
		}

		if d.IsDir() {
			emptyDirs[path] = struct{}{}
			return nil
		}

		// Arquivos .partial: limpar agressivamente (são temporários de montagem).
		// Se o RenameAtomic falhou, o .partial ficou orfão. Limpamos com TTL curto.
		if strings.HasSuffix(d.Name(), ".partial") {
			info, infoErr := d.Info()
			if infoErr != nil || now.Sub(info.ModTime()) < orphanTTL {
				return nil
			}
			if err := os.Remove(path); err == nil {
				removed++
				a.logs.append(fmt.Sprintf("[p2p] limpeza: partial orfao removido: %s", d.Name()))
			}
			return nil
		}

		// Arquivos .importing: temporários de import (single-pass).
		if strings.HasSuffix(d.Name(), ".importing") {
			info, infoErr := d.Info()
			if infoErr != nil || now.Sub(info.ModTime()) < orphanTTL {
				return nil
			}
			if err := os.Remove(path); err == nil {
				removed++
				a.logs.append(fmt.Sprintf("[p2p] limpeza: importing orfao removido: %s", d.Name()))
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) < ttl {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return nil
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, err
	}

	for dirPath := range emptyDirs {
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			_ = os.Remove(dirPath)
		}
	}

	if a.p2pCoord != nil {
		a.p2pCoord.mu.Lock()
		a.p2pCoord.lastCleanupUTC = now.UTC()
		a.p2pCoord.mu.Unlock()
	}

	if removed > 0 {
		a.logs.append(fmt.Sprintf("[p2p] limpeza de temp concluida: %d item(ns) removido(s)", removed))
	}
	return removed, nil
}

func (a *App) clearAllP2PTempArtifacts(now time.Time) (int, error) {
	dir := a.p2pTempDir()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
		removed++
	}

	if a.p2pCoord != nil {
		a.p2pCoord.mu.Lock()
		a.p2pCoord.lastCleanupUTC = now.UTC()
		a.p2pCoord.mu.Unlock()

		a.p2pCoord.sha256CacheMu.Lock()
		a.p2pCoord.sha256Cache = make(map[string]artifactSHA256CacheEntry)
		a.p2pCoord.sha256CacheMu.Unlock()

		// Remove manifests órfãos após limpeza total.
		a.p2pCoord.collectOrphanArtifacts()
	}

	if removed > 0 {
		a.logs.append(fmt.Sprintf("[p2p] limpeza total de artifacts locais: %d item(ns) removido(s)", removed))
	}

	return removed, nil
}

func formatTimeRFC3339(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}
