package e2e_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"

	"discovery/app/core/platform"
)

func TestF12_T1_HardenSecretFileACLGrantsOnlySystemAndAdmins(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(secretFile, []byte(`{"authToken":"mdz_test"}`), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	if err := platform.HardenSecretFileACL(secretFile); err != nil {
		t.Fatalf("platform.HardenSecretFileACL: %v", err)
	}

	out, err := runIcacls(t, secretFile)
	if err != nil {
		t.Fatalf("icacls %s falhou: %v\n%s", secretFile, err, out)
	}
	lower := strings.ToLower(out)

	// Nao deve herdar permissoes (I) nem conceder qualquer acesso a Users / Builtin\Users / Everyone / Autenticados
	if strings.Contains(lower, "users:") || strings.Contains(lower, `builtin\users`) ||
		strings.Contains(lower, `usuários`) || strings.Contains(lower, "everyone:") ||
		strings.Contains(lower, "todos:") || strings.Contains(lower, "authenticated users") {
		t.Fatalf("F12 violacao: HardenSecretFileACL manteve permissao para Users/Everyone em %s:\n%s", secretFile, out)
	}

	if !strings.Contains(lower, `nt authority\system:(f)`) && !strings.Contains(lower, `autoridade nt\sistema:(f)`) {
		t.Fatalf("F12 violacao: HardenSecretFileACL nao concedeu SYSTEM:(F) em %s:\n%s", secretFile, out)
	}

	if !strings.Contains(lower, `administrators:(f)`) && !strings.Contains(lower, `administradores:(f)`) {
		t.Fatalf("F12 violacao: HardenSecretFileACL nao concedeu Administrators:(F) em %s:\n%s", secretFile, out)
	}
}

func TestF12_T1_DPAPILocalMachineRoundTripAndCiphertextOpaque(t *testing.T) {
	const secret = "mdz_super_secret_agent_token_0123456789"
	cipher, err := dpapiProtect([]byte(secret))
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	if len(cipher) == 0 {
		t.Fatalf("dpapiProtect retornou ciphertext vazio")
	}
	if bytes.Contains(cipher, []byte(secret)) {
		t.Fatalf("ciphertext DPAPI contem plaintext")
	}
	b64 := base64.StdEncoding.EncodeToString(cipher)
	if strings.Contains(b64, secret) {
		t.Fatalf("base64 DPAPI contem plaintext")
	}

	plain, err := dpapiUnprotect(cipher)
	if err != nil {
		t.Fatalf("dpapiUnprotect: %v", err)
	}
	if string(plain) != secret {
		t.Fatalf("round-trip DPAPI divergiu: got=%q want=%q", string(plain), secret)
	}
}

func TestF12_T1_ConfigWritersEncryptTokensAndHardenACL(t *testing.T) {
	targets := []string{
		filepath.Join(srcRoot(t), "app", "installer", "service.go"),
		filepath.Join(srcRoot(t), "app", "debug", "installer.go"),
		filepath.Join(srcRoot(t), "app", "debug", "service.go"),
	}

	var hasDPAPIInApp bool
	err := filepath.WalkDir(filepath.Join(srcRoot(t), "app"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		s := string(b)
		if strings.Contains(s, "CryptProtectData") || strings.Contains(s, "authTokenDpapi") || strings.Contains(s, "DPAPI") {
			hasDPAPIInApp = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(app): %v", err)
	}
	if !hasDPAPIInApp {
		t.Fatalf("F12 violacao: nenhuma implementacao DPAPI (CryptProtectData/authTokenDpapi) encontrada em Discovery/src/app/")
	}

	for _, filePath := range targets {
		b, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("os.ReadFile(%s): %v", filePath, err)
		}
		src := string(b)
		if !strings.Contains(src, "HardenSecretFileACL") && !strings.Contains(src, "hardenACL") {
			t.Fatalf("F12 violacao: %s grava config/debug_config sem invocar HardenSecretFileACL", filePath)
		}
	}
}

func TestF12_T1_NamedPipeSDDLMatchesLeastPrivilegeAndExchangesJSONLines(t *testing.T) {
	ipcWin := readSrcFile(t, filepath.Join("app", "ipc_windows.go"))

	if !strings.Contains(ipcWin, `D:(A;;GA;;;SY)(A;;GRGW;;;BU)`) {
		t.Fatalf("F12: ipc_windows.go deve definir SDDL de leitura D:(A;;GA;;;SY)(A;;GRGW;;;BU)")
	}
	if !strings.Contains(ipcWin, `D:(A;;GA;;;SY)(A;;GA;;;BU)`) {
		t.Fatalf("F12: ipc_windows.go deve definir SDDL admin D:(A;;GA;;;SY)(A;;GA;;;BU)")
	}

	pipeName := `\\.\pipe\discovery-e2e-ipc-` + strings.ReplaceAll(t.Name(), "/", "_") + `-` + time.Now().Format("150405.000000")
	ln, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:(A;;GA;;;SY)(A;;GRGW;;;BU)",
		MessageMode:        false,
		InputBufferSize:    16 * 1024,
		OutputBufferSize:   16 * 1024,
	})
	if err != nil {
		t.Fatalf("winio.ListenPipe(%s): %v", pipeName, err)
	}
	defer ln.Close()

	srvErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		line, err := r.ReadBytes('\n')
		if err != nil {
			srvErr <- err
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			srvErr <- err
			return
		}
		resp, _ := json.Marshal(map[string]any{
			"id": req["id"],
			"ok": true,
			"result": map[string]any{
				"apiServer":    "api.example.local",
				"hasAuthToken": true,
			},
		})
		_, err = conn.Write(append(resp, '\n'))
		srvErr <- err
	}()

	d := time.Second * 3
	c, err := winio.DialPipe(pipeName, &d)
	if err != nil {
		t.Fatalf("winio.DialPipe(%s): %v", pipeName, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))

	reqBytes, _ := json.Marshal(map[string]any{"v": 2, "id": "1", "method": "debug:get"})
	if _, err := c.Write(append(reqBytes, '\n')); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	resp := readJSONLine(t, bufio.NewReader(c), 3*time.Second)
	if resp["ok"] != true {
		t.Fatalf("resposta IPC inesperada: %v", resp)
	}
	if err := <-srvErr; err != nil && !strings.Contains(err.Error(), net.ErrClosed.Error()) {
		t.Fatalf("servidor pipe erro: %v", err)
	}
}

func TestF12_T1_IPCSecretRedactionContract(t *testing.T) {
	appDir := filepath.Join(srcRoot(t), "app")
	var foundDebugGet, foundRedaction bool

	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		if strings.Contains(src, "ipcRPCDebugGet") || strings.Contains(src, `"debug:get"`) {
			foundDebugGet = true
			if strings.Contains(src, "hasAuthToken") || strings.Contains(src, "Redact") || strings.Contains(src, "redact") {
				foundRedaction = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(app): %v", err)
	}
	if !foundDebugGet {
		t.Fatalf("handler debug:get nao encontrado em app/")
	}
	if !foundRedaction {
		t.Fatalf("F12 violacao: handlers IPC (debug:get / config:set) nao redigem authToken/deployToken nem expoem hasAuthToken")
	}
}
