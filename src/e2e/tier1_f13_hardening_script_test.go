package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/automation"
	"discovery/app/core/selfupdate"
	"discovery/app/core/terminal"
	"discovery/app/debughttp"
)

func TestF13_T1_DebugHTTP_AuthCORSAndFirewallHardening(t *testing.T) {
	// 1. Regra de firewall do NSIS nao deve abrir UI debug_http em profile=any sem restricao
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	rePermissiveUIRule := regexp.MustCompile(`(?i)netsh\s+advfirewall\s+firewall\s+add\s+rule[^\r\n]*PRODUCT_EXECUTABLE[^\r\n]*profile=any`)
	if rePermissiveUIRule.MatchString(nsi) {
		t.Fatalf("F13 violacao: project.nsi registra regra de firewall inbound irrestrita (profile=any) para ${PRODUCT_EXECUTABLE}, expondo debug_http na rede")
	}

	// 2. resolveDebugCORSOrigin / debughttp nao deve retornar "*" quando BindAllInterfaces == true e deve exigir token auth + Host check
	var hasAuthGuard, hasWildcardCORS bool
	var filesChecked int
	for _, dir := range []string{filepath.Join(srcRoot(t), "app"), filepath.Join(srcRoot(t), "app", "debughttp")} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			base := filepath.Base(path)
			if !strings.Contains(base, "debug_http") && !strings.Contains(path, "debughttp") {
				return nil
			}
			filesChecked++
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			src := string(b)
			if strings.Contains(src, `return "*"`) && strings.Contains(src, "BindAllInterfaces") {
				hasWildcardCORS = true
			}
			if (strings.Contains(src, "Authorization") || strings.Contains(src, "X-Debug-Token")) &&
				(strings.Contains(src, "StatusUnauthorized") || strings.Contains(src, "401")) {
				hasAuthGuard = true
			}
			return nil
		})
	}
	if filesChecked == 0 {
		t.Fatalf("nenhum arquivo debug_http/debughttp encontrado")
	}
	if hasWildcardCORS {
		t.Fatalf("F13 violacao: debug_http ainda retorna Access-Control-Allow-Origin: * quando BindAllInterfaces=true")
	}
	if !hasAuthGuard {
		t.Fatalf("F13 violacao: debug_http/SSE nao exige autenticacao por token (401 StatusUnauthorized)")
	}

	// 3. Exercitar debughttp.ChatEventBroker ao vivo
	broker := debughttp.NewChatEventBroker()
	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)
	broker.Publish("ping", `{"ok":true}`)

	select {
	case msg := <-ch:
		if !strings.Contains(msg, `"event":"ping"`) {
			t.Fatalf("evento SSE inesperado: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout aguardando evento no ChatEventBroker")
	}
	if drained := broker.DrainPollBuffer(); len(drained) != 1 || !strings.Contains(drained[0], `"event":"ping"`) {
		t.Fatalf("DrainPollBuffer inesperado: %v", drained)
	}
}

func TestF13_T1_TerminalRunAs_UserParseAndFailClosed(t *testing.T) {
	if terminal.ParseRunAs("user") != terminal.RunAsUser {
		t.Fatalf("ParseRunAs(user) = %q", terminal.ParseRunAs("user"))
	}
	if terminal.ParseRunAs("system") != terminal.RunAsSystem {
		t.Fatalf("ParseRunAs(system) = %q", terminal.ParseRunAs("system"))
	}
	if terminal.ParseRunAs("") != terminal.RunAsInherit {
		t.Fatalf("ParseRunAs(\"\") = %q", terminal.ParseRunAs(""))
	}
	for _, valid := range []string{"", "inherit", "system", "user"} {
		if !terminal.ValidRunAs(valid) {
			t.Fatalf("ValidRunAs(%q) deveria ser true", valid)
		}
	}

	if eff := terminal.EffectiveRunAs(terminal.RunAsUser); eff == "system" {
		t.Fatalf("F13 violacao: EffectiveRunAs(RunAsUser) retornou %q (fail-open para SYSTEM)", eff)
	}

	// Verificar contrato fail-closed e enumeracao WTS (console + RDP) em runas_windows.go
	runasSrc := readSrcFile(t, filepath.Join("app", "core", "terminal", "runas_windows.go"))
	if strings.Contains(runasSrc, `effectiveRunAs: EffectiveRunAs(RunAsInherit)`) &&
		strings.Contains(runasSrc, `case RunAsUser:`) {
		t.Fatalf("F13 violacao: resolveRunAs(RunAsUser) ainda faz fallback fail-open para RunAsInherit/SYSTEM em runas_windows.go")
	}
	if !strings.Contains(runasSrc, "WTSEnumerateSessions") {
		t.Fatalf("F13 violacao: runas_windows.go nao enumera sessoes WTS (RDP + console) via WTSEnumerateSessions")
	}
}

