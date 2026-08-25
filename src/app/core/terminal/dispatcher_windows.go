//go:build windows

package terminal

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// ── Dispatcher do terminal — executa o ConPTY num processo filho ──
//
// Contexto: o ConPTY rodado no processo principal do agente (Wails GUI)
// é suscetível ao crash 0xC0000142 (STATUS_DLL_INIT_FAILED) durante o boot por
// injeção de DLL de AV/injetores. O MeshCentral resolve isso rodando o
// terminal num processo filho isolado (dispatcher).
//
// Este arquivo implementa o LADO SERVIDOR do dispatcher: um subprocesso do
// próprio binário do agente, lançado com a flag "--terminal-dispatcher", que
// cria o ConPTY + spawn do shell sem a GUI. O processo pai (agente) se
// conecta a dois named pipes e troca input/output.
//
// Fica HABILITADO apenas quando a variável de ambiente
// DISCOVERY_TERM_DISPATCHER=1 está presente no agente (opt-in, para não
// alterar o caminho produtivo por padrão). Com fallback automático para o
// caminho atual (ConPTY in-process → legacy).

// RunDispatcher executa o modo dispatcher e bloqueia até o shell encerrar.
// É chamado do main.go quando "--terminal-dispatcher" está presente nos args.
func RunDispatcher() {
	args := os.Args
	var session, shellKind string
	cols, rows := 120, 40
	for i := 1; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		switch {
		case strings.HasPrefix(a, "--terminal-session="):
			session = strings.TrimPrefix(a, "--terminal-session=")
		case strings.HasPrefix(a, "--terminal-shell="):
			shellKind = strings.TrimPrefix(a, "--terminal-shell=")
		case strings.HasPrefix(a, "--terminal-cols="):
			if v, err := strconv.Atoi(strings.TrimPrefix(a, "--terminal-cols=")); err == nil {
				cols = v
			}
		case strings.HasPrefix(a, "--terminal-rows="):
			if v, err := strconv.Atoi(strings.TrimPrefix(a, "--terminal-rows=")); err == nil {
				rows = v
			}
		}
	}
	if session == "" || shellKind == "" {
		log.Printf("[term-dispatcher] args invalidos: session vazio ou shell vazio (%v)", args[1:])
		os.Exit(1)
	}

	inPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-in`, session)
	outPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-out`, session)

	// Cria os listeners (server) dos dois pipes antes de qualquer conexão.
	outL, err := winio.ListenPipe(outPipe, nil)
	if err != nil {
		log.Printf("[dispatcher] ListenPipe(out): %v", err)
		os.Exit(1)
	}
	defer outL.Close()
	inL, err := winio.ListenPipe(inPipe, nil)
	if err != nil {
		log.Printf("[dispatcher] ListenPipe(in): %v", err)
		os.Exit(1)
	}
	defer inL.Close()

	log.Printf("[dispatcher] aguardando agente conectar: session=%s cols=%d rows=%d", session, cols, rows)

	// Aceita as duas conexões (in e out) do agente.
	inConn, err := inL.Accept()
	if err != nil {
		log.Printf("[dispatcher] Accept(in): %v", err)
		os.Exit(1)
	}
	defer inConn.Close()
	outConn, err := outL.Accept()
	if err != nil {
		log.Printf("[dispatcher] Accept(out): %v", err)
		os.Exit(1)
	}
	defer outConn.Close()

	// Cria o ConPTY + shell. onOutput → escreve no pipe outConn.
	ish, err := NewConPTYShell(ShellKind(shellKind), cols, rows, func(output string) {
		_, _ = outConn.Write([]byte(output))
	})
	if err != nil {
		log.Printf("[dispatcher] NewConPTYShell: %v", err)
		os.Exit(1)
	}
	defer ish.Close()

	log.Printf("[dispatcher] shell iniciado: kind=%s backend=conpty-remote", shellKind)

	// Goroutine: monitor de exit → fecha.
	done := make(chan struct{})
	go func() {
		_ = ish.Wait()
		close(done)
	}()

	// Leitura do pipe in → shell.WriteStdin (envelope: base64 por linha).
	buf := make([]byte, 64*1024)
	var pending []byte
	for {
		n, err := inConn.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			// Processa linhas completas (separadas por \n).
			for {
				idx := -1
				for i := range pending {
					if pending[i] == '\n' {
						idx = i
						break
					}
				}
				if idx < 0 {
					break
				}
				line := pending[:idx]
				pending = pending[idx+1:]
				s := string(line)
				if strings.HasPrefix(s, "resize:") {
					parts := strings.SplitN(strings.TrimPrefix(s, "resize:"), "x", 2)
					if len(parts) == 2 {
						if c, e1 := strconv.Atoi(parts[0]); e1 == nil {
							if r, e2 := strconv.Atoi(parts[1]); e2 == nil {
								_ = ish.Resize(c, r)
							}
						}
					}
					continue
				}
				if s == "" {
					continue
				}
				if raw, derr := base64.StdEncoding.DecodeString(s); derr == nil {
					_ = ish.WriteStdin(string(raw))
				}
			}
		}
		if err != nil {
			break
		}
		select {
		case <-done:
			log.Printf("[dispatcher] shell encerrado")
			return
		default:
		}
	}
	log.Printf("[dispatcher] pipe in fechado; encerrando shell")
	// O agente fechou o pipe de entrada (fim da sessão). Fecha o shell para
	// não deixar o processo órfão antes de retornar.
	_ = ish.Close()
	// Não bloqueia indefinidamente: aguarda a morte com um teto de tempo.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("[dispatcher] timeout aguardando o shell encerrar após close")
	}
}

// DispatchersAvailable reporta se o modo dispatcher está habilitado via
// variável de ambiente (opt-in) no agente.
func DispatchersAvailable() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DISCOVERY_TERM_DISPATCHER")))
	return v == "1" || v == "true" || v == "yes"
}