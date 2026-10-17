//go:build windows

package terminal

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// ── Descoberta do zsh: PATH + locais conhecidos (Git/MSYS2/Cygwin) ──
//
// zshResolver é injetável para que os testes não dependam do host: sem
// executável o shell não pode ser oferecido.

// fakeFileInfo implementa os.FileInfo para o FS falso do resolvedor.
type fakeFileInfo struct {
	dir bool
}

func (f fakeFileInfo) Name() string       { return "zsh.exe" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

// TestZshResolver_EncontraNoPATH cobre o caso comum: zsh.exe no PATH.
func TestZshResolver_EncontraNoPATH(t *testing.T) {
	probe := zshResolver{
		lookPath: func(nome string) (string, error) {
			if nome == "zsh.exe" {
				return "C:\\Git\\usr\\bin\\zsh.exe", nil
			}
			return "", errors.New("nao encontrado")
		},
		stat: func(string) (os.FileInfo, error) { return nil, errors.New("sem fs") },
	}
	caminho, ok := probe.find()
	if !ok || caminho != "C:\\Git\\usr\\bin\\zsh.exe" {
		t.Fatalf("find() = (%q,%v), want caminho do PATH", caminho, ok)
	}
}

// TestZshResolver_EncontraNosLocaisConhecidos cobre Git/MSYS2/Cygwin quando o
// PATH não tem zsh: o primeiro caminho que for ARQUIVO vence.
func TestZshResolver_EncontraNosLocaisConhecidos(t *testing.T) {
	alvo := zshKnownPaths[1]
	probe := zshResolver{
		lookPath: func(string) (string, error) { return "", errors.New("fora do PATH") },
		stat: func(caminho string) (os.FileInfo, error) {
			switch {
			case caminho == zshKnownPaths[0]:
				return fakeFileInfo{dir: true}, nil // diretório não serve
			case caminho == alvo:
				return fakeFileInfo{}, nil
			}
			return nil, errors.New("ausente")
		},
	}
	caminho, ok := probe.find()
	if !ok || caminho != alvo {
		t.Fatalf("find() = (%q,%v), want %q", caminho, ok, alvo)
	}
}

// TestZshResolver_SemExecutavelNaoOferece cobre a máquina sem zsh.
func TestZshResolver_SemExecutavelNaoOferece(t *testing.T) {
	probe := zshResolver{
		lookPath: func(string) (string, error) { return "", errors.New("ausente") },
		stat:     func(string) (os.FileInfo, error) { return nil, errors.New("ausente") },
	}
	if caminho, ok := probe.find(); ok || caminho != "" {
		t.Fatalf("find() = (%q,%v), want (vazio,false)", caminho, ok)
	}
}

// TestResolveShell_ZshUsaResolvedorInjetavel garante que ResolveShell oferece
// zsh somente com executável e nunca rebaixa para cmd.
func TestResolveShell_ZshUsaResolvedorInjetavel(t *testing.T) {
	presente := zshResolver{
		lookPath: func(nome string) (string, error) {
			if nome == "zsh.exe" {
				return `C:\Program Files\Git\bin\zsh.exe`, nil
			}
			return "", errors.New("ausente")
		},
		stat: func(string) (os.FileInfo, error) { return nil, errors.New("ausente") },
	}
	kind, caminho := resolveShellWith(ShellZsh, presente)
	if kind != ShellZsh || caminho == "" {
		t.Fatalf("resolveShellWith(zsh) = (%q,%q), want zsh com caminho", kind, caminho)
	}

	ausente := zshResolver{
		lookPath: func(string) (string, error) { return "", errors.New("ausente") },
		stat:     func(string) (os.FileInfo, error) { return nil, errors.New("ausente") },
	}
	kind, caminho = resolveShellWith(ShellZsh, ausente)
	if kind != ShellZsh || caminho != "" {
		t.Fatalf("sem zsh: resolveShellWith = (%q,%q), want (zsh,\"\")", kind, caminho)
	}
}

// TestAvailableShellKinds_ZshSoQuandoInstalado cobre a listagem enviada no
// term.ready: zsh aparece só com executável; os demais shells não mudam.
func TestAvailableShellKinds_ZshSoQuandoInstalado(t *testing.T) {
	semWSL := func() (bool, []string) { return false, nil }
	comZsh := availableShellKinds(func() bool { return true }, semWSL)
	if !contemShellKind(comZsh, string(ShellZsh)) {
		t.Fatalf("zsh instalado deveria aparecer na lista: %v", comZsh)
	}
	semZsh := availableShellKinds(func() bool { return false }, semWSL)
	if contemShellKind(semZsh, string(ShellZsh)) {
		t.Fatalf("zsh ausente NAO deveria aparecer na lista: %v", semZsh)
	}
	for _, esperado := range []string{string(ShellPowerShell), string(ShellCmd)} {
		if !contemShellKind(semZsh, esperado) {
			t.Fatalf("shell %q sumiu da lista: %v", esperado, semZsh)
		}
	}
	comWSL := availableShellKinds(func() bool { return true }, func() (bool, []string) {
		return true, []string{"Ubuntu"}
	})
	if !contemShellKind(comWSL, "wsl:Ubuntu") {
		t.Fatalf("wsl:Ubuntu deveria continuar na lista: %v", comWSL)
	}
}

// contemShellKind informa se a lista contém o shell (case-insensitive).
func contemShellKind(shells []string, quer string) bool {
	for _, s := range shells {
		if strings.EqualFold(s, quer) {
			return true
		}
	}
	return false
}
