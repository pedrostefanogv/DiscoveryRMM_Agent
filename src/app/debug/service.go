package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"

	"discovery/app/netutil"
	"discovery/app/p2pmeta"
	"discovery/internal/agentconn"
	"discovery/internal/tlsutil"
)

const debugConfigFile = "debug_config.json"

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AgentConn abstracts the agent connection runtime.
type AgentConn interface {
	Reload()
	GetStatus() agentconn.Status
}

// AgentInfoCache exposes the cache invalidation needed by debug actions.
type AgentInfoCache interface {
	Invalidate()
}

// CacheDB exposes the cache invalidation needed by debug actions.
type CacheDB interface {
	CacheDelete(key string) error
}

// Options configures the debug service.
type Options struct {
	Logf               func(string)
	AgentConn          AgentConn
	AgentInfo          AgentInfoCache
	DB                 CacheDB
	NormalizeP2PConfig func(p2pmeta.Config) p2pmeta.Config
	ApplyP2PConfig     func(p2pmeta.Config)
	DefaultP2PConfig   func() p2pmeta.Config
	Version            string
}

// Service owns runtime debug configuration and related workflows.
type Service struct {
	mu     sync.RWMutex
	config Config

	logf               func(string)
	agentConn          AgentConn
	agentInfo          AgentInfoCache
	db                 CacheDB
	normalizeP2PConfig func(p2pmeta.Config) p2pmeta.Config
	applyP2PConfig     func(p2pmeta.Config)
	defaultP2PConfig   func() p2pmeta.Config
	version            string
}

// NewService builds a debug service with its dependencies.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	tlsutil.SetConfigAllowInsecureTLS(false)
	return &Service{
		logf:               logf,
		agentConn:          opts.AgentConn,
		agentInfo:          opts.AgentInfo,
		db:                 opts.DB,
		normalizeP2PConfig: opts.NormalizeP2PConfig,
		applyP2PConfig:     opts.ApplyP2PConfig,
		defaultP2PConfig:   opts.DefaultP2PConfig,
		version:            strings.TrimSpace(opts.Version),
	}
}

func installerConfigAllowInsecureTLS(cfg InstallerConfig) bool {
	return cfg.AllowInsecureTLS != nil && *cfg.AllowInsecureTLS
}

func (s *Service) applyAllowInsecureTLS(allow bool) {
	s.mu.Lock()
	cfg := s.config
	cfg.AllowInsecureTLS = allow
	s.config = cfg
	s.mu.Unlock()
	tlsutil.SetConfigAllowInsecureTLS(allow)
}

// LoadPersistedConfig loads debug configuration from disk if available.
func (s *Service) LoadPersistedConfig() {
	for _, path := range debugConfigPathCandidates() {
		data, err := osReadFile(path)
		if err != nil {
			continue
		}
		data = trimJSONBOM(data)

		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			s.logf("[debug] falha ao ler configuracao persistida: " + err.Error())
			return
		}

		if !isValidDebugScheme(cfg.Scheme) {
			s.logf("[debug] configuracao persistida ignorada: scheme invalido")
			return
		}
		normalizeSecurityConfig(&cfg)

		s.mu.Lock()
		s.config = cfg
		s.mu.Unlock()
		tlsutil.SetConfigAllowInsecureTLS(cfg.AllowInsecureTLS)
		s.logf("[debug] configuracao carregada de " + path)
		return
	}
}

// PersistConfig persists debug configuration to disk.
func (s *Service) PersistConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuracao de debug: %w", err)
	}

	var errs []string
	for _, path := range debugConfigPathCandidates() {
		dir := filepath.Dir(path)
		if err := osMkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := osWriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		s.logf("[debug] configuracao salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho valido para salvar configuracao de debug")
	}
	return fmt.Errorf("falha ao salvar configuracao de debug: %s", strings.Join(errs, " | "))
}

