package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A leitura de arquivo pela IA e sempre precedida de autorizacao do usuario;
// estes testes cobrem as guardas puras (caminho, binario e limite de bytes).

func TestValidateReadFilePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"vazio", "", true},
		{"so espacos", "   ", true},
		{"relativo", "arquivo.txt", true},
		{"relativo com pasta", "logs\\app.log", true},
		{"UNC", `\\servidor\share\arquivo.txt`, true},
		{"dispositivo", `\\.\PhysicalDrive0`, true},
		{"com NUL", "C:\\temp\\a\x00.txt", true},
		{"com quebra de linha", "C:\\temp\\a\n.txt", true},
		{"absoluto valido", `C:\Windows\win.ini`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateReadFilePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("esperava erro para %q, veio %q", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("nao esperava erro para %q: %v", tt.path, err)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("esperava caminho absoluto, veio %q", got)
			}
		})
	}
}

func TestLooksBinary(t *testing.T) {
	if looksBinary([]byte("texto normal com acentos: acao, coracao")) {
		t.Fatal("texto nao pode ser classificado como binario")
	}
	if !looksBinary([]byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x01}) {
		t.Fatal("payload com NUL deveria ser binario (ex: zip)")
	}
	// NUL alem da janela de probe nao deve ser detectado (limite conhecido).
	long := append([]byte(strings.Repeat("a", readFileBinaryProbe+10)), 0x00)
	if looksBinary(long) {
		t.Fatal("NUL fora da janela de probe nao deve marcar como binario")
	}
}

func TestSensitiveReadFileHint(t *testing.T) {
	if got := sensitiveReadFileHint(`C:\Users\pedro\.ssh\id_rsa`); got == "" {
		t.Fatal("esperava alerta para caminho sensivel")
	}
	if got := sensitiveReadFileHint(`C:\Windows\win.ini`); got != "" {
		t.Fatalf("nao esperava alerta, veio %q", got)
	}
}

func TestBuildReadFileQuestionIncludesPathAndWarning(t *testing.T) {
	q := buildReadFileQuestion(`C:\Users\pedro\.aws\credentials`, 1234, "diagnosticar proxy")
	for _, want := range []string{"C:\\Users\\pedro\\.aws\\credentials", "1234", "diagnosticar proxy", "ATENCAO"} {
		if !strings.Contains(q, want) {
			t.Fatalf("pergunta deveria conter %q:\n%s", want, q)
		}
	}
}

func TestReadFileCappedTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grande.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}

	data, truncated, err := readFileCapped(path, 1024)
	if err != nil {
		t.Fatalf("readFileCapped: %v", err)
	}
	if !truncated {
		t.Fatal("esperava truncated=true para arquivo maior que o limite")
	}
	if len(data) != 1024 {
		t.Fatalf("esperava 1024 bytes, veio %d", len(data))
	}

	data, truncated, err = readFileCapped(path, 10000)
	if err != nil {
		t.Fatalf("readFileCapped: %v", err)
	}
	if truncated || len(data) != 5000 {
		t.Fatalf("arquivo menor que o limite nao pode truncar (len=%d truncado=%v)", len(data), truncated)
	}
}
