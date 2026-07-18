package debug

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

	"discovery/app/netutil"
	"discovery/app/p2pmeta"
	"discovery/internal/platform"
	"discovery/internal/tlsutil"

	"github.com/samber/lo"
)

var utf8Bom = []byte{0xEF, 0xBB, 0xBF}

func trimJSONBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, utf8Bom)
}

func installerConfigPathCandidates() []string {
	return platform.ConfigPathCandidates()
}

func installerOverridePathCandidates() []string {
	return platform.InstallerOverridePathCandidates()
}

func loadInstallerConfigFromCandidates(paths []string, normalizeP2P func(p2pmeta.Config) p2pmeta.Config) (InstallerConfig, string, bool, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[config] aviso: não foi possível ler config candidate %s: %v", path, err)
			}
			continue
		}
		data = trimJSONBOM(data)

		var cfg InstallerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return InstallerConfig{}, "", false, fmt.Errorf("falha ao ler %s: %w", path, err)
		}

		cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.ClientID = strings.TrimSpace(cfg.ClientID)
		cfg.SiteID = strings.TrimSpace(cfg.SiteID)
		cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
		cfg.NatsWsServer = strings.TrimSpace(cfg.NatsWsServer)
		if normalizeP2P != nil {
			cfg.P2P = normalizeP2P(cfg.P2P)
		}

		return cfg, path, true, nil
	}

	return InstallerConfig{}, "", false, nil
}

func mergeInstallerOverride(base, override InstallerConfig) InstallerConfig {
	if strings.TrimSpace(override.ServerURL) != "" {
		base.ServerURL = strings.TrimSpace(override.ServerURL)
	}
	if strings.TrimSpace(override.APIKey) != "" {
		base.APIKey = strings.TrimSpace(override.APIKey)
	}
	if strings.TrimSpace(override.ApiScheme) != "" {
		base.ApiScheme = strings.TrimSpace(strings.ToLower(override.ApiScheme))
	}
	if strings.TrimSpace(override.ApiServer) != "" {
		base.ApiServer = strings.TrimSpace(override.ApiServer)
	}
	if strings.TrimSpace(override.ClientID) != "" {
		base.ClientID = strings.TrimSpace(override.ClientID)
	}
	if strings.TrimSpace(override.SiteID) != "" {
		base.SiteID = strings.TrimSpace(override.SiteID)
	}
	if override.AutoProvisioning != nil {
		base.AutoProvisioning = override.AutoProvisioning
	}
	if override.AllowInsecureTLS != nil {
		base.AllowInsecureTLS = override.AllowInsecureTLS
	}
	if override.ApiInsecure != nil {
		base.ApiInsecure = override.ApiInsecure
	}
	if strings.TrimSpace(override.NatsServer) != "" {
		base.NatsServer = strings.TrimSpace(override.NatsServer)
	}
	if strings.TrimSpace(override.NatsWsServer) != "" {
		base.NatsWsServer = strings.TrimSpace(override.NatsWsServer)
	}
	return base
}

func findInstallerOverridePath() string {
	for _, path := range installerOverridePathCandidates() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func cleanupLegacyInstallerOverrideFiles() {
	for _, path := range installerOverridePathCandidates() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
	}
}

func installerConfigWriteCandidates(sourcePath string) []string {
	paths := make([]string, 0, len(platform.ConfigPathCandidates())+1)
	paths = append(paths, platform.ConfigPathCandidates()...)
	if strings.TrimSpace(sourcePath) != "" {
		paths = append(paths, sourcePath)
	}
	return lo.Uniq(paths)
}

func (s *Service) loadInstallerConfig() (InstallerConfig, string, error) {
	baseCfg, basePath, baseFound, baseErr := loadInstallerConfigFromCandidates(installerConfigPathCandidates(), s.normalizeP2PConfig)
	if baseErr != nil {
		return InstallerConfig{}, "", baseErr
	}

	overrideCfg, _, overrideFound, overrideErr := loadInstallerConfigFromCandidates(installerOverridePathCandidates(), s.normalizeP2PConfig)
	if overrideErr != nil {
		return InstallerConfig{}, "", overrideErr
	}

	if !baseFound && !overrideFound {
		return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
	}

	resolved := baseCfg
	resolvedPath := basePath

	if !baseFound {
		// ApiServer é o campo canônico; ApiScheme (json:"-") e ServerURL (legado) não são mais populados.
		if overrideCfg.ApiServer == "" && overrideCfg.ServerURL == "" {
			return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
		}
		resolved = overrideCfg
		resolvedPath = ""
	} else {
		resolved = mergeInstallerOverride(baseCfg, overrideCfg)
		// ApiServer é o campo canônico; ApiScheme (json:"-") e ServerURL (legado) não são mais populados.
		if resolved.ApiServer == "" && resolved.ServerURL == "" {
			return InstallerConfig{}, "", fmt.Errorf("config.json de producao nao encontrado")
		}
	}

	if overrideFound {
		if writePath, err := persistInstallerConfig(resolvedPath, resolved); err == nil {
			return resolved, writePath, nil
		}
	}

	return resolved, resolvedPath, nil
}

