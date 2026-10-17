//go:build windows

package remotesession

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// ── Atualização do cwd reportado ──

func TestApplyReportedCwd_AtualizaEstado(t *testing.T) {
	st := &SessionTerminal{sessionID: "sess-cwd", termShellKind: "pwsh"}
	dir := t.TempDir()
	st.applyReportedCwd(dir)

	if got := st.ReportedCwd(); got != dir {
		t.Fatalf("ReportedCwd = %q, want %q", got, dir)
	}
	if got := st.CurrentCwd(); got != dir {
		t.Fatalf("CurrentCwd = %q, want %q", got, dir)
	}
}

func TestApplyReportedCwd_ReportadoVenceHeuristico(t *testing.T) {
	st := &SessionTerminal{sessionID: "sess-cwd"}
	reportado := t.TempDir()
	outro := t.TempDir()
	st.applyReportedCwd(reportado)
	st.noteTerminalInput("cd " + outro + "\r")

	if got := st.CurrentCwd(); got != reportado {
		t.Fatalf("CurrentCwd = %q, want reportado %q", got, reportado)
	}
}

func TestPublishCwd_SubjectPayload(t *testing.T) {
	server := startLivenessNATS(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar NATS: %v", err)
	}
	t.Cleanup(nc.Close)

	h := NewNatsStreamHandler(nc, "client-1", "site-1", "agent-1")
	st := &SessionTerminal{sessionID: "sess-cwd-pub", natsStream: h, termShellKind: "powershell"}

	msgs := make(chan termCwdPayload, 4)
	sub, err := nc.Subscribe(h.publishSubject("sess-cwd-pub", "term.cwd"), func(msg *nats.Msg) {
		var p termCwdPayload
		if json.Unmarshal(msg.Data, &p) == nil {
			msgs <- p
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	dir := t.TempDir()
	st.applyReportedCwd(dir)
	_ = nc.Flush()

	select {
	case p := <-msgs:
		if p.Cwd != dir || p.ShellKind != "powershell" || p.AtMs <= 0 {
			t.Fatalf("payload inesperado: %+v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cwd nao foi publicado em .term.cwd")
	}

	// Mesmo path de novo nao republica (dedupe por mudança).
	st.applyReportedCwd(dir)
	_ = nc.Flush()
	select {
	case p := <-msgs:
		t.Fatalf("path repetido nao deveria republicar: %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTermReadyPayload_IncluiCwd(t *testing.T) {
	ready := buildTermReadyPayload([]string{"cmd"}, "main", 120, 40, "cmd", "cmd.exe", "conpty", "C:\\Users\\op")
	if ready["cwd"] != "C:\\Users\\op" {
		t.Fatalf("ready cwd = %v, want C:\\Users\\op", ready["cwd"])
	}
}

// ── Fallback heurístico de cd/Set-Location ──

func TestNoteTerminalInput_CdFallback(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	comEspaco := filepath.Join(base, "com espaço")
	if err := os.Mkdir(comEspaco, 0o755); err != nil {
		t.Fatalf("mkdir com espaço: %v", err)
	}
	home, _ := os.UserHomeDir()

	t.Run("absoluto", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd " + base + "\r")
		if got := st.CurrentCwd(); got != base {
			t.Fatalf("CurrentCwd = %q, want %q", got, base)
		}
	})

	t.Run("relativo", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd " + base + "\n")
		st.noteTerminalInput("cd sub\n")
		if got := st.CurrentCwd(); got != sub {
			t.Fatalf("CurrentCwd = %q, want %q", got, sub)
		}
	})

	t.Run("set-location com espaco e aspas", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("Set-Location \"" + comEspaco + "\"\n")
		if got := st.CurrentCwd(); got != comEspaco {
			t.Fatalf("CurrentCwd = %q, want %q", got, comEspaco)
		}
	})

	t.Run("inexistente nao muda", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd " + base + "\n")
		st.noteTerminalInput("cd nao-existe-xyz-123\n")
		if got := st.CurrentCwd(); got != base {
			t.Fatalf("CurrentCwd = %q, want %q (nao podia mudar)", got, base)
		}
	})

	t.Run("tilde vai para home", func(t *testing.T) {
		if home == "" {
			t.Skip("home indisponivel")
		}
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd ~\n")
		if got := st.CurrentCwd(); got != filepath.Clean(home) {
			t.Fatalf("CurrentCwd = %q, want home %q", got, home)
		}
	})

	t.Run("cd sem argumento vai para home", func(t *testing.T) {
		if home == "" {
			t.Skip("home indisponivel")
		}
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd\n")
		if got := st.CurrentCwd(); got != filepath.Clean(home) {
			t.Fatalf("CurrentCwd = %q, want home %q", got, home)
		}
	})

	t.Run("cmd cd /d", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd /d " + base + "\r\n")
		if got := st.CurrentCwd(); got != base {
			t.Fatalf("CurrentCwd = %q, want %q", got, base)
		}
	})

	t.Run("linha parcial nao aplica antes do enter", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("cd " + base)
		if got := st.heuristicCwd(); got != "" {
			t.Fatalf("heuristicCwd = %q, want vazio antes do Enter", got)
		}
	})

	t.Run("comando que nao e cd e ignorado", func(t *testing.T) {
		st := &SessionTerminal{sessionID: "s"}
		st.noteTerminalInput("echo cd " + base + "\n")
		if got := st.heuristicCwd(); got != "" {
			t.Fatalf("heuristicCwd = %q, want vazio", got)
		}
	})
}

