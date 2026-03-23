package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"

	"discovery/internal/agentconn"
)

func (a *App) loadPersistedDebugConfig() {
	for _, path := range debugConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg DebugConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			a.logs.append("[debug] falha ao ler configuracao persistida: " + err.Error())
			return
		}

		if !isValidDebugScheme(cfg.Scheme) {
			a.logs.append("[debug] configuracao persistida ignorada: scheme invalido")
			return
		}

		a.debugMu.Lock()
		a.debugConfig = cfg
		a.debugMu.Unlock()
		a.logs.append("[debug] configuracao carregada de " + path)
		return
	}
}

func (a *App) persistDebugConfig(cfg DebugConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuracao de debug: %w", err)
	}

	var errs []string
	for _, path := range debugConfigPathCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		a.logs.append("[debug] configuracao salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho valido para salvar configuracao de debug")
	}
	return fmt.Errorf("falha ao salvar configuracao de debug: %s", strings.Join(errs, " | "))
}

// DebugConfig holds server connection settings for the debug page.
type DebugConfig struct {
	// API Server (HTTP/HTTPS) - para tickets, inventario, etc
	ApiScheme string `json:"apiScheme"` // "http" or "https"
	ApiServer string `json:"apiServer"` // hostname:port or IP
	AuthToken string `json:"authToken"` // bearer token

	// NATS Server - para comandos de agente
	NatsServer string `json:"natsServer"` // hostname:port for NATS
	AgentID    string `json:"agentId"`    // agent identifier

	// Deprecated: mantidos para compatibilidade com agentConn
	Scheme string `json:"scheme,omitempty"` // "http", "https" or "nats"
	Server string `json:"server,omitempty"` // hostname:port or IP

	// AutomationP2PWingetInstallEnabled routes Winget installs through P2P-first flow in debug mode.
	AutomationP2PWingetInstallEnabled bool `json:"automationP2pWingetInstallEnabled,omitempty"`
}

// InstallerConfig is the bootstrap config saved by the NSIS installer.
type InstallerConfig struct {
	ServerURL            string    `json:"serverUrl"`
	APIKey               string    `json:"apiKey"`
	DiscoveryEnabled     *bool     `json:"discoveryEnabled,omitempty"`
	ApiScheme            string    `json:"apiScheme,omitempty"`
	ApiServer            string    `json:"apiServer,omitempty"`
	AuthToken            string    `json:"authToken,omitempty"`
	AgentID              string    `json:"agentId,omitempty"`
	P2P                  P2PConfig `json:"p2p,omitempty"`
	MeshCentralInstalled bool      `json:"meshCentralInstalled,omitempty"`
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
		P2P                  P2PConfig       `json:"p2p,omitempty"`
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
		P2P:                  normalizeP2PConfig(raw.P2P),
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
		default:
			return nil, fmt.Errorf("texto invalido: %q", asString)
		}
	}

	return nil, fmt.Errorf("formato nao suportado")
}

// GetDebugConfig returns the current debug configuration.
func (a *App) GetDebugConfig() DebugConfig {
	a.debugMu.RLock()
	defer a.debugMu.RUnlock()
	return a.debugConfig
}

func (a *App) loadConnectionConfigFromProduction() {
	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.logs.append("[config] config de producao nao encontrado: " + err.Error())
		a.applyP2PConfig(defaultP2PConfig())
		return
	}
	p2pCfg := normalizeP2PConfig(inst.P2P)
	p2pCfg.Enabled = true
	p2pCfg.DiscoveryMode = p2pDiscoveryMDNS
	a.applyP2PConfig(p2pCfg)
	a.logs.append("[config] P2P inicializado com enabled=true e discovery=mdns")

	if strings.TrimSpace(inst.ApiScheme) == "" || strings.TrimSpace(inst.ApiServer) == "" {
		scheme, server, parseErr := parseInstallerServerURL(inst.ServerURL)
		if parseErr == nil {
			inst.ApiScheme = scheme
			inst.ApiServer = server
		}
	}

	if strings.TrimSpace(inst.ApiScheme) == "" || strings.TrimSpace(inst.ApiServer) == "" {
		a.logs.append("[config] config de producao sem apiScheme/apiServer: " + path)
		return
	}

	if strings.TrimSpace(inst.AuthToken) == "" || strings.TrimSpace(inst.AgentID) == "" {
		a.logs.append("[config] config de producao carregado sem authToken/agentId (bootstrap pendente): " + path)
		return
	}

	a.applyRuntimeConnectionConfig(inst.ApiScheme, inst.ApiServer, inst.AuthToken, inst.AgentID)
	a.logs.append("[config] credenciais carregadas do config de producao: " + path)
}

