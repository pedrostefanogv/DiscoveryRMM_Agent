package debug

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"discovery/internal/agentconn"
	"discovery/internal/tlsutil"
)

type fakeAgentConn struct {
	reloadCount int
}

func (f *fakeAgentConn) Reload() {
	f.reloadCount++
}

func (f *fakeAgentConn) GetStatus() agentconn.Status {
	return agentconn.Status{}
}

func TestInstallerConfigUnmarshalAutoProvisioningCompat(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "canonical bool true", raw: `{"autoProvisioning":true}`, want: true},
		{name: "canonical bool false", raw: `{"autoProvisioning":false}`, want: false},
		{name: "canonical number 1", raw: `{"autoProvisioning":1}`, want: true},
		{name: "canonical string yes", raw: `{"autoProvisioning":"yes"}`, want: true},
		{name: "legacy bool true", raw: `{"discoveryEnabled":true}`, want: true},
		{name: "legacy bool false", raw: `{"discoveryEnabled":false}`, want: false},
		{name: "legacy number 1", raw: `{"discoveryEnabled":1}`, want: true},
		{name: "legacy number 0", raw: `{"discoveryEnabled":0}`, want: false},
		{name: "legacy string true", raw: `{"discoveryEnabled":"true"}`, want: true},
		{name: "legacy string false", raw: `{"discoveryEnabled":"false"}`, want: false},
		{name: "legacy string yes", raw: `{"discoveryEnabled":"yes"}`, want: true},
		{name: "legacy string no", raw: `{"discoveryEnabled":"no"}`, want: false},
		{name: "legacy string 1", raw: `{"discoveryEnabled":"1"}`, want: true},
		{name: "legacy string 0", raw: `{"discoveryEnabled":"0"}`, want: false},
		{name: "canonical wins over legacy", raw: `{"autoProvisioning":false,"discoveryEnabled":true}`, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg InstallerConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatalf("unmarshal compat: %v", err)
			}
			if cfg.AutoProvisioning == nil {
				t.Fatal("autoProvisioning nao deveria ser nil")
			}
			if *cfg.AutoProvisioning != tc.want {
				t.Fatalf("autoProvisioning = %v, want %v", *cfg.AutoProvisioning, tc.want)
			}
		})
	}
}

func TestInstallerConfigUnmarshalAutoProvisioningInvalid(t *testing.T) {
	var cfg InstallerConfig
	if err := json.Unmarshal([]byte(`{"autoProvisioning":2}`), &cfg); err == nil {
		t.Fatal("esperava erro para autoProvisioning invalido")
	}
	if err := json.Unmarshal([]byte(`{"discoveryEnabled":2}`), &cfg); err == nil {
		t.Fatal("esperava erro para discoveryEnabled invalido")
	}
}

func TestInstallerConfigUnmarshalAllowInsecureTLSCompat(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "bool true", raw: `{"allowInsecureTls":true}`, want: true},
		{name: "bool false", raw: `{"allowInsecureTls":false}`, want: false},
		{name: "number 1", raw: `{"allowInsecureTls":1}`, want: true},
		{name: "number 0", raw: `{"allowInsecureTls":0}`, want: false},
		{name: "string yes", raw: `{"allowInsecureTls":"yes"}`, want: true},
		{name: "string no", raw: `{"allowInsecureTls":"no"}`, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg InstallerConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatalf("unmarshal compat: %v", err)
			}
			if cfg.AllowInsecureTLS == nil {
				t.Fatal("allowInsecureTls nao deveria ser nil")
			}
			if *cfg.AllowInsecureTLS != tc.want {
				t.Fatalf("allowInsecureTls = %v, want %v", *cfg.AllowInsecureTLS, tc.want)
			}
		})
	}
}

func TestInstallerConfigUnmarshalDeployTokenCompat(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "deployToken atual", raw: `{"deployToken":"token-atual"}`, want: "token-atual"},
		{name: "apiKey legado", raw: `{"apiKey":"token-legado"}`, want: "token-legado"},
		{name: "deployToken prevalece", raw: `{"deployToken":"token-atual","apiKey":"token-legado"}`, want: "token-atual"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg InstallerConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatalf("unmarshal compat: %v", err)
			}
			if cfg.APIKey != tc.want {
				t.Fatalf("APIKey = %q, want %q", cfg.APIKey, tc.want)
			}
		})
	}
}

