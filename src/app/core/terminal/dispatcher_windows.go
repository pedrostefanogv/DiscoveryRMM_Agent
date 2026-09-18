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
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
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
// HABILITADO POR PADRÃO (fix 13/09/2026 — terminal web sem setas/história):
// o fallback legacy (pipes + CREATE_NEW_CONSOLE) NÃO processa sequências VT —
// setas ↑/↓ (histórico do PSReadLine), Home/End/Delete e TAB-completação não
// funcionam nele; só texto cru. O ConPTY é quem traduz \x1b[A em KEY_EVENT.
// Como o ConPTY in-process morre intermitentemente com AV/EDR (0xC0000142,
// injeção de DLL), o dispatcher isola o ConPTY num processo filho — o mesmo
// padrão do MeshCentral — mantendo TUI/setas/história funcionando.
// Opt-out explícito: DISCOVERY_TERM_DISPATCHER=0/false/no volta ao comportamento
// antigo (ConPTY in-process → legacy). Opt-in continua aceito (1/true/yes).

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

	// B23: monitor de morte do pai — sem isso, se o agente crashar ANTES de
	// conectar nos pipes, o dispatcher fica órfão em Accept() eterno, zumbi
	// segurando os named pipes da sessão (a cada crash do agente). Detecta o
	// PID do pai via Toolhelp32 e encerra o processo quando o handle sinalizar.
	startParentDeathMonitor()

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

	// Cria o ConPTY + shell com retry por morte prematura — a MESMA política
	// do caminho in-process (iserial_windows.go): o 0xC0000142 do AV/injetor
	// também mata o ConPTY DENTRO do dispatcher (o isolamento reduz, não
	// elimina). Antes: uma única tentativa — falhou, exit(1), e o agente não
	// tinha fallback (terminal morto em silêncio). onOutput → pipe outConn.
	newShell := func() (IShell, error) {
		return NewConPTYShell(ShellKind(shellKind), cols, rows, func(output string) {
			_, _ = outConn.Write([]byte(output))
		})
	}
	var ish IShell
	var lastErr error
	for attempt := 0; attempt <= conptyMaxRetries; attempt++ {
		cand, cerr := newShell()
		if cerr != nil {
			lastErr = cerr
			log.Printf("[dispatcher] NewConPTYShell falhou (tentativa %d/%d): %v", attempt+1, conptyMaxRetries+1, cerr)
			continue
		}
		if dead := probeForEarlyDeath(cand, conptyProbeTimeout); dead {
			lastErr = fmt.Errorf("ConPTY morreu prematuramente no startup")
			log.Printf("[dispatcher] ConPTY morreu prematuramente (tentativa %d/%d); %s",
				attempt+1, conptyMaxRetries+1, retryLabel(attempt))
			_ = cand.Close()
			continue
		}
		ish = cand
		break
	}
	if ish == nil {
		log.Printf("[dispatcher] ConPTY instável após %d tentativas (último: %v)", conptyMaxRetries+1, lastErr)
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

	// FIX zumbi silencioso: se o shell morrer com o cliente calado (nenhum
	// input chega), o loop principal fica PRESO em inConn.Read e o canal
	// done só é consultado depois de um Read retornar — o dispatcher
	// continuava VIVO com o shell morto: o cliente via processo vivo
	// (Alive()=true), pipe aberta, nenhum byte de output e nenhum EOF — o
	// viewer ficava mudo até o stop manual ('terminal não funciona', só o
	// banner na tela). Fechando as conexões aqui, o Read pendente
	// desbloqueia com erro, o loop sai e o cliente recebe o EOF/Wait.
	go func() {
		<-done
		log.Printf("[dispatcher] shell encerrado — fechando pipes (notifica cliente via EOF)")
		_ = outConn.Close()
		_ = inConn.Close()
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

// DispatchersAvailable reporta se o modo dispatcher está habilitado.
// DEFAULT: habilitado (ConPTY isolado em processo filho — sem isso, quando o
// ConPTY in-process cai no fallback legacy por AV/injetor, o terminal web
// perde setas/história/TAB: o stdin do legacy é uma PIPE sem processamento VT).
// Opt-out explícito: DISCOVERY_TERM_DISPATCHER=0/false/no.
func DispatchersAvailable() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DISCOVERY_TERM_DISPATCHER")))
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ── B23: monitor de morte do processo pai ──────────────────────────────────

// parentPID descobre o PID do processo pai via Toolhelp32Snapshot
// (CreateToolhelp32Snapshot + Process32First/Next). Retorna 0 se não achou.
func parentPID() uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snapshot)

	self := windows.GetCurrentProcessId()
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0
	}
	for {
		if entry.ProcessID == self {
			return entry.ParentProcessID
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return 0
		}
	}
}

// startParentDeathMonitor lança a goroutine que encerra o dispatcher quando
// o processo pai morrer (B23). Best-effort: falha ao abrir o handle não
// bloqueia o dispatcher (comportamento antigo).
func startParentDeathMonitor() {
	ppid := parentPID()
	if ppid == 0 {
		log.Printf("[term-dispatcher] aviso: PID do pai nao identificado — monitor de morte desativado")
		return
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, ppid)
	if err != nil {
		// Pai já saiu ou sem acesso — nada a monitorar.
		log.Printf("[term-dispatcher] pai (pid=%d) indisponivel para monitor: %v — encerrando", ppid, err)
		os.Exit(0)
	}
	go func() {
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		_ = windows.CloseHandle(h)
		log.Printf("[term-dispatcher] processo pai (pid=%d) morreu — dispatcher encerrando (B23)", ppid)
		os.Exit(0)
	}()
}
