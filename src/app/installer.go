package app

import (
	"discovery/app/installer"
)

// installerSvc é o service de configuração do instalador.
var installerSvc *installer.Service

func installerConfigPathCandidates() []string {
	if installerSvc == nil {
		return nil
	}
	return installerSvc.ConfigPathCandidates()
}

func installerOverridePathCandidates() []string {
	if installerSvc == nil {
		return nil
	}
	return installerSvc.OverridePathCandidates()
}

func loadInstallerConfigFromCandidates(paths []string) (InstallerConfig, string, bool, error) {
	if installerSvc == nil {
		return InstallerConfig{}, "", false, nil
	}
	return installerSvc.LoadFromCandidates(paths)
}

func mergeInstallerOverride(base, override InstallerConfig) InstallerConfig {
	if installerSvc == nil {
		return base
	}
	return installerSvc.MergeOverride(base, override)
}

func findInstallerOverridePath() string {
	if installerSvc == nil {
		return ""
	}
	return installerSvc.FindOverridePath()
}

func cleanupLegacyInstallerOverrideFiles() {
	if installerSvc == nil {
		return
	}
	installerSvc.CleanupLegacyOverrideFiles()
}

func installerConfigWriteCandidates(sourcePath string) []string {
	if installerSvc == nil {
		return nil
	}
	return installerSvc.WriteCandidates(sourcePath)
}

func loadInstallerConfig() (InstallerConfig, string, error) {
	if installerSvc == nil {
		return InstallerConfig{}, "", nil
	}
	return installerSvc.Load()
}

func ensureDefaultInstallerConfig() (InstallerConfig, string, error) {
	if installerSvc == nil {
		return InstallerConfig{}, "", nil
	}
	return installerSvc.EnsureDefault()
}

func persistInstallerConfig(sourcePath string, cfg InstallerConfig) (string, error) {
	if installerSvc == nil {
		return "", nil
	}
	return installerSvc.Persist(sourcePath, cfg)
}