// GetConfig returns the current debug configuration.
func (s *Service) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// LoadConnectionConfigFromProduction loads bootstrap configuration and applies runtime settings.
func (s *Service) LoadConnectionConfigFromProduction() {
	inst, path, err := s.loadInstallerConfig()
	if err != nil {
		s.applyAllowInsecureTLS(false)
		s.logf("[config] config de producao nao encontrado: " + err.Error())
		if s.defaultP2PConfig != nil && s.applyP2PConfig != nil {
			s.applyP2PConfig(s.defaultP2PConfig())
		}
		return
	}
	s.applyAllowInsecureTLS(installerConfigAllowInsecureTLS(inst))
	if s.applyP2PConfig != nil {
		p2pCfg := inst.P2P
		if s.normalizeP2PConfig != nil {
			p2pCfg = s.normalizeP2PConfig(p2pCfg)
		}
		p2pCfg.Enabled = true
		s.applyP2PConfig(p2pCfg)
		s.logf("[config] P2P inicializado com enabled=true")
	}

	if strings.TrimSpace(inst.ApiScheme) == "" || strings.TrimSpace(inst.ApiServer) == "" {
		scheme, server, parseErr := parseInstallerServerURL(inst.ServerURL)
		if parseErr == nil {
			inst.ApiScheme = scheme
			inst.ApiServer = server
		}
	}

	if strings.TrimSpace(inst.ApiScheme) == "" || strings.TrimSpace(inst.ApiServer) == "" {
		if strings.TrimSpace(inst.NatsServer) == "" && strings.TrimSpace(inst.NatsWsServer) == "" {
			s.logf("[config] config de producao sem apiScheme/apiServer e sem NATS: " + path)
			return
		}
	}

	if strings.TrimSpace(inst.AuthToken) == "" || strings.TrimSpace(inst.AgentID) == "" {
		s.logf("[config] config de producao carregado sem authToken/agentId (bootstrap pendente): " + path)
		return
	}

	s.ApplyRuntimeConnectionConfig(inst.ApiScheme, inst.ApiServer, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)
	s.logf("[config] credenciais carregadas do config de producao: " + path)
}

// ApplyRuntimeConnectionConfig updates runtime connection settings.
func (s *Service) ApplyRuntimeConnectionConfig(apiScheme, apiServer, authToken, agentID, natsServer, natsWsServer string) {
	cfg := s.GetConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(apiScheme))
	cfg.ApiServer = strings.TrimSpace(apiServer)
	cfg.AuthToken = strings.TrimSpace(authToken)
	cfg.AgentID = strings.TrimSpace(agentID)
	cfg.NatsServer = strings.TrimSpace(natsServer)
	cfg.NatsWsServer = strings.TrimSpace(natsWsServer)
	normalizeSecurityConfig(&cfg)

	if cfg.NatsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsServer
	} else if cfg.NatsWsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsWsServer
	} else {
		cfg.Scheme = cfg.ApiScheme
		cfg.Server = cfg.ApiServer
	}

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	tlsutil.SetConfigAllowInsecureTLS(cfg.AllowInsecureTLS)
}

// ApplyRemoteConnectionSecurity updates TLS pinning and transport hardening values from /me/configuration.
// It persists the updated runtime config and forces reconnect so new policies apply immediately.
func (s *Service) ApplyRemoteConnectionSecurity(natsServerHost string, natsUseWssExternal, enforceTLSHashValidation, handshakeEnabled *bool, apiTLSCertHash, natsTLSCertHash string) (bool, error) {
	s.mu.Lock()
	cfg := s.config

	nextNatsServerHost := strings.TrimSpace(natsServerHost)
	nextApiTLSCertHash := strings.ToUpper(strings.TrimSpace(apiTLSCertHash))
	nextNatsTLSCertHash := strings.ToUpper(strings.TrimSpace(natsTLSCertHash))

	changed := false
	if cfg.NatsServerHost != nextNatsServerHost {
		cfg.NatsServerHost = nextNatsServerHost
		changed = true
	}
	if natsUseWssExternal != nil && cfg.NatsUseWssExternal != *natsUseWssExternal {
		cfg.NatsUseWssExternal = *natsUseWssExternal
		changed = true
	}
	if cfg.ApiTlsCertHash != nextApiTLSCertHash {
		cfg.ApiTlsCertHash = nextApiTLSCertHash
		changed = true
	}
	if cfg.NatsTlsCertHash != nextNatsTLSCertHash {
		cfg.NatsTlsCertHash = nextNatsTLSCertHash
		changed = true
	}
	if enforceTLSHashValidation != nil && cfg.EnforceTlsHashValidation != *enforceTLSHashValidation {
		cfg.EnforceTlsHashValidation = *enforceTLSHashValidation
		changed = true
	}
	if handshakeEnabled != nil && cfg.HandshakeEnabled != *handshakeEnabled {
		cfg.HandshakeEnabled = *handshakeEnabled
		changed = true
	}

	normalizeSecurityConfig(&cfg)
	s.config = cfg
	s.mu.Unlock()
	tlsutil.SetConfigAllowInsecureTLS(cfg.AllowInsecureTLS)

	if !changed {
		return false, nil
	}

	persistErr := s.PersistConfig(cfg)
	if persistErr != nil {
		s.logf("[debug] falha ao persistir atualizacao de seguranca remota: " + persistErr.Error())
	}
	if s.agentConn != nil {
		s.logf("[debug] recarregando conexao apos atualizacao de seguranca remota")
		s.agentConn.Reload()
	}

	if persistErr != nil {
		return true, persistErr
	}
	return true, nil
}