func (a *App) applyRuntimeConnectionConfig(apiScheme, apiServer, authToken, agentID string) {
	cfg := a.GetDebugConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(apiScheme))
	cfg.ApiServer = strings.TrimSpace(apiServer)
	cfg.AuthToken = strings.TrimSpace(authToken)
	cfg.AgentID = strings.TrimSpace(agentID)

	if cfg.NatsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsServer
	} else {
		cfg.Scheme = cfg.ApiScheme
		cfg.Server = cfg.ApiServer
	}

	a.debugMu.Lock()
	a.debugConfig = cfg
	a.debugMu.Unlock()
}

// bootstrapAgentCredentialsFromInstallerConfig reads installer-provided URL/KEY,
// registers the agent on first startup and persists authToken+agentId.
func (a *App) bootstrapAgentCredentialsFromInstallerConfig(ctx context.Context) {
	cfg := a.GetDebugConfig()
	if strings.TrimSpace(cfg.ApiServer) != "" && strings.TrimSpace(cfg.AuthToken) != "" && strings.TrimSpace(cfg.AgentID) != "" {
		return
	}

	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.logs.append("[installer-bootstrap] sem bootstrap de instalador: " + err.Error())
		return
	}

	scheme, server, err := parseInstallerServerURL(inst.ServerURL)
	if err != nil {
		a.logs.append("[installer-bootstrap] serverUrl invalida no config do instalador: " + err.Error())
		return
	}

	if strings.TrimSpace(inst.AuthToken) != "" && strings.TrimSpace(inst.AgentID) != "" {
		a.applyRuntimeConnectionConfig(scheme, server, inst.AuthToken, inst.AgentID)
		a.logs.append("[installer-bootstrap] credenciais ja presentes no config de producao (" + path + ")")
		a.agentConn.Reload()
		return
	}

	if strings.TrimSpace(inst.APIKey) == "" {
		a.logs.append("[installer-bootstrap] apiKey ausente no config de producao; nao foi possivel gerar token")
		return
	}

	a.logs.append("[installer-bootstrap] registrando agente em " + scheme + "://" + server + "/api/agent-install/register")
	token, agentID, resolvedScheme, err := a.registerAgentFromDeployToken(ctx, scheme, server, inst.APIKey)
	if err != nil {
		a.logs.append("[installer-bootstrap] falha ao obter credenciais: " + err.Error())
		return
	}

	inst.ApiScheme = resolvedScheme
	inst.ApiServer = server
	inst.AuthToken = token
	inst.AgentID = agentID
	inst.APIKey = ""
	writePath, err := persistInstallerConfig(path, inst)
	if err != nil {
		a.logs.append("[installer-bootstrap] falha ao persistir config de producao: " + err.Error())
		return
	}
	if writePath != path {
		if err := scrubInstallerConfigSource(path, inst); err != nil {
			a.logs.append("[installer-bootstrap] aviso: falha ao limpar apiKey no config de origem: " + err.Error())
		}
	}

	a.applyRuntimeConnectionConfig(inst.ApiScheme, inst.ApiServer, inst.AuthToken, inst.AgentID)

	a.logs.append("[installer-bootstrap] credenciais aplicadas com sucesso (leitura: " + path + ", persistencia: " + writePath + ", agentId=" + agentID + ")")
	a.agentConn.Reload()
}

func installerConfigPathCandidates() []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		// 1º: C:\ProgramData\Discovery (compartilhado entre usuários)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2º: LOCALAPPDATA\Discovery (fallback compatibilidade)
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "config.json"))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "config.json"))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", "config.json"))
	}

	paths = append(paths, filepath.Join(".", "config.json"))
	return lo.Uniq(paths)
}

func installerConfigWriteCandidates(sourcePath string) []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		// 1º: C:\ProgramData\Discovery (preferência - compartilhado entre usuários)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2º: LOCALAPPDATA\Discovery (fallback compatibilidade)
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "config.json"))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", "config.json"))
	}

	paths = append(paths, filepath.Join(".", "config.json"))
	if strings.TrimSpace(sourcePath) != "" {
		paths = append(paths, sourcePath)
	}

	return lo.Uniq(paths)
}

func loadInstallerConfig() (InstallerConfig, string, error) {
	for _, path := range installerConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg InstallerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return InstallerConfig{}, "", fmt.Errorf("falha ao ler %s: %w", path, err)
		}

		cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.P2P = normalizeP2PConfig(cfg.P2P)

		if cfg.ServerURL == "" && (cfg.ApiScheme == "" || cfg.ApiServer == "") {
			continue
		}

		return cfg, path, nil
	}

	return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
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
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}

