//go:build windows

package remotesession

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"discovery/app/core/terminal"
)

// ── Seleção de provider pelo registry ──

func TestCompletionRegistry_SelecionaProviderPorShell(t *testing.T) {
	r := newCompletionRegistry()
	casos := []struct {
		shell   string
		backend string
		quer    string
	}{
		{"powershell", "conpty", "powershell"},
		{"pwsh", "conpty", "powershell"},
		{"cmd", "conpty", "cmd"},
		{"bash", "conpty", "posix"},
		{"zsh", "conpty", "posix"},
		{"wsl", "conpty", "posix"},
		{"wsl:Ubuntu", "conpty", "posix"},
	}
	for _, c := range casos {
		p := r.providerFor(terminal.ShellKind(c.shell), c.backend)
		if p == nil {
			t.Fatalf("%s/%s sem provider", c.shell, c.backend)
		}
		var obtido string
		switch p.(type) {
		case *powerShellCompletionProvider:
			obtido = "powershell"
		case *cmdCompletionProvider:
			obtido = "cmd"
		case *posixCompletionProvider:
			obtido = "posix"
		default:
			obtido = "desconhecido"
		}
		if obtido != c.quer {
			t.Fatalf("%s/%s escolheu %s, want %s", c.shell, c.backend, obtido, c.quer)
		}
	}
}

func TestCompletionRegistry_SemSuporte(t *testing.T) {
	r := newCompletionRegistry()
	casos := []struct {
		shell   string
		backend string
	}{
		{"powershell", "legacy"},
		{"cmd", "legacy"},
		{"bash", "legacy"},
		{"zsh", ""},
		{"", "conpty"},
		{"fish", "conpty"},
	}
	for _, c := range casos {
		if r.Supported(terminal.ShellKind(c.shell), c.backend) {
			t.Fatalf("%q/%q nao deveria ser suportado", c.shell, c.backend)
		}
	}
}

func TestCompletionSupported_RefleteRegistry(t *testing.T) {
	casos := []struct {
		shell   string
		backend string
		quer    bool
	}{
		{"powershell", "conpty", true},
		{"cmd", "conpty", true},
		{"bash", "conpty", true},
		{"zsh", "conpty", true},
		{"wsl:Debian", "conpty", true},
		{"cmd", "legacy", false},
		{"powershell", "legacy", false},
	}
	for _, c := range casos {
		if got := completionSupported(terminal.ShellKind(c.shell), c.backend); got != c.quer {
			t.Fatalf("completionSupported(%q,%q) = %v, want %v", c.shell, c.backend, got, c.quer)
		}
	}
}

// ── Prefixo de prompt é removido e o replacement volta à linha original ──

type capturaProvider struct {
	input  string
	cursor int
}

func (capturaProvider) Supports(terminal.ShellKind, string) bool { return true }

func (capturaProvider) Complete(context.Context, string, int) ([]completionMatch, error) {
	return []completionMatch{{Text: "x"}}, nil
}

func (c *capturaProvider) CompleteSpan(_ context.Context, input string, cursor int) (tabExpansionResult, error) {
	c.input, c.cursor = input, cursor
	return tabExpansionResult{
		ReplacementIndex:  0,
		ReplacementLength: 3,
		Matches:           []completionMatch{{Text: "dir"}},
	}, nil
}

func TestCompletionRegistry_RemovePrefixoDePrompt(t *testing.T) {
	cap := &capturaProvider{}
	r := &completionRegistry{providers: []completionProvider{cap}}
	res, err := r.Complete(context.Background(), terminal.ShellCmd, "conpty", "C:\\> dir", 8)
	if err != nil {
		t.Fatalf("Complete erro: %v", err)
	}
	if cap.input != "dir" || cap.cursor != 3 {
		t.Fatalf("provider recebeu (%q,%d), want (dir,3)", cap.input, cap.cursor)
	}
	if res.ReplacementIndex != 5 || res.ReplacementLength != 3 {
		t.Fatalf("replacement = (%d,%d), want (5,3)", res.ReplacementIndex, res.ReplacementLength)
	}
}

// ── Fallback: erro do provider vira ok=false sem derrubar a sessão ──

type falhaProvider struct{}

func (falhaProvider) Supports(terminal.ShellKind, string) bool { return true }

func (falhaProvider) Complete(context.Context, string, int) ([]completionMatch, error) {
	return nil, errors.New("provider indisponivel")
}

