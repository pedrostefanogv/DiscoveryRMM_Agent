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

// stringAuthTokenFromPayload extrai o authToken injetado no payload do comando
// (o serviço injeta o token VIVO em memória no spawn — ver
// remote_session_worker_spawn/remote_debug_commands). Retorna "" se ausente.
func stringAuthTokenFromPayload(cmd map[string]any) string {
	if cmd == nil {
		return ""
	}
	if v, ok := cmd["authToken"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// GetInstallerAuthTokenForWorker lê o authToken do config.json do instalador
// (mesmo arquivo que o agentconn usa como fallback canônico).
//
// POR QUÊ (bug 2026-09-20 — "nats: Authorization Violation" no worker de
// remote session): o token do agente é ROTACIONADO pelo P2P/zero-touch e a
// persistência da rotação vai para o config.json (persistInstallerConfig).
// O debug_config.json NÃO é reescrito pela rotação — fica com o token
// original. O worker lia o token APENAS do debug_config.json e conectava o
// NATS com token vencido → "Authorization Violation" nos dois endpoints
// (nats:// e wss://) → sessão de terminal/acesso remoto morria no spawn
// (evidência: agent-service.log 10:42:20-10:42:40, 4 sessões). O agent
// principal conecta com o token vivo em memória — por isso só o worker falhava.
func GetInstallerAuthTokenForWorker() string {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\\ProgramData`
	}
	path := filepath.Join(programData, "Discovery", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	data = trimJSONBOMWorker(data)
	var raw workerInstallerConfigRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	return strings.TrimSpace(raw.AuthToken)
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