func scrubInstallerConfigSource(path string, cfg InstallerConfig) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func parseInstallerServerURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(strings.Trim(raw, `"`))
	if raw == "" {
		return "", "", fmt.Errorf("serverUrl vazio")
	}

	parsedInput := raw
	if !strings.Contains(parsedInput, "://") {
		parsedInput = "https://" + parsedInput
	}

	u, err := url.Parse(parsedInput)
	if err != nil {
		return "", "", err
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("scheme invalido: %s", scheme)
	}

	host := strings.TrimSpace(u.Host)
	if host == "" {
		host = strings.TrimSpace(u.Path)
	}
	host = strings.Trim(host, "/")
	if host == "" {
		return "", "", fmt.Errorf("host ausente em serverUrl")
	}

	return scheme, host, nil
}

func (a *App) registerAgentFromDeployToken(ctx context.Context, scheme, server, deployToken string) (string, string, string, error) {
	type registerRequest struct {
		Hostname        string `json:"hostname"`
		DisplayName     string `json:"displayName"`
		OperatingSystem string `json:"operatingSystem"`
		OSVersion       string `json:"osVersion,omitempty"`
		AgentVersion    string `json:"agentVersion"`
		MACAddress      string `json:"macAddress,omitempty"`
	}

	hostname, _ := os.Hostname()
	displayName := hostname
	osName := strings.Title(runtime.GOOS)
	osVersion := strings.TrimSpace(os.Getenv("OS"))
	macAddress := firstUsableMACAddress()

	payload := registerRequest{
		Hostname:        strings.TrimSpace(hostname),
		DisplayName:     strings.TrimSpace(displayName),
		OperatingSystem: osName,
		OSVersion:       osVersion,
		AgentVersion:    strings.TrimSpace(Version),
		MACAddress:      macAddress,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("falha ao serializar request: %w", err)
	}

	schemes := []string{scheme}
	if !strings.Contains(strings.TrimSpace(installerServerURLForLog(server, scheme)), "://") {
		schemes = []string{scheme}
	}
	if scheme == "https" {
		schemes = []string{"https", "http"}
	}
	if scheme == "http" {
		schemes = []string{"http", "https"}
	}

	var errs []string
	for _, candidateScheme := range lo.Uniq(schemes) {
		endpoint := candidateScheme + "://" + server + "/api/agent-install/register"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deployToken))

		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			errs = append(errs, endpoint+": erro ao ler resposta: "+readErr.Error())
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errs = append(errs, fmt.Sprintf("%s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body))))
			continue
		}

		token, agentID, err := extractTokenAndAgentID(body)
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		return token, agentID, candidateScheme, nil
	}

	return "", "", "", fmt.Errorf("nao foi possivel registrar agente: %s", strings.Join(errs, " | "))
}

func firstUsableMACAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := strings.TrimSpace(iface.HardwareAddr.String())
		if mac != "" {
			return strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
		}
	}
	return ""
}

func installerServerURLForLog(server, scheme string) string {
	return strings.TrimSpace(scheme) + "://" + strings.TrimSpace(server)
}

func extractTokenAndAgentID(body []byte) (string, string, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", fmt.Errorf("resposta JSON invalida: %w", err)
	}

	tryExtract := func(m map[string]any) (string, string) {
		token := firstNonEmptyAnyString(m["token"], m["authToken"], m["accessToken"])
		agentID := firstNonEmptyAnyString(m["agentId"], m["agentID"], m["id"])
		return strings.TrimSpace(token), strings.TrimSpace(agentID)
	}

	token, agentID := tryExtract(raw)
	if token != "" && agentID != "" {
		return token, agentID, nil
	}

	for _, key := range []string{"data", "result", "payload"} {
		nested, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		token, agentID = tryExtract(nested)
		if token != "" && agentID != "" {
			return token, agentID, nil
		}
	}

	return "", "", fmt.Errorf("resposta sem token/agentId")
}