func TestInstallerConfigMarshalUsesDeployTokenField(t *testing.T) {
	data, err := json.Marshal(InstallerConfig{ServerURL: "https://srv/api/", APIKey: "deploy-token"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	jsonText := string(data)
	if !strings.Contains(jsonText, `"deployToken":"deploy-token"`) {
		t.Fatalf("json deveria conter deployToken: %s", jsonText)
	}
	if strings.Contains(jsonText, `"apiKey"`) {
		t.Fatalf("json nao deveria conter apiKey legado: %s", jsonText)
	}
}

func TestLoadInstallerConfigFromCandidates_StripsUTF8BOM(t *testing.T) {
	tempDir := t.TempDir()
	path := tempDir + string(os.PathSeparator) + "config.json"
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"serverUrl":"https://srv/api/","deployToken":"token-bom"}`)...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, resolvedPath, found, err := loadInstallerConfigFromCandidates([]string{path}, nil)
	if err != nil {
		t.Fatalf("loadInstallerConfigFromCandidates: %v", err)
	}
	if !found {
		t.Fatal("esperava found=true")
	}
	if resolvedPath != path {
		t.Fatalf("resolvedPath = %q, want %q", resolvedPath, path)
	}
	if cfg.ServerURL != "https://srv/api/" {
		t.Fatalf("ServerURL = %q", cfg.ServerURL)
	}
	if cfg.APIKey != "token-bom" {
		t.Fatalf("APIKey = %q, want %q", cfg.APIKey, "token-bom")
	}
}

func TestParseInstallerServerURL_PreservesExplicitPort(t *testing.T) {
	scheme, server, err := parseInstallerServerURL("https://api.example.local:5001/base/")
	if err != nil {
		t.Fatalf("parseInstallerServerURL: %v", err)
	}
	if scheme != "https" {
		t.Fatalf("scheme = %q, want https", scheme)
	}
	if server != "api.example.local:5001" {
		t.Fatalf("server = %q, want api.example.local:5001", server)
	}
}

func TestLoadPersistedConfig_StripsUTF8BOM(t *testing.T) {
	oldReadFile := osReadFile
	oldExecutable := osExecutable
	oldUserHomeDir := osUserHomeDir
	oldAllowInsecureTLS := tlsutil.ConfigAllowInsecureTLS()
	defer func() {
		osReadFile = oldReadFile
		osExecutable = oldExecutable
		osUserHomeDir = oldUserHomeDir
		tlsutil.SetConfigAllowInsecureTLS(oldAllowInsecureTLS)
	}()

	osReadFile = func(string) ([]byte, error) {
		return append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"scheme":"https","server":"api.example.local","allowInsecureTls":true}`)...), nil
	}
	osExecutable = func() (string, error) { return "", errors.New("sem executavel") }
	osUserHomeDir = func() (string, error) { return "", errors.New("sem home") }

	svc := NewService(Options{})
	svc.LoadPersistedConfig()

	cfg := svc.GetConfig()
	if cfg.Scheme != "https" {
		t.Fatalf("Scheme = %q", cfg.Scheme)
	}
	if cfg.Server != "api.example.local" {
		t.Fatalf("Server = %q", cfg.Server)
	}
	if !cfg.AllowInsecureTLS {
		t.Fatal("AllowInsecureTLS deveria ser true")
	}
	if !tlsutil.AllowInsecureTLS() {
		t.Fatal("AllowInsecureTLS global deveria ser true")
	}
}

