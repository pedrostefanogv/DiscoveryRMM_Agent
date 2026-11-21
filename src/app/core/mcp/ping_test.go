package mcp

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestValidateLocalHostOrIP_Privates(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "192.168.1.1", "10.0.0.5"} {
		if err := ValidateLocalHostOrIP(host); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", host, err)
		}
	}
}

func TestValidateLocalHostOrIP_PublicIP(t *testing.T) {
	if err := ValidateLocalHostOrIP("8.8.8.8"); err == nil {
		t.Errorf("expected public IP to be rejected")
	}
}

func TestBuildPingCommand_ContainsHost(t *testing.T) {
	cmd, err := buildPingCommand("192.168.0.1", 1, 1)
	if err != nil {
		t.Fatalf("buildPingCommand failed: %v", err)
	}
	if len(cmd.Args) == 0 {
		t.Fatal("expected command args")
	}
	found := false
	for _, a := range cmd.Args {
		if a == "192.168.0.1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ping args to include host, got %v", cmd.Args)
	}
}

func TestBuildFlushDNSCommand_Exists(t *testing.T) {
	cmd, err := buildFlushDNSCommand()
	if err != nil {
		t.Skipf("flush DNS command not available on this platform (%s): %v", runtime.GOOS, err)
	}
	if cmd == nil || len(cmd.Args) == 0 {
		t.Fatalf("expected valid command, got %v", cmd)
	}
}

// O agent roda no contexto do usuário (sem UAC). Nesse cenário o
// `ipconfig /flushdns` devolve "A operação solicitada requer elevação"
// (exit 1) e a limpeza nunca acontecia; o caminho nativo via dnsapi.dll resolve.
func TestFlushDNS_SucceedsWithoutElevation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("teste especifico do caminho nativo do Windows")
	}
	res, err := FlushDNS(context.Background())
	if err != nil {
		t.Fatalf("FlushDNS devolveu erro: %v", err)
	}
	if !res.Success {
		t.Fatalf("flush DNS sem elevacao deveria funcionar; saida=%q", res.Output)
	}
}

// Garante que o simbolo realmente existe na DLL: syscall.LazyProc.Call PANICa
// quando o proc nao resolve, e um panic derrubaria o agent (por isso Find() +
// recover em flushDNSNative).
func TestFlushDNSNativeSymbolResolves(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("teste especifico do Windows")
	}
	if err := dnsapiDLL.Load(); err != nil {
		t.Fatalf("dnsapi.dll nao carregou: %v", err)
	}
	if err := procDnsFlushResolverCache.Find(); err != nil {
		t.Fatalf("DnsFlushResolverCache nao existe em dnsapi.dll: %v", err)
	}
}

func TestWithFlushDNSHint_OnlyForElevationFailures(t *testing.T) {
	withHint := withFlushDNSHint("A operacao solicitada requer elevacao.")
	if !strings.Contains(withHint, "administrador") {
		t.Fatalf("esperava dica de elevacao, got %q", withHint)
	}
	plain := withFlushDNSHint("Successfully flushed the DNS Resolver Cache.")
	if strings.Contains(plain, "administrador") {
		t.Fatalf("nao deveria acrescentar dica: %q", plain)
	}
}

func TestRegistryOpenAIFunctions_ConcurrentSafe(t *testing.T) {
	reg := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reg.Register(Tool{
				Name:        "tool_" + string(rune('a'+idx)),
				Description: "test",
				Handler: func(ctx context.Context, args map[string]any) (any, error) {
					return nil, nil
				},
			})
			_ = reg.OpenAIFunctions()
		}(i)
	}
	wg.Wait()
	if got := len(reg.OpenAIFunctions()); got != 8 {
		t.Fatalf("expected 8 tools, got %d", got)
	}
}

func TestPingHost_ZeroTimeoutDoesNotExpireEarly(t *testing.T) {
	res, err := PingHost(context.Background(), "127.0.0.1", 1, 0)
	if err != nil {
		t.Fatalf("PingHost(127.0.0.1, 1, 0) failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected localhost ping to succeed, output=%q", res.Output)
	}
}

func TestOpenAIFunctionsFiltered_RespectsPredicate(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{"get_inventory", "service_control", "ping"} {
		reg.Register(Tool{
			Name:        name,
			Description: "test",
			Handler:     func(ctx context.Context, args map[string]any) (any, error) { return nil, nil },
		})
	}
	filtered := reg.OpenAIFunctionsFiltered(func(name string) bool {
		return name != "service_control"
	})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered tools, got %d", len(filtered))
	}
}

func TestClearScreenshotCache_ClearsEntries(t *testing.T) {
	ref := cacheScreenshotImage("data:image/png;base64,AAAA")
	if ref == "" {
		t.Fatal("expected non-empty ref")
	}
	ClearScreenshotCache()
	if _, ok := screenshotByRef(ref); ok {
		t.Fatal("expected screenshot cache to be cleared")
	}
}

func TestGetTopProcesses_NativeReturnsValidJSON(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	raw, err := GetTopProcesses(context.Background(), 5, "memory")
	if err != nil {
		t.Fatalf("GetTopProcesses failed: %v", err)
	}
	if len(raw) == 0 || raw[0] != '[' {
		t.Fatalf("expected JSON array, got %s", string(raw))
	}
}