func persistInstallerConfig(sourcePath string, cfg InstallerConfig) (string, error) {
	paths := installerConfigWriteCandidates(sourcePath)
	if len(paths) == 0 {
		return "", fmt.Errorf("nenhum caminho disponivel para persistir config de producao")
	}

	CleanInstallerConfigForPersistence(&cfg)

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
		cleanupLegacyInstallerOverrideFiles()
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}

// CleanInstallerConfigForPersistence remove campos legados redundantes antes da serialização.
// serverUrl e apiScheme são deriváveis e não devem ser persistidos após o bootstrap.
func CleanInstallerConfigForPersistence(cfg *InstallerConfig) {
	cfg.ServerURL = ""
	cfg.ApiScheme = ""
}

func scrubInstallerConfigSource(path string, cfg InstallerConfig) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	CleanInstallerConfigForPersistence(&cfg)
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

func (s *Service) registerAgentFromDeployToken(ctx context.Context, scheme, server, deployToken string) (token, agentID, clientID, siteID, resolvedScheme string, err error) {
	hostname, _ := os.Hostname()
	macAddress := firstUsableMACAddress()

	version := s.version
	if version == "" {
		version = "dev"
	}

	payload := map[string]any{
		"cmd":          "CreateAgent",
		"name":         strings.TrimSpace(hostname),
		"macAddress":   macAddress,
		"departmentId": nil,
		"notes":        fmt.Sprintf("OS: %s | Agent: %s", runtime.GOOS, strings.TrimSpace(version)),
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("falha ao serializar request: %w", err)
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
		endpoint := candidateScheme + "://" + server + "/api/v1/agent-register"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(deployToken))

		resp, err := tlsutil.NewHTTPClient(30 * time.Second).Do(req)
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
			errs = append(errs, fmt.Sprintf("%s: HTTP %d: %s", endpoint, resp.StatusCode, netutil.SanitizeHTTPErrorBody(resp.StatusCode, string(body))))
			continue
		}

		t, aID, cID, sID, err := extractTokenAgentIDAndRouting(body)
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		return t, aID, cID, sID, candidateScheme, nil
	}

	return "", "", "", "", "", fmt.Errorf("nao foi possivel registrar agente: %s", strings.Join(errs, " | "))
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
	token, agentID, _, _, err := extractTokenAgentIDAndRouting(body)
	return token, agentID, err
}

func extractTokenAgentIDAndRouting(body []byte) (token, agentID, clientID, siteID string, err error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", "", "", fmt.Errorf("resposta JSON invalida: %w", err)
	}

	tryExtract := func(m map[string]any) (string, string, string, string) {
		t := firstNonEmptyAnyString(m["token"], m["authToken"], m["accessToken"])
		a := firstNonEmptyAnyString(m["agentId"], m["agentID"], m["id"])
		c := firstNonEmptyAnyString(m["clientId"], m["clientID"], m["client_id"])
		s := firstNonEmptyAnyString(m["siteId"], m["siteID"], m["site_id"])
		return strings.TrimSpace(t), strings.TrimSpace(a), strings.TrimSpace(c), strings.TrimSpace(s)
	}

	token, agentID, clientID, siteID = tryExtract(raw)
	if token != "" && agentID != "" {
		return token, agentID, clientID, siteID, nil
	}

	for _, key := range []string{"data", "result", "payload"} {
		nested, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		token, agentID, clientID, siteID = tryExtract(nested)
		if token != "" && agentID != "" {
			return token, agentID, clientID, siteID, nil
		}
	}

	return "", "", "", "", fmt.Errorf("resposta sem token/agentId")
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