func TestGetRealtimeStatus_SetsAgentAuthHeadersAndAgentID(t *testing.T) {
	const (
		token   = "token-123"
		agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/agent-auth/me/realtime/status" {
			t.Fatalf("path = %q, want %q", got, "/api/v1/agent-auth/me/realtime/status")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer "+token)
		}
		if got := r.Header.Get("X-Agent-Token"); got != token {
			t.Fatalf("X-Agent-Token = %q, want %q", got, token)
		}
		if got := r.Header.Get("X-Agent-ID"); got != agentID {
			t.Fatalf("X-Agent-ID = %q, want %q", got, agentID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"natsConnected":true,"realtimeConnectedAgents":7,"checkedAtUtc":"2026-03-23T12:00:00Z"}`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	svc := NewService(Options{})
	svc.ApplyRuntimeConnectionConfig("http", u.Host, token, agentID, "", "")

	status, err := svc.GetRealtimeStatus()
	if err != nil {
		t.Fatalf("GetRealtimeStatus: %v", err)
	}
	if !status.NATSConnected {
		t.Fatal("NATSConnected deveria ser true")
	}
	if status.RealtimeConnectedAgents != 7 {
		t.Fatalf("RealtimeConnectedAgents = %d, want 7", status.RealtimeConnectedAgents)
	}
}

func TestApplyRuntimeConnectionConfig_DerivesNativeNATSServerFromAPIServer(t *testing.T) {
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	svc := NewService(Options{})
	svc.ApplyRuntimeConnectionConfig("https", "tngplacas.com.br", "token-123", agentID, "", "")

	cfg := svc.GetConfig()
	if cfg.NatsServer != "nats://tngplacas.com.br:4222" {
		t.Fatalf("NatsServer = %q", cfg.NatsServer)
	}
	if cfg.Scheme != "nats" {
		t.Fatalf("Scheme = %q", cfg.Scheme)
	}
	if cfg.Server != "nats://tngplacas.com.br:4222" {
		t.Fatalf("Server = %q", cfg.Server)
	}
}

func TestApplyRemoteConnectionSecurity_UpdatesConfigAndReloads(t *testing.T) {
	oldWriteFile := osWriteFile
	oldMkdirAll := osMkdirAll
	oldExecutable := osExecutable
	oldUserHomeDir := osUserHomeDir
	defer func() {
		osWriteFile = oldWriteFile
		osMkdirAll = oldMkdirAll
		osExecutable = oldExecutable
		osUserHomeDir = oldUserHomeDir
	}()

	osWriteFile = func(string, []byte, os.FileMode) error { return nil }
	osMkdirAll = func(string, os.FileMode) error { return nil }
	osExecutable = func() (string, error) { return "", errors.New("sem executavel") }
	osUserHomeDir = func() (string, error) { return "", errors.New("sem home") }

	agentConn := &fakeAgentConn{}
	svc := NewService(Options{AgentConn: agentConn})
	svc.ApplyRuntimeConnectionConfig("https", "api.example.local", "tok", "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")

	enforce := true
	natsWssExternal := true
	handshake := true
	changed, err := svc.ApplyRemoteConnectionSecurity("nats.example.local", &natsWssExternal, &enforce, &handshake, "aa:bb", "11 22")
	if err != nil {
		t.Fatalf("ApplyRemoteConnectionSecurity retornou erro: %v", err)
	}
	if !changed {
		t.Fatal("esperava changed=true")
	}

	cfg := svc.GetConfig()
	if cfg.NatsServerHost != "nats.example.local" {
		t.Fatalf("NatsServerHost = %q", cfg.NatsServerHost)
	}
	if !cfg.NatsUseWssExternal {
		t.Fatal("NatsUseWssExternal deveria ser true")
	}
	if !cfg.EnforceTlsHashValidation {
		t.Fatal("EnforceTlsHashValidation deveria ser true")
	}
	if cfg.ApiTlsCertHash != "AA:BB" {
		t.Fatalf("ApiTlsCertHash = %q", cfg.ApiTlsCertHash)
	}
	if cfg.NatsTlsCertHash != "11 22" {
		t.Fatalf("NatsTlsCertHash = %q", cfg.NatsTlsCertHash)
	}
	if agentConn.reloadCount != 1 {
		t.Fatalf("reloadCount = %d, want 1", agentConn.reloadCount)
	}
}
