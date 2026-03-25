package debug

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/p2pmeta"
)

// Config holds server connection settings for the debug page.
type Config struct {
	ApiScheme                         string `json:"apiScheme"`
	ApiServer                         string `json:"apiServer"`
	AuthToken                         string `json:"authToken"`
	NatsServer                        string `json:"natsServer"`
	NatsWsServer                      string `json:"natsWsServer"`
	AgentID                           string `json:"agentId"`
	Scheme                            string `json:"scheme,omitempty"`
	Server                            string `json:"server,omitempty"`
	AutomationP2PWingetInstallEnabled bool   `json:"automationP2pWingetInstallEnabled,omitempty"`
}

// InstallerConfig is the bootstrap config saved by the NSIS installer.
type InstallerConfig struct {
	ServerURL            string         `json:"serverUrl"`
	APIKey               string         `json:"apiKey"`
	DiscoveryEnabled     *bool          `json:"discoveryEnabled,omitempty"`
	ApiScheme            string         `json:"apiScheme,omitempty"`
	ApiServer            string         `json:"apiServer,omitempty"`
	AuthToken            string         `json:"authToken,omitempty"`
	AgentID              string         `json:"agentId,omitempty"`
	NatsServer           string         `json:"natsServer,omitempty"`
	NatsWsServer         string         `json:"natsWsServer,omitempty"`
	P2P                  p2pmeta.Config `json:"p2p,omitempty"`
	MeshCentralInstalled bool           `json:"meshCentralInstalled,omitempty"`
}

func (c *InstallerConfig) UnmarshalJSON(data []byte) error {
	type rawInstallerConfig struct {
		ServerURL            string          `json:"serverUrl"`
		APIKey               string          `json:"apiKey"`
		DiscoveryEnabled     json.RawMessage `json:"discoveryEnabled,omitempty"`
		ApiScheme            string          `json:"apiScheme,omitempty"`
		ApiServer            string          `json:"apiServer,omitempty"`
		AuthToken            string          `json:"authToken,omitempty"`
		AgentID              string          `json:"agentId,omitempty"`
		NatsServer           string          `json:"natsServer,omitempty"`
		NatsWsServer         string          `json:"natsWsServer,omitempty"`
		P2P                  p2pmeta.Config  `json:"p2p,omitempty"`
		MeshCentralInstalled bool            `json:"meshCentralInstalled,omitempty"`
	}

	var raw rawInstallerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	discoveryEnabled, err := parseInstallerBool(raw.DiscoveryEnabled)
	if err != nil {
		return fmt.Errorf("discoveryEnabled invalido: %w", err)
	}

	*c = InstallerConfig{
		ServerURL:            raw.ServerURL,
		APIKey:               raw.APIKey,
		DiscoveryEnabled:     discoveryEnabled,
		ApiScheme:            raw.ApiScheme,
		ApiServer:            raw.ApiServer,
		AuthToken:            raw.AuthToken,
		AgentID:              raw.AgentID,
		NatsServer:           raw.NatsServer,
		NatsWsServer:         raw.NatsWsServer,
		P2P:                  raw.P2P,
		MeshCentralInstalled: raw.MeshCentralInstalled,
	}
	return nil
}

func parseInstallerBool(raw json.RawMessage) (*bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var asBool bool
	if err := json.Unmarshal(raw, &asBool); err == nil {
		return &asBool, nil
	}

	var asNumber int
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		switch asNumber {
		case 0:
			value := false
			return &value, nil
		case 1:
			value := true
			return &value, nil
		default:
			return nil, fmt.Errorf("numero fora do intervalo esperado: %d", asNumber)
		}
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		switch strings.TrimSpace(strings.ToLower(asString)) {
		case "true", "1", "yes", "on":
			value := true
			return &value, nil
		case "false", "0", "no", "off":
			value := false
			return &value, nil
		case "":
			return nil, nil
		}
	}

	return nil, fmt.Errorf("valor nao suportado")
}

// AgentStatus is the frontend-facing agent connection snapshot.
type AgentStatus struct {
	Connected bool   `json:"connected"`
	AgentID   string `json:"agentId"`
	Server    string `json:"server"`
	LastEvent string `json:"lastEvent"`
	Transport string `json:"transport,omitempty"`
}

// RealtimeStatus represents server-side realtime transport health.
type RealtimeStatus struct {
	NATSConnected          bool      `json:"natsConnected"`
	SignalRConnectedAgents int       `json:"signalrConnectedAgents"`
	CheckedAtUTC           time.Time `json:"checkedAtUtc"`
}
