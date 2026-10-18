package debug

// Testes de regressão P2/P7: escrita atômica + lock, DACL sempre endurecida
// (coberta no pacote platform) e watcher de alteração externa.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), debugConfigFile)
}

func serviceForPath(path string, interval time.Duration) *Service {
	return NewService(Options{
		ConfigPaths:     func() []string { return []string{path} },
		WatcherInterval: interval,
		// Testes não aplicam DACL real: os arquivos ficam em t.TempDir e o
		// process de teste (não-SYSTEM) não teria DELETE após o harden.
		HardenFileACL: func(string) error { return nil },
	})
}

const testAgentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

func validConfig(server string) Config {
	cfg := Config{
		ApiScheme: "https",
		ApiServer: server,
		AuthToken: "token-123",
		AgentID:   testAgentID,
		Scheme:    "https",
		Server:    server,
	}
	cfg.normalizeApiScheme()
	return cfg
}

func TestPersistConfigUsesAtomicWriteAndKeepsOldFileOnRenameFailure(t *testing.T) {
	path := testConfigPath(t)
	oldContent := []byte("{\"apiServer\":\"old.example.local\"}\n")
	if err := os.WriteFile(path, oldContent, 0o600); err != nil {
		t.Fatalf("preparar arquivo: %v", err)
	}

	oldRename := osRename
	t.Cleanup(func() { osRename = oldRename })
	osRename = func(string, string) error { return errors.New("rename falhou (simulado)") }

	svc := serviceForPath(path, time.Hour)
	err := svc.PersistConfig(validConfig("new.example.local"))
	if err == nil {
		t.Fatal("esperava erro quando o rename atômico falha")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("arquivo antigo deveria permanecer: %v", readErr)
	}
	if string(got) != string(oldContent) {
		t.Fatalf("arquivo original foi alterado em caso de erro: %q", string(got))
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporário vazou: %s", entry.Name())
		}
	}
}

func TestPersistConfigConcurrentWritesStayValid(t *testing.T) {
	path := testConfigPath(t)
	svc := serviceForPath(path, time.Hour)

	const writes = 10
	var wg sync.WaitGroup
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := validConfig(fmt.Sprintf("api-%d.example.local", i))
			if err := svc.PersistConfig(cfg); err != nil {
				t.Errorf("PersistConfig concorrente %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler config final: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config final corrompida: %v — conteúdo: %q", err, string(data))
	}
	if !strings.HasPrefix(cfg.ApiServer, "api-") || !strings.HasSuffix(cfg.ApiServer, ".example.local") {
		t.Fatalf("ApiServer final inesperado: %q", cfg.ApiServer)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporário vazou após writes concorrentes: %s", entry.Name())
		}
	}
}

func TestLoadPersistedConfigKeepsLegacyPlainJSON(t *testing.T) {
	path := testConfigPath(t)
	// JSON plano legado, sem BOM e sem apiInsecure.
	legacy := "{\"apiScheme\":\"http\",\"apiServer\":\"legacy.example.local\",\"authToken\":\"tok-legado\",\"agentId\":\"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7\",\"scheme\":\"https\",\"natsServer\":\"nats://legacy.example.local:4222\"}"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("preparar legado: %v", err)
	}

	svc := serviceForPath(path, time.Hour)
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	cfg := svc.GetConfig()
	if cfg.ApiServer != "legacy.example.local" || cfg.AuthToken != "tok-legado" {
		t.Fatalf("config legada não carregada: %+v", cfg)
	}
	// Compatibilidade: o JSON plano legado é lido normalmente; a normalização
	// de scheme continua a mesma do loader anterior (ApiInsecure é a fonte).
	if cfg.NatsServer != "nats://legacy.example.local:4222" {
		t.Fatalf("NatsServer legado não preservado: %+v", cfg)
	}
}

func TestConfigWatcherAppliesExternalChangeWithin3sAndKeepsLastValidOnInvalidJSON(t *testing.T) {
	path := testConfigPath(t)
	initial := validConfig("initial.example.local")
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("preparar config inicial: %v", err)
	}

	applied := make(chan Config, 4)
	svc := serviceForPath(path, 25*time.Millisecond)
	svc.SetExternalConfigApplyHook(func(cfg Config) { applied <- cfg })
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	// Alteração externa válida: deve ser adotada em <=3s (polling curto).
	external := validConfig("external.example.local")
	external.AuthToken = "token-externo"
	data, _ = json.Marshal(external)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("gravar alteração externa: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.GetConfig().ApiServer == "external.example.local" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := svc.GetConfig(); got.ApiServer != "external.example.local" || got.AuthToken != "token-externo" {
		t.Fatalf("watcher não aplicou alteração externa em <=3s: %+v", got)
	}
	select {
	case <-applied:
	default:
		t.Fatal("hook de reaplicação não foi chamado para alteração externa válida")
	}

	// JSON externo inválido: mantém o último válido em memória.
	if err := os.WriteFile(path, []byte("{ isto nao e json"), 0o600); err != nil {
		t.Fatalf("gravar JSON inválido: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := svc.GetConfig(); got.ApiServer != "external.example.local" {
		t.Fatalf("JSON inválido externo sobrescreveu a config em memória: %+v", got)
	}
}

