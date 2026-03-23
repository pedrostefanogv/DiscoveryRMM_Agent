package debug

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"
)

func installerConfigPathCandidates() []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		// 1o: C:\ProgramData\Discovery (compartilhado entre usuarios)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2o: LOCALAPPDATA\Discovery (fallback compatibilidade)
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
		// 1o: C:\ProgramData\Discovery (preferencia - compartilhado entre usuarios)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			paths = append(paths, filepath.Join(programData, "Discovery", "config.json"))
		}
		// 2o: LOCALAPPDATA\Discovery (fallback compatibilidade)
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

func (s *Service) loadInstallerConfig() (InstallerConfig, string, error) {
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
		if s.normalizeP2PConfig != nil {
			cfg.P2P = s.normalizeP2PConfig(cfg.P2P)
		}

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

func (s *Service) registerAgentFromDeployToken(ctx context.Context, scheme, server, deployToken string) (string, string, string, error) {
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

	version := s.version
	if version == "" {
		version = "dev"
	}

	payload := registerRequest{
		Hostname:        strings.TrimSpace(hostname),
		DisplayName:     strings.TrimSpace(displayName),
		OperatingSystem: osName,
		OSVersion:       osVersion,
		AgentVersion:    strings.TrimSpace(version),
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
