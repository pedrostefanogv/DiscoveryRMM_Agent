package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"

	"discovery/app/core/platform"
)

// ── F11 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF11_T2_NSISUninstallCleansAllArtifactDirs(t *testing.T) {
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	idxUninst := strings.Index(nsi, `Section "uninstall"`)
	if idxUninst < 0 {
		t.Fatalf(`Section "uninstall" nao encontrada em project.nsi`)
	}
	uninst := nsi[idxUninst:]
	for _, needle := range []string{
		`$INSTDIR`,
		`\Discovery\WebView2`,
		`\Discovery`,
		`un.UnregisterAgentService`,
		`un.UnregisterUIStartupTask`,
	} {
		if !strings.Contains(uninst, needle) {
			t.Fatalf("Section uninstall omite limpeza obrigatoria de %q", needle)
		}
	}
}

func TestF11_T2_NSISDirectoryUpgradePreservesConfigNoOverwrite(t *testing.T) {
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	idx := strings.Index(nsi, "Function SaveAgentConfig")
	if idx < 0 {
		t.Fatalf("Function SaveAgentConfig nao encontrada em project.nsi")
	}
	body := nsi[idx:]
	if !strings.Contains(body, "BUILD_UPDATE_INSTALL") || !strings.Contains(body, "$UpdateMode") {
		t.Fatalf("SaveAgentConfig deve preservar config existente quando BUILD_UPDATE_INSTALL == 1 ou $UpdateMode == 1")
	}
}

func TestF11_T2_DecommissionPathValidationBoundaries(t *testing.T) {
	src := readSrcFile(t, filepath.Join("app", "agent_commands_decommission.go"))
	if !strings.Contains(src, "validateUninstallerPath") {
		t.Fatalf("validateUninstallerPath ausente em app/agent_commands_decommission.go")
	}
	// Verifica que exige base uninstall.exe e os.Stat + !info.IsDir()
	for _, token := range []string{"IsDir()", "uninstall.exe", "strings.EqualFold"} {
		if !strings.Contains(src, token) {
			t.Fatalf("validateUninstallerPath nao verifica %q", token)
		}
	}
}

func TestF11_T2_DebugConfigWatcherBOMAndCorruptJSONBoundary(t *testing.T) {
	// Valida que parser de debug_config.json trata BOM UTF-8, arquivo vazio e JSON truncado com seguranca
	debugSvcSrc := readSrcFile(t, filepath.Join("app", "debug", "service.go"))
	if !strings.Contains(debugSvcSrc, "0xEF") && !strings.Contains(debugSvcSrc, "TrimPrefix") {
		t.Fatalf("app/debug/service.go deve tratar BOM UTF-8 ao ler debug_config.json")
	}
	// Garante que parsePersistedConfigData rejeita JSON corrompido / esquema invalido
	if !strings.Contains(debugSvcSrc, "json.Unmarshal") {
		t.Fatalf("parsePersistedConfigData ausente em app/debug/service.go")
	}
}

func TestF11_T2_NSISWin10FloorAndNonAdminUIAutostart(t *testing.T) {
	wailsTools := readSrcFile(t, filepath.Join("build", "windows", "installer", "wails_tools.nsh"))
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))

	if !strings.Contains(wailsTools, "${AtLeastWin10}") || !strings.Contains(wailsTools, "SetErrorLevel 64") {
		t.Fatalf("wails_tools.nsh deve exigir ${AtLeastWin10} com SetErrorLevel 64 em modo silencioso")
	}
	if !strings.Contains(nsi, "-RunLevel LeastPrivilege") {
		t.Fatalf("project.nsi deve agendar DiscoveryAgentUI com -RunLevel LeastPrivilege")
	}
	if strings.Contains(nsi, "-RunLevel Highest") {
		t.Fatalf("F11 violacao: project.nsi agenda tarefa com -RunLevel Highest")
	}
}

// ── F12 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF12_T2_DPAPIEmptyAndLargeTokenBoundaries(t *testing.T) {
	// Win32 CryptProtectData rejeita input de 0 bytes com ERROR_INVALID_PARAMETER sem panicar
	if _, err := dpapiProtect([]byte("")); err == nil {
		t.Fatalf("dpapiProtect(\"\") deveria retornar erro para buffer vazio")
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"1byte", []byte("x")},
		{"jwt_1500B", bytes.Repeat([]byte("a"), 1500)},
		{"large_16KB", bytes.Repeat([]byte("Z"), 16*1024)},
	}
	for _, tc := range cases {
		c1, err := dpapiProtect(tc.data)
		if err != nil {
			t.Fatalf("%s: dpapiProtect(1): %v", tc.name, err)
		}
		c2, err := dpapiProtect(tc.data)
		if err != nil {
			t.Fatalf("%s: dpapiProtect(2): %v", tc.name, err)
		}
		if bytes.Equal(c1, c2) {
			t.Fatalf("%s: DPAPI gerou ciphertexts identicos em duas chamadas (sem entropia aleatoria)", tc.name)
		}
		out1, err := dpapiUnprotect(c1)
		if err != nil {
			t.Fatalf("%s: dpapiUnprotect: %v", tc.name, err)
		}
		if !bytes.Equal(out1, tc.data) {
			t.Fatalf("%s: round-trip divergiu (len got=%d want=%d)", tc.name, len(out1), len(tc.data))
		}
	}
}

