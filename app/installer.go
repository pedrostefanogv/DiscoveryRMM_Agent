package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/samber/lo"
)

func installerConfigPathCandidates() []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		// 1o: C:\ProgramData\Discovery (compartilhado entre usuarios)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2o: LOCALAPPDATA\Discovery (fallback compatibilidade)
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "config.json"))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "config.json"))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", "config.json"))
	}

	paths = append(paths, filepath.Join(".", "config.json"))
	return lo.Uniq(paths)
}

func installerConfigWriteCandidates(sourcePath string) []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		// 1o: C:\ProgramData\Discovery (preferencia - compartilhado entre usuarios)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2o: LOCALAPPDATA\Discovery (fallback compatibilidade)
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "config.json"))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", "config.json"))
	}

	paths = append(paths, filepath.Join(".", "config.json"))
	if strings.TrimSpace(sourcePath) != "" {
		paths = append(paths, sourcePath)
	}

	return lo.Uniq(paths)
}

func loadInstallerConfig() (InstallerConfig, string, error) {
	for _, path := range installerConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg InstallerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return InstallerConfig{}, "", fmt.Errorf("falha ao ler %s: %w", path, err)
		}

		cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.P2P = normalizeP2PConfig(cfg.P2P)

		if cfg.ServerURL == "" && (cfg.ApiScheme == "" || cfg.ApiServer == "") {
			continue
		}

		return cfg, path, nil
	}

	return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
}

func persistInstallerConfig(sourcePath string, cfg InstallerConfig) (string, error) {
	paths := installerConfigWriteCandidates(sourcePath)
	if len(paths) == 0 {
		return "", fmt.Errorf("nenhum caminho disponivel para persistir config de producao")
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("falha ao serializar config de producao: %w", err)
	}

	var errs []string
	for _, path := range paths {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}