func TestSetConfigPersistsAndAppliesPartialFields(t *testing.T) {
	path := testConfigPath(t)
	svc := serviceForPath(path, time.Hour)

	if err := svc.SetConfig(validConfig("api.example.local")); err != nil {
		t.Fatalf("SetConfig inicial: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler config: %v", err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("config persistida inválida: %v", err)
	}
	if persisted.AuthToken != "token-123" || persisted.AgentID != testAgentID {
		t.Fatalf("campos não foram persistidos: %+v", persisted)
	}
	if svc.GetConfig().NatsServer == "" {
		t.Fatal("NatsServer deveria ser derivado do ApiServer")
	}
}

// TestLoadPersistedConfigHardensACLOfExistingFile cobre A.1.5: a DACL
// restritiva (SYSTEM:F + Users:R) precisa valer também para o arquivo que JÁ
// existia no disco. Antes ela era aplicada apenas no temporário de cada
// gravação nova — um arquivo herdado de uma instalação anterior (ou criado pelo
// instalador) permanecia com a ACL do diretório até o próximo save do serviço.
// Falha do icacls é best-effort: só loga, o carregamento continua.
func TestLoadPersistedConfigHardensACLOfExistingFile(t *testing.T) {
	path := testConfigPath(t)
	data, err := json.Marshal(validConfig("acl.example.local"))
	if err != nil {
		t.Fatalf("serializar config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("preparar config: %v", err)
	}

	var hardened []string
	svc := NewService(Options{
		ConfigPaths:     func() []string { return []string{path} },
		WatcherInterval: time.Hour,
		HardenFileACL: func(p string) error {
			hardened = append(hardened, p)
			return errors.New("icacls falhou (simulado)")
		},
	})
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	if len(hardened) != 1 || hardened[0] != path {
		t.Fatalf("DACL do arquivo existente deveria ser endurecida 1x em %q, got %v", path, hardened)
	}
	if got := svc.GetConfig(); got.ApiServer != "acl.example.local" {
		t.Fatalf("falha de ACL não pode impedir o carregamento da config: %+v", got)
	}
}

// TestConfigWatcherDoesNotOverwriteConfigPersistedByService é a regressão da
// corrida watcher x RPC (A.1.3): o watcher lia o debug_config.json, uma
// gravação do serviço (config:set/SetConfig) acontecia nesse meio-tempo e a
// adoção do conteúdo LIDO ANTES publicava em memória a config VELHA — memória
// divergente do disco até a próxima alteração externa.
func TestConfigWatcherDoesNotOverwriteConfigPersistedByService(t *testing.T) {
	path := testConfigPath(t)
	initial := validConfig("initial.example.local")
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("serializar config inicial: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("preparar config inicial: %v", err)
	}

	// Watcher com intervalo longo: o teste dispara o poll manualmente para
	// controlar a ordem dos eventos.
	svc := serviceForPath(path, time.Hour)
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	// Injeta um "atraso" na leitura do arquivo pelo watcher: ele captura o
	// conteúdo ANTIGO e só continua depois da gravação do serviço.
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	var once sync.Once
	var stale []byte
	oldRead := osReadFile
	t.Cleanup(func() { osReadFile = oldRead })
	osReadFile = func(p string) ([]byte, error) {
		if p != path {
			return oldRead(p)
		}
		once.Do(func() {
			stale, _ = oldRead(p)
			close(readStarted)
			<-releaseRead
		})
		return stale, nil
	}

	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		svc.pollExternalConfigChange()
	}()
	<-readStarted

	newer := validConfig("rpc.example.local")
	newer.AuthToken = "token-rpc"
	setDone := make(chan error, 1)
	go func() { setDone <- svc.SetConfig(newer) }()

	// Tempo para o SetConfig chegar ao ponto de gravação: com a serialização
	// (applyMu) ele espera a adoção externa terminar; sem ela, já gravou.
	time.Sleep(150 * time.Millisecond)
	close(releaseRead)

	if err := <-setDone; err != nil {
		t.Fatalf("SetConfig concorrente com o watcher falhou: %v", err)
	}
	<-pollDone

	if got := svc.GetConfig(); got.ApiServer != "rpc.example.local" || got.AuthToken != "token-rpc" {
		t.Fatalf("watcher sobrescreveu a config mais nova persistida pelo serviço: %+v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler config final: %v", err)
	}
	var persisted Config
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("config final inválida: %v — %q", err, string(raw))
	}
	if persisted.ApiServer != "rpc.example.local" {
		t.Fatalf("arquivo final não é o gravado pelo serviço: %q", persisted.ApiServer)
	}
}