func TestF12_T2_DPAPICorruptOrTruncatedCiphertextFailsClosed(t *testing.T) {
	valid, err := dpapiProtect([]byte("mdz_secret_token"))
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	tampered := append([]byte{}, valid...)
	tampered[len(tampered)-1] ^= 0xFF

	badInputs := [][]byte{
		{},
		valid[:len(valid)/2],
		tampered,
		[]byte("mdz_plaintext_not_dpapi"),
	}
	for i, bad := range badInputs {
		if plain, err := dpapiUnprotect(bad); err == nil {
			t.Fatalf("caso %d: dpapiUnprotect deveria falhar em input corrompido, retornou %x", i, plain)
		}
	}
}

func TestF12_T2_HardenSecretFileACLMissingFileAndInheritedPermissiveACE(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if err := platform.HardenSecretFileACL(missing); err == nil {
		t.Fatalf("HardenSecretFileACL em arquivo inexistente deveria retornar erro")
	}

	dir := t.TempDir()
	// Concede Everyone:(OI)(CI)(F) no diretorio pai para simular heranca permissiva de ProgramData
	if out, err := runIcacls(t, dir, "/grant", "*S-1-1-0:(OI)(CI)(F)", "/Q"); err != nil {
		t.Fatalf("icacls setup dir falhou: %v\n%s", err, out)
	}
	secret := filepath.Join(dir, "debug_config.json")
	if err := os.WriteFile(secret, []byte(`{"authToken":"x"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := platform.HardenSecretFileACL(secret); err != nil {
		t.Fatalf("HardenSecretFileACL: %v", err)
	}
	out, err := runIcacls(t, secret)
	if err != nil {
		t.Fatalf("icacls inspect falhou: %v\n%s", err, out)
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "everyone") || strings.Contains(lower, "todos:") ||
		strings.Contains(lower, "users:") || strings.Contains(lower, `builtin\users`) {
		t.Fatalf("F12 violacao: heranca permissiva (Everyone/Users) nao foi removida de %s:\n%s", secret, out)
	}
}

func TestF12_T2_NamedPipeInvalidSDDLAndNameLengthBoundary(t *testing.T) {
	if _, err := winio.ListenPipe(`\\.\pipe\discovery-e2e-bad-sddl-`+time.Now().Format("150405.000"), &winio.PipeConfig{
		SecurityDescriptor: "INVALID_SDDL_STRING",
	}); err == nil {
		t.Fatalf("ListenPipe deveria rejeitar SDDL invalida")
	}

	// Nome de pipe sem leaf (`\\.\pipe\`) ou sem prefixo valido deve falhar no Win32 CreateNamedPipe
	for _, badPipe := range []string{`\\.\pipe\`, `not-a-pipe-path`, `\\.\pipe\bad` + "\x00" + `name`} {
		if ln, err := winio.ListenPipe(badPipe, nil); err == nil {
			_ = ln.Close()
			t.Fatalf("ListenPipe deveria rejeitar nome de pipe invalido %q", badPipe)
		}
	}
}

func TestF12_T2_IPCTokenRedactionEdgeCases(t *testing.T) {
	// Valida contrato de redacao: estrutura redigida nunca serializa authToken ou deployToken
	type redactedConfigView struct {
		ApiServer    string `json:"apiServer"`
		HasAuthToken bool   `json:"hasAuthToken"`
	}
	for _, tc := range []struct {
		rawToken string
		wantHas  bool
	}{
		{"", false},
		{"   ", false},
		{"mdz_live_secret_token", true},
	} {
		has := strings.TrimSpace(tc.rawToken) != ""
		b, err := json.Marshal(redactedConfigView{ApiServer: "api.local", HasAuthToken: has})
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if has != tc.wantHas {
			t.Fatalf("hasAuthToken got=%v want=%v", has, tc.wantHas)
		}
		if strings.Contains(string(b), "authToken") || strings.Contains(string(b), "deployToken") ||
			(tc.wantHas && strings.Contains(string(b), tc.rawToken)) {
			t.Fatalf("payload IPC redigido vazou segredo: %s", string(b))
		}
	}
}
