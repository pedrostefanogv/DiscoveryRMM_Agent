package app

import (
	"os"
	"runtime"
	runtimeDebug "runtime/debug"
	"strings"
	"time"

	"discovery/app/core/buildinfo"
	"discovery/app/status"
)

// StatusOverview provides a simplified health snapshot for the default status page.
type StatusOverview = status.Overview

// GetStatusOverview returns a user-friendly status summary for the Status tab.
func (a *App) GetStatusOverview() StatusOverview {
	agent := a.GetAgentStatus()
	cfg := a.GetDebugConfig()
	out := StatusOverview{
		Connected:           agent.Connected,
		TransportConnected:  agent.TransportConnected,
		ConnectionLabel:     "Offline",
		OnlineReason:        strings.TrimSpace(agent.OnlineReason),
		Hostname:            "Computador local",
		Server:              status.NormalizeServer(status.FirstServerCandidate(agent.Server, status.DebugConfig{Server: cfg.Server, NatsWsServer: cfg.NatsWsServer, NatsServer: cfg.NatsServer, ApiServer: cfg.ApiServer})),
		ConnectionType:      status.ResolveConnectionType(agent.Transport, status.DebugConfig{Scheme: cfg.Scheme, NatsWsServer: cfg.NatsWsServer, NatsServer: cfg.NatsServer}),
		LastGlobalPongAtUTC: strings.TrimSpace(agent.LastGlobalPongAtUTC),
		GlobalPongStale:     agent.GlobalPongStale,
		AppVersion:          strings.TrimSpace(Version),
		AppCommit:           strings.TrimSpace(buildinfo.Commit),
		BuildDateUTC:        resolveAgentBuildDateUTC(),
		OSName:              normalizeOSDisplayName(runtime.GOOS),
		OSVersion:           runtime.GOARCH,
		CheckedAtUTC:        time.Now().UTC(),
	}

	if host, err := os.Hostname(); err == nil {
		host = strings.TrimSpace(host)
		if host != "" {
			out.Hostname = host
		}
	}

	if out.Connected {
		out.ConnectionLabel = "Online"
	}

	if until := parseRFC3339Time(strings.TrimSpace(agent.NonCriticalBackoffUntilUTC)); !until.IsZero() && until.After(time.Now().UTC()) {
		out.NonCriticalDeferred = true
		out.NonCriticalDeferredUntil = until.UTC().Format(time.RFC3339)
		out.NonCriticalDeferredReason = strings.TrimSpace(agent.NonCriticalBackoffReason)
	}
	if !out.NonCriticalDeferred {
		if until, deferred, reason := a.nonCriticalBackoffStatus(); deferred {
			out.NonCriticalDeferred = true
			out.NonCriticalDeferredUntil = until.UTC().Format(time.RFC3339)
			out.NonCriticalDeferredReason = reason
		}
	}
	if out.AppVersion == "" {
		out.AppVersion = "dev"
	}

	if inv, ok := a.invCache.get(); ok {
		if host := strings.TrimSpace(inv.Hardware.Hostname); host != "" {
			out.Hostname = host
		}
		if name := strings.TrimSpace(inv.OS.Name); name != "" {
			out.OSName = name
		}
		if edition := strings.TrimSpace(inv.OS.Edition); edition != "" {
			out.OSEdition = edition
		}
		versionParts := []string{}
		if version := strings.TrimSpace(inv.OS.Version); version != "" {
			versionParts = append(versionParts, version)
		}
		if build := strings.TrimSpace(inv.OS.Build); build != "" {
			versionParts = append(versionParts, "build "+build)
		}
		if arch := strings.TrimSpace(inv.OS.Architecture); arch != "" {
			versionParts = append(versionParts, arch)
		}
		if len(versionParts) > 0 {
			out.OSVersion = strings.Join(versionParts, " | ")
		}
		out.LastInventoryCollected = strings.TrimSpace(inv.CollectedAt)
	}

	rt, err := a.GetRealtimeStatus()
	if err != nil {
		status.ApplyRealtimeFallbackFromAgentStatus(&out, status.AgentStatus{
			Connected:          agent.Connected,
			TransportConnected: agent.TransportConnected,
			Transport:          agent.Transport,
		}, err)
	} else {
		status.ApplyRealtimeStatus(&out, status.RealtimeStatus{
			NATSConnected:           rt.NATSConnected,
			RealtimeConnectedAgents: rt.RealtimeConnectedAgents,
		})
	}

	if a.db != nil {
		agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
		if agentID != "" {
			if n, err := a.db.CountPendingCommandResultOutbox(agentID); err == nil {
				out.PendingCommandResults = n
			}
			if n, err := a.db.CountPendingP2PTelemetryOutbox(agentID); err == nil {
				out.PendingP2PTelemetry = n
			}
		}
	}

	// Status de self-update do agente.
	if a.selfUpdater != nil {
		policy := a.selfUpdater.GetPolicy()
		out.UpdateCheckEnabled = policy.Enabled
		out.UpdateCheckInProgress = a.selfUpdater.IsChecking()
		if t := a.selfUpdater.LastCheckAt(); !t.IsZero() {
			out.LastUpdateCheckAtUTC = t.UTC().Format(time.RFC3339)
		}
		out.UpdateLastError = a.selfUpdater.LastError()
		out.UpdateLastInstallerExitCode = a.selfUpdater.LastInstallerExitCode()
		out.UpdatePendingTargetVersion = a.selfUpdater.PendingTargetVersion()
		out.UpdateDownloadOKCount = a.selfUpdater.DownloadOKCount()
		out.UpdateLaunchOKCount = a.selfUpdater.LaunchOKCount()
		out.UpdateLaunchFailCount = a.selfUpdater.LaunchFailCount()
		out.UpdateInstallCompleteCount = a.selfUpdater.InstallCompleteCount()
		out.UpdateDeferred = a.selfUpdater.IsDeferred()
		if out.UpdateDeferred {
			out.UpdateDeferredReason = a.selfUpdater.DeferredReason()
			if t := a.selfUpdater.DeferredSince(); !t.IsZero() {
				out.UpdateDeferredSinceUTC = t.UTC().Format(time.RFC3339)
			}
		}
	}

	return out
}

// normalizeOSDisplayName converte runtime.GOOS para um nome amigável.
// Usado como fallback antes do inventário estar disponível no cache.
func normalizeOSDisplayName(goos string) string {
	switch goos {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	default:
		return goos
	}
}

func resolveAgentBuildDateUTC() string {
	if vcsTime := readBuildInfoVCSTime(); !vcsTime.IsZero() {
		return vcsTime.UTC().Format(time.RFC3339)
	}

	execPath, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(execPath)
	if err != nil {
		return ""
	}
	if info.ModTime().IsZero() {
		return ""
	}

	return info.ModTime().UTC().Format(time.RFC3339)
}

func readBuildInfoVCSTime() time.Time {
	buildInfo, ok := runtimeDebug.ReadBuildInfo()
	if !ok || buildInfo == nil {
		return time.Time{}
	}

	for _, setting := range buildInfo.Settings {
		if setting.Key != "vcs.time" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(setting.Value))
		if err != nil {
			return time.Time{}
		}
		return parsed.UTC()
	}

	return time.Time{}
}
