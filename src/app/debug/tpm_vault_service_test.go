package debug

// Testes do cofre TPM na camada de serviço (WS2): política herdável, gravação,
// apagamento, no-op com política nil e status nil-safe. Nenhum token completo
// pode aparecer em log/status.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/core/tpmvault"
)

type stubTPMVault struct {
	status  tpmvault.Status
	loadRec tpmvault.Record
	loadOK  bool
	loadErr error
	saved   []tpmvault.Record
	erased  int
	saveErr error
}

func (s *stubTPMVault) Status() tpmvault.Status { return s.status }
func (s *stubTPMVault) Load() (tpmvault.Record, bool, error) {
	return s.loadRec, s.loadOK, s.loadErr
}
func (s *stubTPMVault) Save(rec tpmvault.Record) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, rec)
	return nil
}
func (s *stubTPMVault) Erase() error { s.erased++; return nil }

func tpmService(t *testing.T, v tpmvault.Vault, writable bool) (*Service, string) {
	t.Helper()
	path := testConfigPath(t)
	svc := NewService(Options{
		ConfigPaths:         func() []string { return []string{path} },
		WatcherInterval:     time.Hour,
		HardenFileACL:       func(string) error { return nil },
		ValidateFileACL:     func(string) error { return nil },
		ValidateLoadFileACL: func(string) error { return nil },
		TPMVault:            v,
		TPMVaultWritable:    writable,
	})
	return svc, path
}

func tpmConfig(server, token string) Config {
	cfg := validConfig(server)
	cfg.AuthToken = token
	return cfg
}

const testTpmToken = "mdz_Abcdef1234567890"

func TestApplyTpmRecoveryRemoteNilIsNoop(t *testing.T) {
	stub := &stubTPMVault{}
	svc, _ := tpmService(t, stub, true)
	changed, err := svc.ApplyTpmRecoveryRemote(nil)
	if changed || err != nil {
		t.Fatalf("nil deve ser no-op, got changed=%v err=%v", changed, err)
	}
	if len(stub.saved) != 0 || stub.erased != 0 {
		t.Fatalf("nil não pode gravar/apagar o cofre")
	}
}

func TestApplyTpmRecoveryRemoteTrueSavesRecord(t *testing.T) {
	stub := &stubTPMVault{}
	svc, path := tpmService(t, stub, true)
	svc.config = tpmConfig("tpm.example.local", testTpmToken)

	enabled := true
	changed, err := svc.ApplyTpmRecoveryRemote(&enabled)
	if err != nil {
		t.Fatalf("ApplyTpmRecoveryRemote: %v", err)
	}
	if !changed {
		t.Fatal("mudança de política deve reportar changed=true")
	}
	if len(stub.saved) != 1 {
		t.Fatalf("esperava 1 gravação, got %d", len(stub.saved))
	}
	rec := stub.saved[0]
	if rec.Schema != tpmvault.Schema {
		t.Fatalf("schema inesperado: %q", rec.Schema)
	}
	if rec.Host() != "tpm.example.local" || rec.Token() != testTpmToken {
		t.Fatalf("registro inesperado: host=%q token=%q", rec.Host(), rec.Token())
	}
	if got := svc.GetConfig(); got.TpmRecoveryEnabled == nil || !*got.TpmRecoveryEnabled {
		t.Fatal("política efetiva em memória deveria ser true")
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if !strings.Contains(string(raw), `"tpmRecoveryEnabled": true`) {
		t.Fatalf("política não foi persistida: %s", string(raw))
	}
}

func TestApplyTpmRecoveryRemoteFalseErases(t *testing.T) {
	stub := &stubTPMVault{}
	svc, _ := tpmService(t, stub, true)
	on := true
	cfg := tpmConfig("tpm.example.local", testTpmToken)
	cfg.TpmRecoveryEnabled = &on
	svc.config = cfg

	off := false
	changed, err := svc.ApplyTpmRecoveryRemote(&off)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if stub.erased != 1 {
		t.Fatalf("esperava 1 apagamento, got %d", stub.erased)
	}
	if len(stub.saved) != 0 {
		t.Fatal("false não pode gravar")
	}
}

// Regressao: politica LOCAL desconhecida (nil — ex.: debug_config.json apagado
// pela formatacao) + servidor mandando false precisa APAGAR o cofre herdado.
// Antes, nil era comparado como false e a mudanca nao era detectada, deixando o
// registro vivo mesmo com a politica desligada no painel.
func TestApplyTpmRecoveryRemoteUnknownPolicyFalseErases(t *testing.T) {
	stub := &stubTPMVault{}
	svc, _ := tpmService(t, stub, true)
	cfg := tpmConfig("tpm.example.local", testTpmToken)
	cfg.TpmRecoveryEnabled = nil
	svc.config = cfg

	off := false
	changed, err := svc.ApplyTpmRecoveryRemote(&off)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if stub.erased != 1 {
		t.Fatalf("politica desconhecida + false deve apagar o cofre, erased=%d", stub.erased)
	}
	if len(stub.saved) != 0 {
		t.Fatal("false nao pode gravar")
	}
}

func TestApplyTpmRecoveryRemoteSameValueIsNoop(t *testing.T) {
	stub := &stubTPMVault{}
	svc, _ := tpmService(t, stub, true)
	on := true
	cfg := tpmConfig("tpm.example.local", testTpmToken)
	cfg.TpmRecoveryEnabled = &on
	svc.config = cfg

	changed, err := svc.ApplyTpmRecoveryRemote(&on)
	if changed || err != nil {
		t.Fatalf("valor igual deve ser no-op, got changed=%v err=%v", changed, err)
	}
	if len(stub.saved) != 0 || stub.erased != 0 {
		t.Fatal("valor igual não pode agir no cofre")
	}
}

func TestSyncVaultFromCurrentConfigNoops(t *testing.T) {
	cases := []struct {
		name     string
		policy   *bool
		cfg      Config
		writable bool
	}{
		{name: "política nil", policy: nil, cfg: tpmConfig("tpm.example.local", testTpmToken), writable: true},
		{name: "política false", policy: boolPtrFalse(), cfg: tpmConfig("tpm.example.local", testTpmToken), writable: true},
		{name: "não provisionado", policy: boolPtrTrue(), cfg: Config{ApiServer: "tpm.example.local", AuthToken: ""}, writable: true},
		{name: "fora do modo serviço", policy: boolPtrTrue(), cfg: tpmConfig("tpm.example.local", testTpmToken), writable: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubTPMVault{}
			svc, _ := tpmService(t, stub, tc.writable)
			cfg := tc.cfg
			cfg.TpmRecoveryEnabled = tc.policy
			svc.config = cfg
			if err := svc.SyncVaultFromCurrentConfig(); err != nil {
				t.Fatalf("esperava no-op, got %v", err)
			}
			if len(stub.saved) != 0 {
				t.Fatalf("esperava nenhuma gravação, got %d", len(stub.saved))
			}
		})
	}
}

