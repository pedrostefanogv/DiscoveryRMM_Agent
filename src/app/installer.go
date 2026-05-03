package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/lo"

	"discovery/internal/platform"
)

func installerConfigPathCandidates() []string {
	return platform.ConfigPathCandidates()
}

func installerOverridePathCandidates() []string {
	return platform.InstallerOverridePathCandidates()
}

func loadInstallerConfigFromCandidates(paths []string) (InstallerConfig, string, bool, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[config] aviso: nao foi possivel ler config candidate %s: %v", path, err)
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
		cfg.ClientID = strings.TrimSpace(cfg.ClientID)
		cfg.SiteID = strings.TrimSpace(cfg.SiteID)
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
	if override.AutoProvisioning != nil {
		base.AutoProvisioning = override.AutoProvisioning
	}
	if override.AllowInsecureTLS != nil {
		base.AllowInsecureTLS = override.AllowInsecureTLS
	}
	if strings.TrimSpace(override.ClientID) != "" {
		base.ClientID = strings.TrimSpace(override.ClientID)
	}
	if strings.TrimSpace(override.SiteID) != "" {
		base.SiteID = strings.TrimSpace(override.SiteID)
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

func cleanupLegacyInstallerOverrideFiles() {
	for _, path := range installerOverridePathCandidates() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
	}
}

func installerConfigWriteCandidates(sourcePath string) []string {
	paths := make([]string, 0, len(platform.ConfigPathCandidates())+1)
	paths = append(paths, platform.ConfigPathCandidates()...)
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

	resolved := baseCfg
	resolvedPath := basePath

	if !baseFound {
		if overrideCfg.ServerURL == "" && (overrideCfg.ApiScheme == "" || overrideCfg.ApiServer == "") {
			return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
		}
		resolved = overrideCfg
		resolvedPath = ""
	} else {
		resolved = mergeInstallerOverride(baseCfg, overrideCfg)
		if resolved.ServerURL == "" && (resolved.ApiScheme == "" || resolved.ApiServer == "") {
			return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
		}
	}

	if overrideFound {
		if writePath, err := persistInstallerConfig(resolvedPath, resolved); err == nil {
			return resolved, writePath, nil
		}
	}

	return resolved, resolvedPath, nil
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
		cleanupLegacyInstallerOverrideFiles()
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}
