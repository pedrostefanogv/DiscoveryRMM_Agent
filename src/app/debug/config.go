package debug

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/p2pmeta"
	"discovery/internal/selfupdate"
)

// Config holds server connection settings for the debug page.
type Config struct {
	ApiScheme                         string `json:"apiScheme"`
	ApiServer                         string `json:"apiServer"`
	AuthToken                         string `json:"authToken"`
	NatsServer                        string `json:"natsServer"`
	NatsWsServer                      string `json:"natsWsServer"`
	AllowInsecureTLS                  bool   `json:"allowInsecureTls,omitempty"`
	NatsServerHost                    string `json:"natsServerHost,omitempty"`
	NatsUseWssExternal                bool   `json:"natsUseWssExternal,omitempty"`
	EnforceTlsHashValidation          bool   `json:"enforceTlsHashValidation,omitempty"`
	HandshakeEnabled                  bool   `json:"handshakeEnabled,omitempty"`
	ApiTlsCertHash                    string `json:"apiTlsCertHash,omitempty"`
	NatsTlsCertHash                   string `json:"natsTlsCertHash,omitempty"`
	AgentID                           string `json:"agentId"`
	Scheme                            string `json:"scheme,omitempty"`
	Server                            string `json:"server,omitempty"`
	AutomationP2PWingetInstallEnabled bool   `json:"automationP2pWingetInstallEnabled,omitempty"`
}

func (c Config) IsProvisioned() bool {
	scheme := strings.TrimSpace(strings.ToLower(c.ApiScheme))
	server := strings.TrimSpace(c.ApiServer)
	token := strings.TrimSpace(c.AuthToken)
	agentID := strings.TrimSpace(c.AgentID)
	return scheme != "" && server != "" && token != "" && agentID != ""
}

// InstallerConfig is the bootstrap config saved by the NSIS installer.
// The JSON contract now persists deployToken, while apiKey remains accepted
// on read for backward compatibility with older installers.
type InstallerConfig struct {
	ServerURL string `json:"serverUrl"`
	APIKey    string `json:"deployToken,omitempty"`
	// AutoProvisioning controla a participação local no fluxo de zero-touch
	// auto-provisioning via P2P (endpoint /p2p/config/onboard). Quando ausente,
	// o agente assume o comportamento padrão definido pela configuração do
	// servidor. O JSON canônico é "autoProvisioning"; o campo legado
	// "discoveryEnabled" continua sendo aceito em leitura para retrocompat.
	AutoProvisioning     *bool              `json:"autoProvisioning,omitempty"`
	ApiScheme            string             `json:"apiScheme,omitempty"`
	ApiServer            string             `json:"apiServer,omitempty"`
	AuthToken            string             `json:"authToken,omitempty"`
	AgentID              string             `json:"agentId,omitempty"`
	ClientID             string             `json:"clientId,omitempty"`
	SiteID               string             `json:"siteId,omitempty"`
	NatsServer           string             `json:"natsServer,omitempty"`
	NatsWsServer         string             `json:"natsWsServer,omitempty"`
	AllowInsecureTLS     *bool              `json:"allowInsecureTls,omitempty"`
	AgentUpdate          *selfupdate.Policy `json:"agentUpdate,omitempty"`
	P2P                  p2pmeta.Config     `json:"p2p,omitempty"`
	MeshCentralInstalled bool               `json:"meshCentralInstalled,omitempty"`
}

func (c *InstallerConfig) UnmarshalJSON(data []byte) error {
	type rawInstallerConfig struct {
		ServerURL            string             `json:"serverUrl"`
		DeployToken          string             `json:"deployToken,omitempty"`
		APIKey               string             `json:"apiKey"`
		AutoProvisioning     json.RawMessage    `json:"autoProvisioning,omitempty"`
		DiscoveryEnabled     json.RawMessage    `json:"discoveryEnabled,omitempty"`
		ApiScheme            string             `json:"apiScheme,omitempty"`
		ApiServer            string             `json:"apiServer,omitempty"`
		AuthToken            string             `json:"authToken,omitempty"`
		AgentID              string             `json:"agentId,omitempty"`
		ClientID             string             `json:"clientId,omitempty"`
		SiteID               string             `json:"siteId,omitempty"`
		NatsServer           string             `json:"natsServer,omitempty"`
		NatsWsServer         string             `json:"natsWsServer,omitempty"`
		AllowInsecureTLS     json.RawMessage    `json:"allowInsecureTls,omitempty"`
		AgentUpdate          *selfupdate.Policy `json:"agentUpdate,omitempty"`
		P2P                  p2pmeta.Config     `json:"p2p,omitempty"`
		MeshCentralInstalled bool               `json:"meshCentralInstalled,omitempty"`
	}

	var raw rawInstallerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	deployToken := strings.TrimSpace(raw.DeployToken)
	if deployToken == "" {
		deployToken = strings.TrimSpace(raw.APIKey)
	}

	// Aceita o nome canônico autoProvisioning e o legado discoveryEnabled. O
	// canônico tem precedência quando ambos estão presentes.
	autoProvisioning, err := parseInstallerBool(raw.AutoProvisioning)
	if err != nil {
		return fmt.Errorf("autoProvisioning invalido: %w", err)
	}
	if autoProvisioning == nil {
		legacy, err := parseInstallerBool(raw.DiscoveryEnabled)
		if err != nil {
			return fmt.Errorf("discoveryEnabled invalido: %w", err)
		}
		autoProvisioning = legacy
	}

	allowInsecureTLS, err := parseInstallerBool(raw.AllowInsecureTLS)
	if err != nil {
		return fmt.Errorf("allowInsecureTls invalido: %w", err)
	}

	*c = InstallerConfig{
		ServerURL:            raw.ServerURL,
		APIKey:               deployToken,
		AutoProvisioning:     autoProvisioning,
		ApiScheme:            raw.ApiScheme,
		ApiServer:            raw.ApiServer,
		AuthToken:            raw.AuthToken,
		AgentID:              raw.AgentID,
		ClientID:             raw.ClientID,
		SiteID:               raw.SiteID,
		NatsServer:           raw.NatsServer,
		NatsWsServer:         raw.NatsWsServer,
		AllowInsecureTLS:     allowInsecureTLS,
		AgentUpdate:          raw.AgentUpdate,
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