func TestCompletionRegistry_ErroDoProviderEhPropagado(t *testing.T) {
	r := &completionRegistry{providers: []completionProvider{falhaProvider{}}}
	if _, err := r.Complete(context.Background(), terminal.ShellCmd, "conpty", "dir", 3); err == nil {
		t.Fatal("erro do provider deveria ser propagado")
	}
}

func TestBuildCompletionResponse_ProviderComErroRetornaOkFalse(t *testing.T) {
	anterior := defaultCompletionRegistry
	defaultCompletionRegistry = &completionRegistry{providers: []completionProvider{falhaProvider{}}}
	t.Cleanup(func() { defaultCompletionRegistry = anterior })

	st := &SessionTerminal{sessionID: "sess-err", termShellKind: "cmd", termBackend: "conpty"}
	res := decodeRes(t, st.buildCompletionResponse([]byte("{\"reqId\":\"r1\",\"input\":\"dir\",\"cursor\":3}")))
	if res.Ok || res.Error == "" {
		t.Fatalf("provider com erro deveria dar ok=false + error; got %+v", res)
	}
}

// ── Truncamento e limite por resposta ──

type muitosProvider struct{}

func (muitosProvider) Supports(terminal.ShellKind, string) bool { return true }

func (muitosProvider) Complete(context.Context, string, int) ([]completionMatch, error) {
	out := make([]completionMatch, completionMaxItems+25)
	for i := range out {
		out[i] = completionMatch{Text: strings.Repeat("x", i+1)}
	}
	return out, nil
}

func TestCompletionRegistry_TruncaEmCompletionMaxItems(t *testing.T) {
	r := &completionRegistry{providers: []completionProvider{muitosProvider{}}}
	res, err := r.Complete(context.Background(), terminal.ShellCmd, "conpty", "x", 1)
	if err != nil {
		t.Fatalf("Complete erro: %v", err)
	}
	if len(res.Matches) != completionMaxItems {
		t.Fatalf("matches = %d, want %d", len(res.Matches), completionMaxItems)
	}
}

// ── Cache de executáveis do PATH: TTL e mudança de PATH ──

func TestCompletionPathIndex_CacheComTTL(t *testing.T) {
	agora := time.Unix(1000, 0)
	var varreduras int
	ix := &completionPathIndex{ttl: completionPathTTL, now: func() time.Time { return agora }}
	scan := func(context.Context, []string) []string {
		varreduras++
		return []string{"git", "go"}
	}

	ix.get(context.Background(), []string{"C:\\A"}, scan)
	ix.get(context.Background(), []string{"C:\\A"}, scan)
	if varreduras != 1 {
		t.Fatalf("varreduras = %d dentro do TTL, want 1", varreduras)
	}

	// PATH diferente invalida o cache na hora.
	ix.get(context.Background(), []string{"C:\\B"}, scan)
	if varreduras != 2 {
		t.Fatalf("varreduras = %d apos mudar o PATH, want 2", varreduras)
	}

	// TTL expirado varre de novo.
	agora = agora.Add(completionPathTTL + time.Second)
	ix.get(context.Background(), []string{"C:\\B"}, scan)
	if varreduras != 3 {
		t.Fatalf("varreduras = %d apos o TTL, want 3", varreduras)
	}
}

// ── Encoding de nomes com caracteres não-ASCII (cursor em runas) ──

func TestCmdProvider_CursorEmRunas(t *testing.T) {
	fs := fakeCompletionFS(
		map[string][]completionFSEntry{
			"C:\\Users\\ana": {entryDir("Documentos"), entryFile("ação.txt")},
		},
		nil,
		"C:\\Users\\ana",
		"C:\\Users\\ana",
	)
	matches, res := completarCMD(t, fs, "type aç")
	exigirMatch(t, matches, "ação.txt")
	if res.ReplacementIndex != len([]rune("type ")) || res.ReplacementLength != 2 {
		t.Fatalf("replacement = (%d,%d), want (5,2)", res.ReplacementIndex, res.ReplacementLength)
	}
}

// decodeRes decodifica a resposta de completion (helper dos testes de fallback).
func decodeRes(t *testing.T, raw []byte) completionRes {
	t.Helper()
	var res completionRes
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("resposta invalida: %v (%s)", err, raw)
	}
	return res
}
