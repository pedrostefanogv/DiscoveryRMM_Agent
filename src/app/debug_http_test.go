//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	debugsvc "discovery/app/debug"
	"discovery/app/debughttp"
	"discovery/app/logs"
)

func newTestDebugHTTPApp(t *testing.T, bindAll bool) (*App, *debughttp.Server) {
	t.Helper()
	a := &App{
		ctx:        context.Background(),
		chatEvents: debughttp.NewChatEventBroker(),
	}
	a.Logs.Buffer = logs.New()
	tok := a.ensureDebugAuthToken()
	srv := debughttp.NewServer(&http.Server{}, nil, 18080, bindAll, tok)
	a.debugHTTP = srv
	return a, srv
}

func TestServeDebugAPI_RejectsMissingOrWrongToken(t *testing.T) {
	a, srv := newTestDebugHTTPApp(t, false)

	for _, tc := range []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{"missing_token", "", "", http.StatusUnauthorized},
		{"wrong_token", "Authorization", "Bearer wrong-token", http.StatusUnauthorized},
		{"prefix_only", "Authorization", "Bearer " + srv.AuthToken[:8], http.StatusUnauthorized},
		{"valid_bearer", "Authorization", "Bearer " + srv.AuthToken, http.StatusOK},
		{"valid_header", "X-Discovery-Debug-Token", srv.AuthToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/GetDebugHTTPPort", nil)
			req.Host = "127.0.0.1:18080"
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rec := httptest.NewRecorder()
			a.serveDebugAPI(rec, req, srv)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestServeDebugAPI_RejectsInvalidHostHeader(t *testing.T) {
	a, srv := newTestDebugHTTPApp(t, false)

	for _, tc := range []struct {
		host string
		want int
	}{
		{"evil.com:18080", http.StatusForbidden},
		{"127.0.0.1:9999", http.StatusForbidden},
		{"127.0.0.1", http.StatusForbidden},
		{"127.0.0.1:18080", http.StatusOK},
		{"localhost:18080", http.StatusOK},
		{"[::1]:18080", http.StatusOK},
	} {
		t.Run(tc.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/GetDebugHTTPPort", nil)
			req.Host = tc.host
			req.Header.Set("Authorization", "Bearer "+srv.AuthToken)
			rec := httptest.NewRecorder()
			a.serveDebugAPI(rec, req, srv)
			if rec.Code != tc.want {
				t.Fatalf("host %q status = %d, want %d", tc.host, rec.Code, tc.want)
			}
		})
	}
}

func TestCORS_NoWildcardAndNoNullOrigin(t *testing.T) {
	a, srv := newTestDebugHTTPApp(t, true)

	for _, origin := range []string{"null", "NULL", "https://evil.com", "http://127.0.0.1:9999"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/GetDebugHTTPPort", nil)
		req.Host = "127.0.0.1:18080"
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", "Bearer "+srv.AuthToken)
		rec := httptest.NewRecorder()
		a.serveDebugAPI(rec, req, srv)

		acao := rec.Header().Get("Access-Control-Allow-Origin")
		if acao == "*" || strings.EqualFold(acao, "null") || acao == "https://evil.com" {
			t.Fatalf("unsafe Access-Control-Allow-Origin=%q for Origin=%q", acao, origin)
		}
	}

	// Same-port loopback origin is allowed
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/GetDebugHTTPPort", nil)
	req.Host = "127.0.0.1:18080"
	req.Header.Set("Origin", "http://127.0.0.1:18080")
	req.Header.Set("Authorization", "Bearer "+srv.AuthToken)
	rec := httptest.NewRecorder()
	a.serveDebugAPI(rec, req, srv)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:18080" {
		t.Fatalf("expected loopback origin echoed, got %q", got)
	}
}

func TestEnsureChatSSEServer_RequiresTokenAndValidHost(t *testing.T) {
	a := &App{
		ctx:        context.Background(),
		chatEvents: debughttp.NewChatEventBroker(),
	}
	a.Logs.Buffer = logs.New()
	if err := a.EnsureChatSSEServer(); err != nil {
		t.Fatalf("EnsureChatSSEServer: %v", err)
	}
	t.Cleanup(a.StopChatSSEServer)

	info := a.GetChatSSEInfo()
	if info.Port <= 0 || info.Token == "" || !strings.Contains(info.URL, "/api/chat-events") {
		t.Fatalf("unexpected GetChatSSEInfo: %+v", info)
	}

	// 1. Unauthenticated request -> 401
	resp, err := http.Get(info.URL)
	if err != nil {
		t.Fatalf("http.Get unauth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SSE status = %d, want 401", resp.StatusCode)
	}

	// 2. Bad Host header -> 403
	reqBadHost, _ := http.NewRequest(http.MethodGet, info.URL+"?token="+info.Token, nil)
	reqBadHost.Host = fmt.Sprintf("evil.example:%d", info.Port)
	respHost, err := (&http.Client{}).Do(reqBadHost)
	if err != nil {
		t.Fatalf("Do bad host: %v", err)
	}
	respHost.Body.Close()
	if respHost.StatusCode != http.StatusForbidden {
		t.Fatalf("bad Host SSE status = %d, want 403", respHost.StatusCode)
	}

	// 3. Valid token via query param + Origin: null must NOT echo null
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reqValid, _ := http.NewRequestWithContext(ctx, http.MethodGet, info.URL+"?token="+info.Token, nil)
	reqValid.Header.Set("Origin", "null")
	respOK, err := (&http.Client{}).Do(reqValid)
	if err != nil {
		t.Fatalf("Do valid SSE: %v", err)
	}
	defer respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("valid SSE status = %d, want 200", respOK.StatusCode)
	}
	if acao := respOK.Header.Get("Access-Control-Allow-Origin"); acao == "*" || strings.EqualFold(acao, "null") {
		t.Fatalf("SSE echoed unsafe ACAO=%q for Origin=null", acao)
	}
	buf := make([]byte, 128)
	n, _ := io.ReadAtLeast(respOK.Body, buf, 10)
	if !strings.Contains(string(buf[:n]), "chat:connected") {
		t.Fatalf("expected chat:connected event, got %q", string(buf[:n]))
	}
}