func TestF13_T1_Automation_CustomFieldsPassedViaEnvNotInterpolated(t *testing.T) {
	pwnFile := filepath.Join(t.TempDir(), "pwn.txt")
	injectedPayload := `x" & echo PWNED > "` + pwnFile + `" & echo "`

	var noteBody string
	var resultSuccess atomic.Bool
	resultDone := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/automation/policy-sync"):
			scriptBody := "Write-Output \"ENV=$($env:MDZ_CUSTOM_FIELDS) LITERAL=[[campo:env_token]]\"\n"
			sum := sha256.Sum256([]byte(scriptBody))
			resp := automation.PolicySyncResponse{
				PolicyFingerprint: "fp-f13-t1",
				TaskCount:         1,
				Tasks: []automation.AutomationTask{
					{
						CommandID:             "cmd-f13-1",
						TaskID:                "task-f13-1",
						Name:                  "F13 CustomFields Env Test",
						ActionType:            automation.ActionRunScript,
						ScopeType:             automation.ApprovalScopeAgent,
						NotificationMode:      automation.NotificationModeSilent,
						WriteOutputToNote:     true,
						TriggerOnAgentCheckIn: true,
						Script: &automation.AutomationScript{
							ScriptID:          "script-f13-1",
							Name:              "env-check",
							Version:           "1",
							ScriptType:        automation.ScriptPowerShell,
							ContentHashSHA256: hex.EncodeToString(sum[:]),
							Content:           scriptBody,
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/custom-fields/runtime"):
			fields := []automation.RuntimeCustomField{
				{
					DefinitionID: "def-1",
					Name:         "env_token",
					ScopeType:    automation.FieldScopeAgent,
					ValueJson:    json.RawMessage(`"s3cr3t_val"`),
					IsSecret:     false,
				},
				{
					DefinitionID: "def-secret",
					Name:         "api_secret",
					ScopeType:    automation.FieldScopeAgent,
					ValueJson:    json.RawMessage(`"never_expose_secret"`),
					IsSecret:     true,
				},
				{
					DefinitionID: "def-2",
					Name:         "cmd_inject",
					ScopeType:    automation.FieldScopeAgent,
					ValueJson:    mustMarshalJSON(t, injectedPayload),
					IsSecret:     false,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fields)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if c, ok := body["content"].(string); ok {
				noteBody = c
			}
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/ack"):
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/result"):
			// A API usa envelope {"agentId","commandId","request":{...},
			// "correlationId"} no callback de execucao (contrato coberto por
			// AgentAutomationCallbackPayloadTests na API) — decodificar o
			// ResultRequest de DENTRO do envelope.
			var envelope struct {
				Request automation.ResultRequest `json:"request"`
			}
			_ = json.NewDecoder(r.Body).Decode(&envelope)
			resultSuccess.Store(envelope.Request.Success)
			select {
			case resultDone <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{
			BaseURL: srv.URL,
			Token:   "mdz_test_token_f13",
			AgentID: "11111111-1111-1111-1111-111111111111",
		}
	}, func(string) {})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := svc.RefreshPolicy(ctx, true); err != nil {
		t.Fatalf("RefreshPolicy: %v", err)
	}
	svc.TriggerAgentCheckInTasks(ctx)

	select {
	case <-resultDone:
	case <-ctx.Done():
		t.Fatalf("timeout aguardando resultado da task de automacao")
	}

	if !resultSuccess.Load() {
		t.Fatalf("task de automacao falhou (note=%q)", noteBody)
	}
	if _, err := os.Stat(pwnFile); err == nil {
		t.Fatalf("F13 violacao CRÍTICA: payload em custom field executou comando injetado e criou %s", pwnFile)
	}
	if strings.Contains(noteBody, "LITERAL=s3cr3t_val") {
		t.Fatalf("F13 violacao: token [[campo:env_token]] foi interpolado textualmente no corpo do script! output:\n%s", noteBody)
	}
	if !strings.Contains(noteBody, "LITERAL=[[campo:env_token]]") {
		t.Fatalf("esperava literal [[campo:env_token]] intacto no output, obteve:\n%s", noteBody)
	}
	if strings.Contains(noteBody, "never_expose_secret") {
		t.Fatalf("campo IsSecret=true vazou para MDZ_CUSTOM_FIELDS/output:\n%s", noteBody)
	}
	if !strings.Contains(noteBody, `"env_token":"s3cr3t_val"`) {
		t.Fatalf("esperava custom field entregue via $env:MDZ_CUSTOM_FIELDS JSON, obteve:\n%s", noteBody)
	}
}

func TestF13_T1_SelfUpdate_InstallFromURL_EnforcesIntegrityAndSignature(t *testing.T) {
	unsignedBinary := []byte("MZ-unsigned-dummy-installer-payload")
	tamperedBinary := []byte("MZ-tampered-payload")
	expectedHash := sha256Hex(unsignedBinary)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/external/unsigned.exe":
			_, _ = w.Write(unsignedBinary)
		case "/api/v1/download/agent":
			_, _ = w.Write(tamperedBinary)
		case "/api/v1/download/agent/sha256":
			_, _ = w.Write([]byte(expectedHash))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var launchCalls atomic.Int32
	tmpDir := t.TempDir()
	apiHost := strings.TrimPrefix(srv.URL, "http://")

	updater := &selfupdate.Updater{
		ApiScheme:   "http",
		ApiServer:   apiHost,
		GetToken:    func() string { return "mdz_test" },
		GetAgentID:  func() string { return "11111111-1111-1111-1111-111111111111" },
		TempDir:     tmpDir,
		ServiceMode: true,
		GetPolicy: func() selfupdate.Policy {
			p := selfupdate.DefaultPolicy()
			p.RequireSignatureValidation = true
			return p
		},
		OnSelfUpdateInstall: func(ctx context.Context, exePath string, targetVersion string) error {
			launchCalls.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. URL externa sem SHA-256 esperado nem assinatura Authenticode valida deve falhar fechado
	if err := updater.InstallFromURL(ctx, "2.0.0", srv.URL+"/external/unsigned.exe"); err == nil {
		t.Fatalf("F13 violacao: InstallFromURL aceitou binario externo nao assinado e sem verificacao SHA-256")
	}
	if launchCalls.Load() != 0 {
		t.Fatalf("OnSelfUpdateInstall nao deveria ter sido chamado para binario externo nao verificado")
	}

	// 2. Endpoint oficial com SHA-256 divergente deve falhar e remover arquivo temporario
	if err := updater.InstallFromURL(ctx, "2.0.0", srv.URL+"/api/v1/download/agent"); err == nil {
		t.Fatalf("InstallFromURL deveria falhar quando /sha256 diverge do arquivo baixado")
	}
	if launchCalls.Load() != 0 {
		t.Fatalf("OnSelfUpdateInstall nao deveria ter sido chamado com hash divergente")
	}
	if entries, _ := os.ReadDir(tmpDir); len(entries) != 0 {
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".exe") {
				t.Fatalf("arquivo .exe adulterado nao foi removido de TempDir: %s", e.Name())
			}
		}
	}
}

func TestF13_T1_LinuxBuildScript_ValidToolingVersionGOBINAndService(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")
	reWailsVer := regexp.MustCompile(`github\.com/wailsapp/wails/v3\s+(v[^\s]+)`)
	m := reWailsVer.FindStringSubmatch(gomod)
	if len(m) < 2 {
		t.Fatalf("versao wails/v3 nao encontrada em go.mod")
	}
	wantWailsVer := m[1]

	script := readRepoFile(t, filepath.Join("build", "server-api", "linux", "build-agent-server-linux.sh"))

	if !strings.Contains(script, "wails3@"+wantWailsVer) {
		t.Fatalf("F13 violacao: build-agent-server-linux.sh nao instala wails3@%s (encontrado v3.0.0-beta.23 ou divergente)", wantWailsVer)
	}
	if strings.Contains(script, "binutils-mingw-w64-x-64") {
		t.Fatalf("F13 violacao: build-agent-server-linux.sh contem typo 'binutils-mingw-w64-x-64' (deveria ser binutils-mingw-w64-x86-64)")
	}
	if strings.Contains(script, `WAILS3_BIN="$(go env GOBIN 2>/dev/null)"`) &&
		!strings.Contains(script, `${WAILS3_BIN}/wails3`) && !strings.Contains(script, `$GOBIN/wails3`) && !strings.Contains(script, `/wails3"`) {
		t.Fatalf("F13 violacao: WAILS3_BIN aponta para diretorio GOBIN sem /wails3")
	}
	if strings.Contains(script, `[\"\\']`) {
		t.Fatalf("F13 violacao: build-agent-server-linux.sh contem escaping quebrado de regex Python dentro de string bash")
	}
	if !strings.Contains(script, "./cmd/discovery-service") || !strings.Contains(script, "discovery-service.exe") {
		t.Fatalf("F13 violacao: build-agent-server-linux.sh compila apenas discovery-agent.exe e omite ./cmd/discovery-service (discovery-service.exe)")
	}
}

func mustMarshalJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return json.RawMessage(b)
}
