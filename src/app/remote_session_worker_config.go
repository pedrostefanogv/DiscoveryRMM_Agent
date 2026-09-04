//go:build windows

package app

// Leitura de config para o worker de remote session (sem App inicializada).
// O worker roda como processo independente spawnado pelo serviço — não tem
// acesso à memória da App, então lê os mesmos arquivos persistidos:
//   - debug_config.json (C:\ProgramData\Discovery) — endpoints NATS + token
//   - config.json do instalador — clientId/siteId/agentId

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// workerDebugConfigFile espelha debug.debugConfigFile (não exportado).
const workerDebugConfigFile = "debug_config.json"

// workerDebugConfigRaw é o subconjunto de debug.Config que o worker precisa.
// Reusa os mesmos nomes de campo JSON do debug.Config (tags em debug/config.go).
type workerDebugConfigRaw struct {
	ApiServer    string `json:"apiServer,omitempty"`
	NatsServer   string `json:"natsServer"`
	NatsWsServer string `json:"natsWsServer"`
	AuthToken    string `json:"authToken"`
	AgentID      string `json:"agentId"`
}

// GetDebugConfigForWorker lê o debug_config.json persistido (mesmo caminho
// usado pelo debug.Service.LoadPersistedConfig).
func GetDebugConfigForWorker() WorkerDebugConfig {
	out := WorkerDebugConfig{}
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	path := filepath.Join(programData, "Discovery", workerDebugConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	data = trimJSONBOMWorker(data)
	var raw workerDebugConfigRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	out.ApiServer = raw.ApiServer
	out.NatsServer = raw.NatsServer
	out.NatsWsServer = raw.NatsWsServer
	out.AuthToken = raw.AuthToken
	out.AgentID = raw.AgentID
	return out
}

// workerInstallerConfigRaw é o subconjunto de InstallerConfig que o worker usa.
type workerInstallerConfigRaw struct {
	ApiServer string `json:"apiServer"`
	AuthToken string `json:"authToken"`
	AgentID   string `json:"agentId"`
	ClientID  string `json:"clientId"`
	SiteID    string `json:"siteId"`
}

// GetAgentConfigurationForWorker lê clientId/siteId do config.json do
// instalador (mesmo arquivo que o agentconn usa como fallback canônico).
func GetAgentConfigurationForWorker() WorkerAgentConfig {
	out := WorkerAgentConfig{}
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	path := filepath.Join(programData, "Discovery", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	data = trimJSONBOMWorker(data)
	var raw workerInstallerConfigRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	out.ClientID = raw.ClientID
	out.SiteID = raw.SiteID
	return out
}

// trimJSONBOMWorker remove BOM UTF-8 (mesmo comportamento de debug.trimJSONBOM).
func trimJSONBOMWorker(data []byte) []byte {
	return []byte(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))
}