func (s *Service) testAgentAPIConnectivity(ctx context.Context, apiScheme, apiServer, authToken, agentID string) error {
	apiScheme = strings.TrimSpace(strings.ToLower(apiScheme))
	apiServer = strings.TrimSpace(apiServer)
	authToken = strings.TrimSpace(authToken)
	agentID = strings.TrimSpace(agentID)

	if apiScheme == "" || apiServer == "" {
		return fmt.Errorf("apiScheme/apiServer ausente")
	}

	target := apiScheme + "://" + apiServer + "/api/v1/agent-auth/me/configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("falha ao criar requisicao: %w", err)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("falha ao conectar na API %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		preview := strings.TrimSpace(string(body))
		return fmt.Errorf("API HTTP %s: %s", resp.Status, preview)
	}

	return nil
}

// BootstrapAgentCredentialsFromInstallerConfig registers the agent when needed and persists token+agentId.
func (s *Service) BootstrapAgentCredentialsFromInstallerConfig(ctx context.Context) {
	inst, path, err := s.loadInstallerConfig()
	if err != nil {
		s.logf("[installer-bootstrap] sem bootstrap de instalador: " + err.Error())
		return
	}
	s.applyAllowInsecureTLS(installerConfigAllowInsecureTLS(inst))

	scheme, server, err := parseInstallerServerURL(inst.ServerURL)
	if err != nil {
		s.logf("[installer-bootstrap] serverUrl invalida no config do instalador: " + err.Error())
		return
	}

	inst.ApiScheme = scheme
	inst.ApiServer = server

	if strings.TrimSpace(inst.AuthToken) != "" && strings.TrimSpace(inst.AgentID) != "" {
		s.ApplyRuntimeConnectionConfig(scheme, server, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)
		s.logf("[installer-bootstrap] credenciais presentes no config de producao (" + path + ")")
		if s.agentConn != nil {
			s.agentConn.Reload()
		}

		testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.testAgentAPIConnectivity(testCtx, scheme, server, inst.AuthToken, inst.AgentID)
		cancel()
		if err == nil {
			if strings.TrimSpace(inst.APIKey) != "" {
				inst.APIKey = ""
				writePath, err := persistInstallerConfig(path, inst)
				if err != nil {
					s.logf("[installer-bootstrap] falha ao limpar deploy token: " + err.Error())
				} else {
					if writePath != path {
						if err := scrubInstallerConfigSource(path, inst); err != nil {
							s.logf("[installer-bootstrap] aviso: falha ao limpar deploy token no config de origem: " + err.Error())
						}
					}
					if overridePath := findInstallerOverridePath(); overridePath != "" && overridePath != path {
						if err := scrubInstallerConfigSource(overridePath, inst); err != nil {
							s.logf("[installer-bootstrap] aviso: falha ao limpar deploy token no override: " + err.Error())
						}
					}
				}
			}
			s.logf("[installer-bootstrap] credenciais validas; deploy token removido")
			return
		}
		s.logf("[installer-bootstrap] credenciais invalidas, tentando re-registrar: " + err.Error())
	}

	if strings.TrimSpace(inst.APIKey) == "" {
		s.logf("[installer-bootstrap] deploy token ausente; nao foi possivel registrar agente")
		return
	}

	s.logf("[installer-bootstrap] registrando agente em " + scheme + "://" + server + "/api/v1/agent-install/register")
	token, agentID, resolvedScheme, err := s.registerAgentFromDeployToken(ctx, scheme, server, inst.APIKey)
	if err != nil {
		s.logf("[installer-bootstrap] falha ao obter credenciais: " + err.Error())
		return
	}

	inst.ApiScheme = resolvedScheme
	inst.ApiServer = server
	inst.AuthToken = token
	inst.AgentID = agentID
	inst.APIKey = ""
	writePath, err := persistInstallerConfig(path, inst)
	if err != nil {
		s.logf("[installer-bootstrap] falha ao persistir config de producao: " + err.Error())
		return
	}
	if writePath != path {
		if err := scrubInstallerConfigSource(path, inst); err != nil {
			s.logf("[installer-bootstrap] aviso: falha ao limpar deploy token no config de origem: " + err.Error())
		}
	}
	if overridePath := findInstallerOverridePath(); overridePath != "" && overridePath != path {
		if err := scrubInstallerConfigSource(overridePath, inst); err != nil {
			s.logf("[installer-bootstrap] aviso: falha ao limpar deploy token no override: " + err.Error())
		}
	}

	s.ApplyRuntimeConnectionConfig(inst.ApiScheme, inst.ApiServer, inst.AuthToken, inst.AgentID, inst.NatsServer, inst.NatsWsServer)

	s.logf("[installer-bootstrap] credenciais aplicadas com sucesso (leitura: " + path + ", persistencia: " + writePath + ", agentId=" + agentID + ")")
	if s.agentConn != nil {
		s.agentConn.Reload()
	}
}