func TestServeDebugAPI_BlocksTokenDisclosureMethods(t *testing.T) {
	a, srv := newTestDebugHTTPApp(t, false)

	for _, method := range []string{"GetChatSSEInfo", "GetDebugConfig", "GetDebugHTTPURL"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/"+method, nil)
		req.Host = "127.0.0.1:18080"
		req.Header.Set("Authorization", "Bearer "+srv.AuthToken)
		rec := httptest.NewRecorder()
		a.serveDebugAPI(rec, req, srv)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for blocked method %s, got %d", method, rec.Code)
		}
	}

	// Also verify blocked methods are absent from /api/ method list
	reqList := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/", nil)
	reqList.Host = "127.0.0.1:18080"
	reqList.Header.Set("Authorization", "Bearer "+srv.AuthToken)
	recList := httptest.NewRecorder()
	a.serveDebugAPI(recList, reqList, srv)
	if recList.Code != http.StatusOK {
		t.Fatalf("/api/ list status = %d", recList.Code)
	}
	var body struct {
		Methods []string `json:"methods"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, m := range body.Methods {
		if m == "GetChatSSEInfo" || m == "GetDebugConfig" || m == "GetDebugHTTPURL" {
			t.Fatalf("blocked method %s exposed in /api/ list", m)
		}
	}
}

func TestGetDebugConfig_RedactsAuthTokenAndBlocksHTTPReflection(t *testing.T) {
	a, srv := newTestDebugHTTPApp(t, false)
	cfgPath := t.TempDir() + `\debug_config.json`
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{
		ConfigPaths:     func() []string { return []string{cfgPath} },
		WatcherInterval: time.Hour,
		HardenFileACL:   func(string) error { return nil },
	})
	const secretToken = "mdz_secret_debug_token_do_not_leak"
	if err := a.DebugSvc.SetConfig(DebugConfig{
		ApiScheme: "https",
		ApiServer: "api.example.local",
		AuthToken: secretToken,
		AgentID:   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	}); err != nil {
		t.Fatalf("seed SetConfig: %v", err)
	}

	// 1. Exported GetDebugConfig() must always redact AuthToken/AuthTokenDpapi and set HasAuthToken.
	publicCfg := a.GetDebugConfig()
	if publicCfg.AuthToken != "" || publicCfg.AuthTokenDpapi != "" {
		t.Fatalf("App.GetDebugConfig() leaked secret fields: %+v", publicCfg)
	}
	if !publicCfg.HasAuthToken {
		t.Fatalf("App.GetDebugConfig() expected HasAuthToken=true, got %+v", publicCfg)
	}
	if publicCfg.ApiServer != "api.example.local" {
		t.Fatalf("App.GetDebugConfig() ApiServer = %q, want api.example.local", publicCfg.ApiServer)
	}

	// 2. Internal getter retains the live token for core Go services.
	internalCfg := a.getDebugConfigInternal()
	if internalCfg.AuthToken != secretToken {
		t.Fatalf("getDebugConfigInternal() AuthToken = %q, want %q", internalCfg.AuthToken, secretToken)
	}

	// 3. HTTP reflection /api/GetDebugConfig must be blocked with 403 Forbidden.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/GetDebugConfig", nil)
	req.Host = "127.0.0.1:18080"
	req.Header.Set("Authorization", "Bearer "+srv.AuthToken)
	rec := httptest.NewRecorder()
	a.serveDebugAPI(rec, req, srv)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/GetDebugConfig status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretToken) {
		t.Fatalf("/api/GetDebugConfig body leaked secret token")
	}

	// 4. Saving other fields via SetDebugConfig with blank authToken preserves existing token.
	if err := a.SetDebugConfig(DebugConfig{
		ApiScheme: "https",
		ApiServer: "api2.example.local",
		AuthToken: "",
		AgentID:   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	}); err != nil {
		t.Fatalf("SetDebugConfig with blank authToken failed: %v", err)
	}
	if got := a.getDebugConfigInternal().AuthToken; got != secretToken {
		t.Fatalf("SetDebugConfig wiped AuthToken: got %q, want %q", got, secretToken)
	}
	if got := a.GetDebugConfig(); got.AuthToken != "" || !got.HasAuthToken || got.ApiServer != "api2.example.local" {
		t.Fatalf("GetDebugConfig after update unexpected: %+v", got)
	}
}