func firstNonEmptyAnyString(values ...any) string {
	for _, v := range values {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

// AgentStatus is the frontend-facing agent connection snapshot.
type AgentStatus struct {
	Connected bool   `json:"connected"`
	AgentID   string `json:"agentId"`
	Server    string `json:"server"`
	LastEvent string `json:"lastEvent"`
}

// RealtimeStatus represents server-side realtime transport health.
type RealtimeStatus struct {
	NATSConnected          bool      `json:"natsConnected"`
	SignalRConnectedAgents int       `json:"signalrConnectedAgents"`
	CheckedAtUTC           time.Time `json:"checkedAtUtc"`
}

// GetAgentStatus returns the current agent connectivity status.
func (a *App) GetAgentStatus() AgentStatus {
	if a.agentConn == nil {
		return AgentStatus{}
	}
	s := a.agentConn.GetStatus()
	return AgentStatus{
		Connected: s.Connected,
		AgentID:   s.AgentID,
		Server:    s.Server,
		LastEvent: s.LastEvent,
	}
}

// SetDebugConfig validates, stores and persists the debug connection settings.
func (a *App) SetDebugConfig(cfg DebugConfig) error {
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)

	if cfg.ApiServer != "" {
		if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			return fmt.Errorf("apiScheme invalido: use 'http' ou 'https'")
		}
	}

	if cfg.NatsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			return fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
		}
	}

	if cfg.ApiServer == "" && cfg.NatsServer == "" {
		return fmt.Errorf("configure pelo menos um servidor (API ou NATS)")
	}

	if cfg.NatsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsServer
	} else if cfg.ApiServer != "" {
		cfg.Scheme = cfg.ApiScheme
		cfg.Server = cfg.ApiServer
	}

	a.logs.append(fmt.Sprintf("[debug] atualizando configuracao: api=%s://%s nats=%s agentId=%s",
		cfg.ApiScheme, cfg.ApiServer, cfg.NatsServer, cfg.AgentID))

	a.debugMu.Lock()
	a.debugConfig = cfg
	a.debugMu.Unlock()

	a.agentInfo.invalidate()

	if a.db != nil {
		if err := a.db.CacheDelete("agent_info"); err != nil {
			log.Printf("[debug] aviso: falha ao limpar cache SQLite: %v", err)
		}
	}

	if err := a.persistDebugConfig(cfg); err != nil {
		a.logs.append("[debug] falha ao persistir configuracao: " + err.Error())
		return err
	}
	if a.agentConn != nil {
		a.logs.append("[debug] solicitando reload da conexao do agente")
		a.agentConn.Reload()
	}
	a.logs.append("[debug] configuracao aplicada com sucesso")
	return nil
}

// TestDebugConnection tests connectivity to configured servers and returns diagnostic info.
func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)

	var results []string

	if cfg.ApiServer != "" {
		a.logs.append(fmt.Sprintf("[debug-test] testando API: %s://%s", cfg.ApiScheme, cfg.ApiServer))
		target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me"
		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return "", fmt.Errorf("URL invalida para API: %w", err)
		}
		if cfg.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
		}
		if cfg.AgentID != "" {
			req.Header.Set("X-Agent-ID", cfg.AgentID)
		}

		resp, err := client.Do(req)
		if err != nil {
			wrapped := fmt.Errorf("falha ao conectar na API %s: %w", target, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			wrapped := fmt.Errorf("erro ao ler resposta da API (%s): %w", resp.Status, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			wrapped := fmt.Errorf("API HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}

		var pretty interface{}
		if json.Unmarshal(body, &pretty) == nil {
			if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				results = append(results, "=== Servidor API ===\n"+string(formatted))
			} else {
				results = append(results, "=== Servidor API ===\n"+string(body))
			}
		} else {
			results = append(results, "=== Servidor API ===\n"+string(body))
		}
		a.logs.append("[debug-test] teste API concluido com sucesso")
	}

	if cfg.NatsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			err := fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
			a.logs.append("[debug-test] " + err.Error())
			return "", err
		}
		a.logs.append(fmt.Sprintf("[debug-test] testando NATS: %s", cfg.NatsServer))
		out, err := agentconn.FetchNATSInfo(cfg.NatsServer, 10*time.Second)
		if err != nil {
			wrapped := fmt.Errorf("falha ao conectar no NATS %s: %w", cfg.NatsServer, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}
		results = append(results, "=== Servidor NATS ===\n"+out)
		a.logs.append("[debug-test] teste NATS concluido com sucesso")
	}

	if len(results) == 0 {
		return "", fmt.Errorf("nenhum servidor configurado para testar")
	}

	return strings.Join(results, "\n\n"), nil
}

// GetRealtimeStatus queries /api/realtime/status from the configured HTTP server.
func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	cfg := a.GetDebugConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	if cfg.ApiServer == "" {
		return RealtimeStatus{}, fmt.Errorf("servidor API nao configurado")
	}
	if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
		return RealtimeStatus{}, fmt.Errorf("apiScheme invalido: use http ou https")
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/realtime/status"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("URL invalida: %w", err)
	}
	setAgentAuthHeaders(req, cfg.AuthToken)
	if strings.TrimSpace(cfg.AgentID) != "" {
		req.Header.Set("X-Agent-ID", cfg.AgentID)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("falha ao conectar em %s: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("erro ao ler resposta (%s): %w", resp.Status, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RealtimeStatus{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out RealtimeStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return RealtimeStatus{}, fmt.Errorf("resposta invalida de /api/realtime/status: %w", err)
	}
	return out, nil
}

func isValidDebugScheme(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "http" || s == "https" || s == "nats"
}