// GetAgentStatus returns the current agent connectivity status.
func (s *Service) GetAgentStatus() AgentStatus {
	if s.agentConn == nil {
		return AgentStatus{}
	}
	src := s.agentConn.GetStatus()
	return AgentStatus{
		Connected: src.Connected,
		AgentID:   src.AgentID,
		Server:    src.Server,
		LastEvent: src.LastEvent,
		Transport: src.Transport,
	}
}

// SetConfig validates, stores and persists the debug connection settings.
func (s *Service) SetConfig(cfg Config) error {
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.NatsWsServer = strings.TrimSpace(cfg.NatsWsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
	normalizeSecurityConfig(&cfg)

	if cfg.ApiServer != "" {
		if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			return fmt.Errorf("apiScheme invalido: use 'http' ou 'https'")
		}
	}

	if cfg.NatsServer != "" || cfg.NatsWsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			return fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
		}
	}

	if cfg.ApiServer == "" && cfg.NatsServer == "" && cfg.NatsWsServer == "" {
		return fmt.Errorf("configure pelo menos um servidor (API ou NATS)")
	}

	if cfg.NatsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsServer
	} else if cfg.NatsWsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsWsServer
	} else if cfg.ApiServer != "" {
		cfg.Scheme = cfg.ApiScheme
		cfg.Server = cfg.ApiServer
	}

	s.logf(fmt.Sprintf("[debug] atualizando configuracao: api=%s://%s nats=%s natsWs=%s agentId=%s",
		cfg.ApiScheme, cfg.ApiServer, cfg.NatsServer, cfg.NatsWsServer, cfg.AgentID))

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	tlsutil.SetConfigAllowInsecureTLS(cfg.AllowInsecureTLS)

	if s.agentInfo != nil {
		s.agentInfo.Invalidate()
	}

	if s.db != nil {
		if err := s.db.CacheDelete("agent_info"); err != nil {
			log.Printf("[debug] aviso: falha ao limpar cache SQLite: %v", err)
		}
	}

	if err := s.PersistConfig(cfg); err != nil {
		s.logf("[debug] falha ao persistir configuracao: " + err.Error())
		return err
	}
	if s.agentConn != nil {
		s.logf("[debug] solicitando reload da conexao do agente")
		s.agentConn.Reload()
	}
	s.logf("[debug] configuracao aplicada com sucesso")
	return nil
}