func TestSyncVaultFromCurrentConfigNilVaultAndNilService(t *testing.T) {
	svc, _ := tpmService(t, nil, true)
	if err := svc.SyncVaultFromCurrentConfig(); err != nil {
		t.Fatalf("nil vault deve ser no-op, got %v", err)
	}
	var nilSvc *Service
	if err := nilSvc.SyncVaultFromCurrentConfig(); err != nil {
		t.Fatalf("nil service deve ser no-op, got %v", err)
	}
	if err := nilSvc.EraseTPMVault("x"); err != nil {
		t.Fatalf("nil service deve ser no-op, got %v", err)
	}
}

func TestEraseTPMVaultNilSafeAndWritableGate(t *testing.T) {
	svc, _ := tpmService(t, nil, true)
	if err := svc.EraseTPMVault("remote-decommission"); err != nil {
		t.Fatalf("nil vault deve ser no-op, got %v", err)
	}

	stub := &stubTPMVault{}
	readOnly, _ := tpmService(t, stub, false)
	if err := readOnly.EraseTPMVault("remote-decommission"); err != nil {
		t.Fatalf("no-op fora do serviço, got %v", err)
	}
	if stub.erased != 0 {
		t.Fatal("UI (somente leitura) não pode apagar o cofre")
	}
}

func TestGetTPMVaultStatusNilVaultExplains(t *testing.T) {
	svc, _ := tpmService(t, nil, true)
	st := svc.GetTPMVaultStatus()
	if st.Available || st.Present {
		t.Fatalf("sem cofre não pode estar disponível: %+v", st)
	}
	if strings.TrimSpace(st.LastError) == "" {
		t.Fatal("status sem cofre deve explicar o motivo em LastError")
	}

	on := true
	cfg := tpmConfig("tpm.example.local", testTpmToken)
	cfg.TpmRecoveryEnabled = &on
	svc.config = cfg
	if st := svc.GetTPMVaultStatus(); !st.Enabled {
		t.Fatal("status deve refletir a política efetiva")
	}
}

func TestSyncVaultErrorIsReturned(t *testing.T) {
	stub := &stubTPMVault{saveErr: errors.New("TPM_E_COMMAND_BLOCKED 0x80280400")}
	svc, _ := tpmService(t, stub, true)
	on := true
	cfg := tpmConfig("tpm.example.local", testTpmToken)
	cfg.TpmRecoveryEnabled = &on
	svc.config = cfg
	if err := svc.SyncVaultFromCurrentConfig(); err == nil {
		t.Fatal("falha do cofre deve ser devolvida ao chamador para log")
	}
}

