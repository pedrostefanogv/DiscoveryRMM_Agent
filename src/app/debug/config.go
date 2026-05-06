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
	// Schema canônico (camelCase) — usado pelo instalador NSIS.
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

	// Schema alternativo (snake_case) — usado pelo ServiceManager (SharedConfig)
	// quando persiste o config.json em C:\ProgramData\Discovery\config.json.
	type snakeCaseInstallerConfig struct {
		ServerURL            string             `json:"server_url"`
		DeployToken          string             `json:"deploy_token,omitempty"`
		APIKey               string             `json:"api_key"`
		AutoProvisioning     json.RawMessage    `json:"auto_provisioning,omitempty"`
		ApiScheme            string             `json:"api_scheme,omitempty"`
		ApiServer            string             `json:"api_server,omitempty"`
		AuthToken            string             `json:"auth_token,omitempty"`
		AgentID              string             `json:"agent_id,omitempty"`
		ClientID             string             `json:"client_id,omitempty"`
		SiteID               string             `json:"site_id,omitempty"`
		NatsServer           string             `json:"nats_server,omitempty"`
		NatsWsServer         string             `json:"nats_ws_server,omitempty"`
		AllowInsecureTLS     json.RawMessage    `json:"allow_insecure_tls,omitempty"`
		AgentUpdate          *selfupdate.Policy `json:"agent_update,omitempty"`
		P2P                  p2pmeta.Config     `json:"p2p,omitempty"`
		MeshCentralInstalled bool               `json:"mesh_central_installed,omitempty"`
	}

	var raw rawInstallerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Se o parse camelCase não preencheu campos críticos, tenta snake_case.
	if strings.TrimSpace(raw.ServerURL) == "" && strings.TrimSpace(raw.AuthToken) == "" && strings.TrimSpace(raw.AgentID) == "" {
		var snake snakeCaseInstallerConfig
		if err := json.Unmarshal(data, &snake); err == nil {
			raw.ServerURL = coalesceStr(raw.ServerURL, snake.ServerURL)
			raw.DeployToken = coalesceStr(raw.DeployToken, snake.DeployToken)
			raw.APIKey = coalesceStr(raw.APIKey, snake.APIKey)
			raw.ApiScheme = coalesceStr(raw.ApiScheme, snake.ApiScheme)
			raw.ApiServer = coalesceStr(raw.ApiServer, snake.ApiServer)
			raw.AuthToken = coalesceStr(raw.AuthToken, snake.AuthToken)
			raw.AgentID = coalesceStr(raw.AgentID, snake.AgentID)
			raw.ClientID = coalesceStr(raw.ClientID, snake.ClientID)
			raw.SiteID = coalesceStr(raw.SiteID, snake.SiteID)
			raw.NatsServer = coalesceStr(raw.NatsServer, snake.NatsServer)
			raw.NatsWsServer = coalesceStr(raw.NatsWsServer, snake.NatsWsServer)
			raw.MeshCentralInstalled = snake.MeshCentralInstalled
			if raw.AutoProvisioning == nil {
				raw.AutoProvisioning = snake.AutoProvisioning
			}
			if raw.AllowInsecureTLS == nil {
				raw.AllowInsecureTLS = snake.AllowInsecureTLS
			}
			if raw.AgentUpdate == nil {
				raw.AgentUpdate = snake.AgentUpdate
			}
		}
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

// coalesceStr retorna a primeira string não vazia.
func coalesceStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	Connected                  bool   `json:"connected"`
	TransportConnected         bool   `json:"transportConnected,omitempty"`
	AgentID                    string `json:"agentId"`
	Server                     string `json:"server"`
	LastEvent                  string `json:"lastEvent"`
	Transport                  string `json:"transport,omitempty"`
	OnlineReason               string `json:"onlineReason,omitempty"`
	LastGlobalPongAtUTC        string `json:"lastGlobalPongAtUtc,omitempty"`
	GlobalPongStale            bool   `json:"globalPongStale,omitempty"`
	NonCriticalBackoffUntilUTC string `json:"nonCriticalBackoffUntilUtc,omitempty"`
	NonCriticalBackoffReason   string `json:"nonCriticalBackoffReason,omitempty"`
}

// RealtimeStatus represents server-side realtime transport health.
type RealtimeStatus struct {
	NATSConnected           bool      `json:"natsConnected"`
	RealtimeConnectedAgents int       `json:"realtimeConnectedAgents"`
	CheckedAtUTC            time.Time `json:"checkedAtUtc"`
}
