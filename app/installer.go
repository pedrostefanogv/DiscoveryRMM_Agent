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

func installerOverridePathCandidates() []string {
	paths := make([]string, 0, 3)

	if runtime.GOOS == "windows" {
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "installer.json"))
		}
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "installer.json"))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "installer.json"))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", "installer.json"))
	}

	paths = append(paths, filepath.Join(".", "installer.json"))
	return lo.Uniq(paths)
}

func loadInstallerConfigFromCandidates(paths []string) (InstallerConfig, string, bool, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg InstallerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return InstallerConfig{}, "", false, fmt.Errorf("falha ao ler %s: %w", path, err)
		}

		cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.P2P = normalizeP2PConfig(cfg.P2P)

		return cfg, path, true, nil
	}

	return InstallerConfig{}, "", false, nil
}

func mergeInstallerOverride(base, override InstallerConfig) InstallerConfig {
	if strings.TrimSpace(override.ServerURL) != "" {
		base.ServerURL = strings.TrimSpace(override.ServerURL)
	}
	if strings.TrimSpace(override.APIKey) != "" {
		base.APIKey = strings.TrimSpace(override.APIKey)
	}
	if strings.TrimSpace(override.ApiScheme) != "" {
		base.ApiScheme = strings.TrimSpace(strings.ToLower(override.ApiScheme))
	}
	if strings.TrimSpace(override.ApiServer) != "" {
		base.ApiServer = strings.TrimSpace(override.ApiServer)
	}
	if override.DiscoveryEnabled != nil {
		base.DiscoveryEnabled = override.DiscoveryEnabled
	}
	return base
}

func findInstallerOverridePath() string {
	for _, path := range installerOverridePathCandidates() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
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
	baseCfg, basePath, baseFound, baseErr := loadInstallerConfigFromCandidates(installerConfigPathCandidates())
	if baseErr != nil {
		return InstallerConfig{}, "", baseErr
	}

	overrideCfg, _, overrideFound, overrideErr := loadInstallerConfigFromCandidates(installerOverridePathCandidates())
	if overrideErr != nil {
		return InstallerConfig{}, "", overrideErr
	}

	if !baseFound && !overrideFound {
		return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
	}

	if !baseFound {
		if overrideCfg.ServerURL == "" && (overrideCfg.ApiScheme == "" || overrideCfg.ApiServer == "") {
			return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
		}
		return overrideCfg, "", nil
	}

	merged := mergeInstallerOverride(baseCfg, overrideCfg)
	if merged.ServerURL == "" && (merged.ApiScheme == "" || merged.ApiServer == "") {
		return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
	}

	return merged, basePath, nil
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
