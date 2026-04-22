package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
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
		// Usar pasta temporária do Windows para permitir limpeza automática pelo sistema.
		windowsDir := strings.TrimSpace(os.Getenv("WINDIR"))
		if windowsDir == "" {
			windowsDir = filepath.Join("C:\\", "Windows")
		}
		return filepath.Join(windowsDir, "Temp", "Discovery", "P2P_Temp")
	}
	return filepath.Join(getDataDir(), "TempP2P")
}

func (a *App) p2pTempDir() string {
	return resolveP2PTempDir(runtime.GOOS)
}

func (a *App) cleanupExpiredP2PTempArtifacts(now time.Time) (int, error) {
	cfg := a.GetP2PConfig()
	ttl := time.Duration(cfg.TempTTLHours) * time.Hour
	dir := a.p2pTempDir()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	removed := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
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

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() || path == dir {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		if len(entries) == 0 {
			_ = os.Remove(path)
		}
		return nil
	})

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

func formatTimeRFC3339(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}
