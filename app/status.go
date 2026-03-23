package app

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// StatusOverview provides a simplified health snapshot for the default status page.
type StatusOverview struct {
	Connected               bool      `json:"connected"`
	ConnectionLabel         string    `json:"connectionLabel"`
	Hostname                string    `json:"hostname"`
	Server                  string    `json:"server"`
	ConnectionType          string    `json:"connectionType"`
	AppVersion              string    `json:"appVersion"`
	OSName                  string    `json:"osName"`
	OSVersion               string    `json:"osVersion"`
	LastInventoryCollected  string    `json:"lastInventoryCollected"`
	RealtimeAvailable       bool      `json:"realtimeAvailable"`
	RealtimeNATSConnected   bool      `json:"realtimeNatsConnected"`
	RealtimeConnectedAgents int       `json:"realtimeConnectedAgents"`
	RealtimeMessage         string    `json:"realtimeMessage"`
	CheckedAtUTC            time.Time `json:"checkedAtUtc"`
}

// GetStatusOverview returns a user-friendly status summary for the Status tab.
func (a *App) GetStatusOverview() StatusOverview {
	agent := a.GetAgentStatus()
	cfg := a.GetDebugConfig()
	out := StatusOverview{
		Connected:       agent.Connected,
		ConnectionLabel: "Offline",
		Hostname:        "Computador local",
		Server:          strings.TrimSpace(agent.Server),
		ConnectionType:  "SignalR",
		AppVersion:      strings.TrimSpace(Version),
		OSName:          runtime.GOOS,
		OSVersion:       runtime.GOARCH,
		CheckedAtUTC:    time.Now().UTC(),
	}

	if strings.EqualFold(strings.TrimSpace(cfg.Scheme), "nats") {
		out.ConnectionType = "NATS"
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
		out.RealtimeMessage = err.Error()
		return out
	}

	out.RealtimeAvailable = true
	out.RealtimeNATSConnected = rt.NATSConnected
	out.RealtimeConnectedAgents = rt.SignalRConnectedAgents
	if rt.NATSConnected {
		out.RealtimeMessage = "Realtime operacional"
	} else {
		out.RealtimeMessage = "Realtime indisponivel no momento"
	}

	return out
}