// TestConnection tests connectivity to configured servers and returns diagnostic info.
func (s *Service) TestConnection(cfg Config) (string, error) {
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.NatsWsServer = strings.TrimSpace(cfg.NatsWsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
	previousAllowInsecureTLS := tlsutil.ConfigAllowInsecureTLS()
	tlsutil.SetConfigAllowInsecureTLS(cfg.AllowInsecureTLS)
	defer tlsutil.SetConfigAllowInsecureTLS(previousAllowInsecureTLS)

	var results []string

	if cfg.ApiServer != "" {
		s.logf(fmt.Sprintf("[debug-test] testando API: %s://%s", cfg.ApiScheme, cfg.ApiServer))
		target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/configuration"
		client := tlsutil.NewHTTPClient(10 * time.Second)
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
			s.logf("[debug-test] " + wrapped.Error())
			return "", wrapped
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			wrapped := fmt.Errorf("erro ao ler resposta da API (%s): %w", resp.Status, err)
			s.logf("[debug-test] " + wrapped.Error())
			return "", wrapped
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			wrapped := fmt.Errorf("API HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			s.logf("[debug-test] " + wrapped.Error())
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
		s.logf("[debug-test] teste API concluido com sucesso")
	}

	if cfg.NatsServer != "" || cfg.NatsWsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			err := fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
			s.logf("[debug-test] " + err.Error())
			return "", err
		}

		if cfg.NatsServer != "" {
			s.logf(fmt.Sprintf("[debug-test] testando NATS: %s", cfg.NatsServer))
			out, err := agentconn.FetchNATSInfo(cfg.NatsServer, 10*time.Second, cfg.AuthToken)
			if err != nil {
				wrapped := fmt.Errorf("falha ao conectar no NATS %s: %w", cfg.NatsServer, err)
				s.logf("[debug-test] " + wrapped.Error())
				return "", wrapped
			}
			results = append(results, "=== Servidor NATS ===\n"+out)
			s.logf("[debug-test] teste NATS concluido com sucesso")
		}

		if cfg.NatsWsServer != "" {
			s.logf(fmt.Sprintf("[debug-test] testando NATS WS: %s", cfg.NatsWsServer))
			out, err := agentconn.FetchNATSInfo(cfg.NatsWsServer, 10*time.Second, cfg.AuthToken)
			if err != nil {
				wrapped := fmt.Errorf("falha ao conectar no NATS WS %s: %w", cfg.NatsWsServer, err)
				s.logf("[debug-test] " + wrapped.Error())
				return "", wrapped
			}
			results = append(results, "=== Servidor NATS WS ===\n"+out)
			s.logf("[debug-test] teste NATS WS concluido com sucesso")
		}
	}

	if len(results) == 0 {
		return "", fmt.Errorf("nenhum servidor configurado para testar")
	}

	return strings.Join(results, "\n\n"), nil
}

// GetRealtimeStatus queries /api/realtime/status from the configured HTTP server.
func (s *Service) GetRealtimeStatus() (RealtimeStatus, error) {
	cfg := s.GetConfig()
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
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)
	if strings.TrimSpace(cfg.AgentID) != "" {
		req.Header.Set("X-Agent-ID", cfg.AgentID)
	}

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
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

func normalizeSecurityConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.NatsServerHost = strings.TrimSpace(cfg.NatsServerHost)
	cfg.ApiTlsCertHash = strings.ToUpper(strings.TrimSpace(cfg.ApiTlsCertHash))
	cfg.NatsTlsCertHash = strings.ToUpper(strings.TrimSpace(cfg.NatsTlsCertHash))
	if !cfg.HandshakeEnabled {
		// Mantemos handshake seguro ativo por padrao para evitar downgrade silencioso.
		cfg.HandshakeEnabled = true
	}
}

func debugConfigPathCandidates() []string {
	paths := make([]string, 0, 4)

	if runtime.GOOS == "windows" {
		// 1o: C:\ProgramData\Discovery (compartilhado entre usuarios)
		programData := strings.TrimSpace(osGetenv("ProgramData"))
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return []string{filepath.Join(programData, "Discovery", debugConfigFile)}
	}

	if exe, err := osExecutable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), debugConfigFile))
	}

	if home, err := osUserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", debugConfigFile))
	}

	paths = append(paths, filepath.Join(".", debugConfigFile))
	return lo.Uniq(paths)
}

// os wrappers to simplify tests.
var (
	osReadFile    = os.ReadFile
	osWriteFile   = os.WriteFile
	osMkdirAll    = os.MkdirAll
	osExecutable  = os.Executable
	osUserHomeDir = os.UserHomeDir
	osGetenv      = os.Getenv
)
