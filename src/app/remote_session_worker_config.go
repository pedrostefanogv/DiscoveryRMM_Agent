//go:build windows

package app

// Merge de overrides do payload no config do worker (2026-10-27).
//
// O serviço já injeta o token VIVO no payload (bug de rotação de 2026-09-20).
// Pelo mesmo motivo, injetamos os endpoints e a identidade: o worker não deve
// depender de debug_config.json/config.json possivelmente velhos, e o serviço
// sabe qual endpoint o agent core está usando.
//
// O payload tem prioridade; arquivos continuam como fallback.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// workerDebugConfigFile espelha debug.debugConfigFile (não exportado).
const workerDebugConfigFile = "debug_config.json"

// workerDebugConfigRaw é o subconjunto não-secreto de debug.Config.
// authToken NUNCA é lido do disco pelo worker (chega estritamente via stdin).
type workerDebugConfigRaw struct {
	ApiScheme                string `json:"apiScheme,omitempty"`
	ApiServer                string `json:"apiServer,omitempty"`
	NatsServer               string `json:"natsServer"`
	NatsWsServer             string `json:"natsWsServer"`
	AgentID                  string `json:"agentId"`
	AllowInsecureTLS         bool   `json:"allowInsecureTls,omitempty"`
	NatsServerHost           string `json:"natsServerHost,omitempty"`
	NatsServerHostInternal   string `json:"natsServerHostInternal,omitempty"`
	NatsUseWssExternal       bool   `json:"natsUseWssExternal,omitempty"`
	EnforceTLSHashValidation bool   `json:"enforceTlsHashValidation,omitempty"`
	HandshakeEnabled         bool   `json:"handshakeEnabled,omitempty"`
	ApiTLSCertHash           string `json:"apiTlsCertHash,omitempty"`
	NatsTLSCertHash          string `json:"natsTlsCertHash,omitempty"`
}

// GetDebugConfigForWorker lê campos não-secretos do debug_config.json (quando
// acessível). Segredos (AuthToken) são recebidos estritamente via stdin.
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
	out.ApiScheme = raw.ApiScheme
	out.ApiServer = raw.ApiServer
	out.NatsServer = raw.NatsServer
	out.NatsWsServer = raw.NatsWsServer
	out.AgentID = raw.AgentID
	out.AllowInsecureTLS = raw.AllowInsecureTLS
	out.NatsServerHost = raw.NatsServerHost
	out.NatsServerHostInternal = raw.NatsServerHostInternal
	out.NatsUseWssExternal = raw.NatsUseWssExternal
	out.EnforceTLSHashValidation = raw.EnforceTLSHashValidation
	out.HandshakeEnabled = raw.HandshakeEnabled
	out.ApiTLSCertHash = raw.ApiTLSCertHash
	out.NatsTLSCertHash = raw.NatsTLSCertHash
	return out
}

// workerInstallerConfigRaw é o subconjunto não-secreto de InstallerConfig.
type workerInstallerConfigRaw struct {
	ApiServer string `json:"apiServer"`
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

// stringFromWorkerPayload extrai uma string não vazia do payload do worker.
func stringFromWorkerPayload(cmd map[string]any, key string) string {
	if cmd == nil {
		return ""
	}
	if v, ok := cmd[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolFromWorkerPayload(cmd map[string]any, key string) (bool, bool) {
	if cmd == nil {
		return false, false
	}
	v, ok := cmd[key].(bool)
	return v, ok
}

// mergeWorkerDebugConfigFromPayload aplica os overrides do payload sobre o
// config do worker. Segredos (authToken) e hints TLS/identidade do serviço têm
// precedência absoluta.
func mergeWorkerDebugConfigFromPayload(cfg WorkerDebugConfig, cmd map[string]any) WorkerDebugConfig {
	if v := stringFromWorkerPayload(cmd, "authToken"); v != "" {
		cfg.AuthToken = v
	}
	if v := stringFromWorkerPayload(cmd, "agentId"); v != "" {
		cfg.AgentID = v
	}
	if v := stringFromWorkerPayload(cmd, "natsCurrentUrl"); v != "" {
		cfg.NatsCurrentURL = v
	}
	if v := stringFromWorkerPayload(cmd, "natsServer"); v != "" {
		cfg.NatsServer = v
	}
	if v := stringFromWorkerPayload(cmd, "natsWsServer"); v != "" {
		cfg.NatsWsServer = v
	}
	if v := stringFromWorkerPayload(cmd, "apiServer"); v != "" {
		cfg.ApiServer = v
	}
	if v := stringFromWorkerPayload(cmd, "apiScheme"); v != "" {
		cfg.ApiScheme = v
	}
	if v, ok := boolFromWorkerPayload(cmd, "allowInsecureTls"); ok {
		cfg.AllowInsecureTLS = v
	}
	if v := stringFromWorkerPayload(cmd, "natsServerHost"); v != "" {
		cfg.NatsServerHost = v
	}
	if v := stringFromWorkerPayload(cmd, "natsServerHostInternal"); v != "" {
		cfg.NatsServerHostInternal = v
	}
	if v, ok := boolFromWorkerPayload(cmd, "natsUseWssExternal"); ok {
		cfg.NatsUseWssExternal = v
	}
	if v, ok := boolFromWorkerPayload(cmd, "enforceTlsHashValidation"); ok {
		cfg.EnforceTLSHashValidation = v
	}
	if v, ok := boolFromWorkerPayload(cmd, "handshakeEnabled"); ok {
		cfg.HandshakeEnabled = v
	}
	if v := stringFromWorkerPayload(cmd, "apiTlsCertHash"); v != "" {
		cfg.ApiTLSCertHash = v
	}
	if v := stringFromWorkerPayload(cmd, "natsTlsCertHash"); v != "" {
		cfg.NatsTLSCertHash = v
	}
	return cfg
}

// mergeWorkerAgentConfigFromPayload aplica clientId/siteId do payload.
func mergeWorkerAgentConfigFromPayload(cfg WorkerAgentConfig, cmd map[string]any) WorkerAgentConfig {
	if v := stringFromWorkerPayload(cmd, "clientId"); v != "" {
		cfg.ClientID = v
	}
	if v := stringFromWorkerPayload(cmd, "siteId"); v != "" {
		cfg.SiteID = v
	}
	return cfg
}

// GetInstallerAuthTokenForWorker foi desativado por segurança (F12): config.json
// é restrito a SYSTEM e protegido por DPAPI, portanto o worker NÃO lê authToken
// do disco — recebe estritamente via stdin do serviço (fail-closed).
func GetInstallerAuthTokenForWorker() string {
	return ""
}

// GetAgentConfigurationForWorker lê clientId/siteId do config.json do
// instalador (fallback apenas quando não injetados via stdin).
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