func TestCurrentCwd_CaiParaHeuristicoQuandoReportadoNaoExiste(t *testing.T) {
	st := &SessionTerminal{sessionID: "s"}
	dir := t.TempDir()
	st.applyReportedCwd(filepath.Join(dir, "apagado"))
	st.noteTerminalInput("cd " + dir + "\n")
	if got := st.CurrentCwd(); got != dir {
		t.Fatalf("CurrentCwd = %q, want heurístico %q", got, dir)
	}
}

// ── Completação usa o cwd reportado ──

func TestCompletionRes_UsaCwdReportado(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}

	st := &SessionTerminal{sessionID: "sess-comp", termShellKind: "cmd", termBackend: "conpty"}
	st.applyReportedCwd(dir)
	st.sessionCompletions = newSessionCompletionRegistry(func() (string, error) {
		return st.completionWorkingDir(), nil
	})

	// Token 1 ("al") evita a varredura do PATH e força a listagem de diretório.
	out := st.buildCompletionResponse([]byte("{\"reqId\":\"r1\",\"input\":\"dir al\",\"cursor\":6}"))
	var res completionRes
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("resposta invalida: %v (%s)", err, out)
	}
	if !res.Ok {
		t.Fatalf("completion falhou: %+v", res)
	}
	if res.Cwd != dir {
		t.Fatalf("res.Cwd = %q, want %q", res.Cwd, dir)
	}
	achou := false
	for _, m := range res.Matches {
		if m.Text == "alpha.txt" {
			achou = true
		}
	}
	if !achou {
		t.Fatalf("alpha.txt nao veio da pasta reportada: %+v", res.Matches)
	}
}

func TestCompletionRes_TrazCwdMesmoComErro(t *testing.T) {
	dir := t.TempDir()
	st := &SessionTerminal{sessionID: "s", termShellKind: "powershell", termBackend: "legacy"}
	st.applyReportedCwd(dir)
	out := st.buildCompletionResponse([]byte("{\"reqId\":\"r1\",\"input\":\"Get-\",\"cursor\":4}"))
	var res completionRes
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("resposta invalida: %v", err)
	}
	if res.Ok {
		t.Fatalf("legacy deveria falhar: %+v", res)
	}
	if res.Cwd != dir {
		t.Fatalf("cwd aditivo ausente na resposta de erro: %q", res.Cwd)
	}
}

func TestCompletionCwdContext(t *testing.T) {
	dir := t.TempDir()
	ctx := withCompletionCwd(context.Background(), dir)
	if got := completionCwdFromContext(ctx); got != dir {
		t.Fatalf("completionCwdFromContext = %q, want %q", got, dir)
	}
	if got := completionCwdFromContext(withCompletionCwd(context.Background(), "")); got != "" {
		t.Fatalf("cwd vazio nao deveria ser anexado: %q", got)
	}
	if got := completionCwdFromContext(nil); got != "" {
		t.Fatalf("ctx nil = %q, want vazio", got)
	}
}
