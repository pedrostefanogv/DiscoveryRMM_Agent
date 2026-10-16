package fileserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// ── Troca de disco (múltiplos volumes) e itens ocultos ─────────────────────

// TestNewServer_NormalizesVolumeRoot garante que "C:" (drive-relative) vira
// "C:\\" — sem isso RootPath/Join não apontam para a raiz da unidade.
func TestNewServer_NormalizesVolumeRoot(t *testing.T) {
	cases := map[string]string{
		"C:":   "C:\\",
		"C:\\": "C:\\",
		"D:":   "D:\\",
	}
	for input, want := range cases {
		srv := NewServer(input)
		if srv.RootPath() != want {
			t.Fatalf("NewServer(%q).RootPath() = %q, want %q", input, srv.RootPath(), want)
		}
		if !srv.AllowOtherVolumes() {
			t.Fatalf("NewServer(%q): esperava AllowOtherVolumes=true", input)
		}
	}
}

// TestSafePath_DriveRelativeInput valida que "C:" digitado é tratado como raiz
// da unidade (e não como "C:\\C:" dentro do volume base).
func TestSafePath_DriveRelativeInput(t *testing.T) {
	srv := NewServer("C:\\")
	got, err := srv.safePath("C:")
	if err != nil {
		t.Fatalf("safePath(C:): erro inesperado: %v", err)
	}
	if got != "C:\\" {
		t.Fatalf("safePath(C:) = %q, want %q", got, "C:\\")
	}

	// Sandbox em subpasta continua bloqueando o restante do mesmo volume.
	sub := NewServer(filepath.Join(t.TempDir(), "sandbox"))
	if _, err := sub.safePath("C:\\Windows"); err == nil {
		t.Fatal("safePath(C:\\Windows) em sandbox de subpasta deveria falhar")
	}
}

// TestHandleRequest_Drives valida a ação "drives": quando o rootPath é a raiz
// de um volume a sessão oferece as unidades lógicas (C:, D:, ...).
func TestHandleRequest_Drives(t *testing.T) {
	srv := NewServer("C:\\")
	if !srv.AllowOtherVolumes() {
		t.Fatal("NewServer(C:\\): esperava AllowOtherVolumes=true")
	}

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "d1", "action": "drives", "path": ""}))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("drives: unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("drives: success=false err=%s", resp.Error)
	}
	if len(resp.Drives) == 0 {
		t.Fatal("drives: esperava ao menos uma unidade lógica")
	}
	foundC := false
	for _, d := range resp.Drives {
		if d.Root == "" || d.Name == "" || d.Type == "" {
			t.Fatalf("drives: unidade incompleta: %+v", d)
		}
		if d.Name == "C:" {
			foundC = true
			if !d.Ready {
				t.Fatalf("drives: C: deveria estar pronta: %+v", d)
			}
		}
	}
	if !foundC {
		t.Fatal("drives: unidade C: não encontrada")
	}
}

// TestHandleRequest_DrivesRestrictedSandbox garante que um sandbox em subpasta
// NÃO oferece troca de disco (a UI esconde o seletor).
func TestHandleRequest_DrivesRestrictedSandbox(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(filepath.Join(tmp, "sandbox"))
	if srv.AllowOtherVolumes() {
		t.Fatal("sandbox em subpasta: esperava AllowOtherVolumes=false")
	}

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "d2", "action": "drives", "path": ""}))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("drives: unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("drives: success=false err=%s", resp.Error)
	}
	if len(resp.Drives) != 0 {
		t.Fatalf("drives: sandbox restrito não deve listar unidades: %+v", resp.Drives)
	}
}

// TestSafePath_OtherVolumePolicy valida a política de volume:
//   - raiz de volume permite navegar em outra unidade existente;
//   - subpasta continua bloqueando volume externo;
//   - UNC permanece bloqueado mesmo com a troca de disco ligada.
func TestSafePath_OtherVolumePolicy(t *testing.T) {
	srvRoot := NewServer("C:\\")
	srvSub := NewServer(filepath.Join(t.TempDir(), "sandbox"))

	drives := ListDrives()
	var other *DriveInfo
	for i := range drives {
		if drives[i].Ready && !strings.EqualFold(drives[i].Name, "C:") {
			other = &drives[i]
			break
		}
	}
	if other == nil {
		t.Skip("host sem segunda unidade lógica pronta — nada a validar")
	}

	target := filepath.Join(other.Root, "pasta-qualquer")
	if got, err := srvRoot.safePath(target); err != nil {
		t.Fatalf("safePath(%q) com troca de disco: erro inesperado: %v", target, err)
	} else if !strings.EqualFold(got, target) {
		t.Fatalf("safePath(%q) = %q", target, got)
	}

	if _, err := srvSub.safePath(target); err == nil {
		t.Fatalf("safePath(%q) em sandbox de subpasta deveria falhar", target)
	} else if !strings.Contains(err.Error(), "fora do sandbox") {
		t.Fatalf("safePath(%q): erro inesperado: %v", target, err)
	}

	if _, err := srvRoot.safePath("\\\\servidor\\share\\pasta"); err == nil {
		t.Fatal("safePath UNC deveria ser bloqueado mesmo com troca de disco")
	}
}

// TestHandleRequest_ListMarksHidden valida o atributo HIDDEN do Windows no list
// e que o item oculto continua navegável.
func TestHandleRequest_ListMarksHidden(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	if err := os.WriteFile(filepath.Join(tmp, "visivel.txt"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	hiddenDir := filepath.Join(tmp, "pasta-oculta")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatal(err)
	}
	ptr, err := syscall.UTF16PtrFromString(hiddenDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetFileAttributes(ptr, 0x2 /* FILE_ATTRIBUTE_HIDDEN */); err != nil {
		t.Fatalf("SetFileAttributes: %v", err)
	}
	t.Cleanup(func() {
		if p, err := syscall.UTF16PtrFromString(hiddenDir); err == nil {
			_ = syscall.SetFileAttributes(p, 0x10 /* FILE_ATTRIBUTE_DIRECTORY */)
		}
	})

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "h1", "action": "list", "path": ""}))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("list: unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("list: success=false err=%s", resp.Error)
	}

	seen := map[string]FileInfo{}
	for _, f := range resp.Entries {
		seen[f.Name] = f
	}
	hidden, ok := seen["pasta-oculta"]
	if !ok {
		t.Fatal("list: pasta-oculta não retornada")
	}
	if !hidden.IsHidden {
		t.Fatalf("list: pasta-oculta deveria ter isHidden=true: %+v", hidden)
	}
	if visible, ok := seen["visivel.txt"]; !ok {
		t.Fatal("list: visivel.txt não retornado")
	} else if visible.IsHidden {
		t.Fatalf("list: visivel.txt não deveria ser oculto: %+v", visible)
	}

	// Item oculto continua navegável (list dentro dele).
	rDir := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "h2", "action": "list", "path": hidden.Path}))
	okResp(t, rDir, "list pasta oculta")
}