func TestExtractAgentIdentityRouting(t *testing.T) {
	cases := []struct {
		name                         string
		body                         string
		wantAgent, wantClient, wantS string
		wantErr                      bool
	}{
		{
			name:      "canônico",
			body:      `{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1"}`,
			wantAgent: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", wantClient: "c1", wantS: "s1",
		},
		{
			name:      "aliases snake_case e id",
			body:      `{"id":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","client_id":"c2","site_id":"s2"}`,
			wantAgent: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", wantClient: "c2", wantS: "s2",
		},
		{
			name:      "wrapper data",
			body:      `{"data":{"agentID":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientID":"c3","siteID":"s3"}}`,
			wantAgent: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", wantClient: "c3", wantS: "s3",
		},
		{name: "sem agentId", body: `{"clientId":"c"}`, wantErr: true},
		{name: "json inválido", body: `{invalid`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, client, site, err := extractAgentIdentityRouting([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("esperava erro")
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if agent != tc.wantAgent || client != tc.wantClient || site != tc.wantS {
				t.Fatalf("got agent=%q client=%q site=%q", agent, client, site)
			}
		})
	}
}

func boolPtrTrue() *bool  { v := true; return &v }
func boolPtrFalse() *bool { v := false; return &v }

func TestTryBootstrapFromTPMVaultAllowInsecureTLS(t *testing.T) {
	tlsutil.SetConfigAllowInsecureTLS(false)
	t.Cleanup(func() { tlsutil.SetConfigAllowInsecureTLS(false) })
	origHarden := hardenInstallerACL
	hardenInstallerACL = func(string) error { return nil }
	t.Cleanup(func() { hardenInstallerACL = origHarden })

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent-auth/me" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1"}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "https://")
	val, err := tpmvault.FormatValue(testTpmToken, host, false)
	if err != nil {
		t.Fatalf("FormatValue: %v", err)
	}

	origDelay := tpmBootstrapRetryBaseDelay
	tpmBootstrapRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { tpmBootstrapRetryBaseDelay = origDelay })

	// AllowInsecureTLS=false deve falhar no certificado self-signed.
	stubFail := &stubTPMVault{
		loadOK:  true,
		loadRec: tpmvault.Record{Schema: tpmvault.Schema, APIURL: val, AllowInsecureTLS: false},
	}
	svcFail, _ := tpmService(t, stubFail, true)
	outPathFail := filepath.Join(t.TempDir(), "config.json")
	if svcFail.tryBootstrapFromTPMVault(context.Background(), InstallerConfig{}, outPathFail) {
		t.Fatal("esperava falha com AllowInsecureTLS=false em certificado self-signed")
	}

	// AllowInsecureTLS=true deve recuperar com sucesso mesmo com tlsutil global false.
	stubOK := &stubTPMVault{
		loadOK:  true,
		loadRec: tpmvault.Record{Schema: tpmvault.Schema, APIURL: val, AllowInsecureTLS: true},
	}
	svcOK, _ := tpmService(t, stubOK, true)
	svcOK.logf = func(m string) { t.Log(m) }
	outPathOK := filepath.Join(t.TempDir(), "config.json")
	if !svcOK.tryBootstrapFromTPMVault(context.Background(), InstallerConfig{}, outPathOK) {
		t.Fatal("esperava sucesso com AllowInsecureTLS=true em certificado self-signed")
	}
	if got := svcOK.GetConfig().AgentID; got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
		t.Fatalf("agentId recuperado inesperado: %q", got)
	}
}

func TestTryBootstrapFromTPMVaultRetriesTransientError(t *testing.T) {
	origDelay := tpmBootstrapRetryBaseDelay
	tpmBootstrapRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { tpmBootstrapRetryBaseDelay = origDelay })
	origHarden := hardenInstallerACL
	hardenInstallerACL = func(string) error { return nil }
	t.Cleanup(func() { hardenInstallerACL = origHarden })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1"}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	val, err := tpmvault.FormatValue(testTpmToken, host, true)
	if err != nil {
		t.Fatalf("FormatValue: %v", err)
	}
	stub := &stubTPMVault{
		loadOK:  true,
		loadRec: tpmvault.Record{Schema: tpmvault.Schema, APIURL: val},
	}
	svc, _ := tpmService(t, stub, true)
	svc.logf = func(m string) { t.Log(m) }
	outPath := filepath.Join(t.TempDir(), "config.json")
	if !svc.tryBootstrapFromTPMVault(context.Background(), InstallerConfig{}, outPath) {
		t.Fatal("esperava recuperação após retries de 503")
	}
	if calls.Load() != 3 {
		t.Fatalf("esperava 3 tentativas, got %d", calls.Load())
	}

	// 401 não pode fazer retry.
	var authCalls atomic.Int32
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv401.Close()

	host401 := strings.TrimPrefix(srv401.URL, "http://")
	val401, _ := tpmvault.FormatValue(testTpmToken, host401, true)
	stub401 := &stubTPMVault{
		loadOK:  true,
		loadRec: tpmvault.Record{Schema: tpmvault.Schema, APIURL: val401},
	}
	svc401, _ := tpmService(t, stub401, true)
	if svc401.tryBootstrapFromTPMVault(context.Background(), InstallerConfig{}, filepath.Join(t.TempDir(), "config.json")) {
		t.Fatal("401 não deve recuperar")
	}
	if authCalls.Load() != 1 {
		t.Fatalf("401 não deve fazer retry, got %d chamadas", authCalls.Load())
	}
}
