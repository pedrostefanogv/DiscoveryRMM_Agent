//go:build windows

package terminal

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ── Fila de saída do dispatcher (backpressure T6) ──

const (
	// Limites da fila ConPTY → named pipe. Ao encher, os chunks são
	// coalescidos; o descarte só acontece como último recurso.
	pipeOutQueueItems = 256
	pipeOutQueueBytes = 1 << 20 // 1MB
	pipeWriteTimeout  = 2 * time.Second

	// Teto do buffer de input pendente (bytes recebidos SEM '\n' que fecha a
	// linha base64). Um cliente que nunca envia linha completa não pode fazer
	// o dispatcher crescer sem limite — acima do teto, desconecta.
	pipeInPendingMax = 1 << 20 // 1MB
)

// bufferedWriter desacopla o callback de output do ConPTY da escrita no named
// pipe (que pode BLOQUEAR sem deadline e travar a sessão inteira). O callback
// apenas enfileira/coalesce e sinaliza; a escrita real acontece numa goroutine
// dedicada com deadline curto.
type bufferedWriter struct {
	conn         net.Conn
	writeTimeout time.Duration

	mu       sync.Mutex
	chunks   [][]byte
	bytes    int
	maxItems int
	maxBytes int
	closed   bool
	dropped  int64

	notify  chan struct{}
	done    chan struct{}
	drainMu sync.Mutex
}

func newBufferedWriter(conn net.Conn, maxItems, maxBytes int) *bufferedWriter {
	w := &bufferedWriter{
		conn:         conn,
		writeTimeout: pipeWriteTimeout,
		maxItems:     maxItems,
		maxBytes:     maxBytes,
		notify:       make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	go w.loop()
	go w.metricsLoop()
	return w
}

// metricsLoop publica periodicamente os bytes descartados para o agente (R8).
func (w *bufferedWriter) metricsLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			w.logDroppedIfAny()
		}
	}
}

// logDroppedIfAny emite a métrica apenas quando houve descarte.
func (w *bufferedWriter) logDroppedIfAny() {
	w.mu.Lock()
	d := w.dropped
	w.mu.Unlock()
	if d > 0 {
		log.Printf("[dispatcher-metrics] dropped=%d", d)
	}
}

// Write enfileira o chunk. NUNCA bloqueia: no máximo coalesce no último chunk
// e, se o teto duro for excedido, descarta os chunks antigos (com contador).
func (w *bufferedWriter) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	w.mu.Lock()
	if w.closed {
		// Escrita apos o fechamento (callback tardio do ConPTY): contabiliza
		// para a telemetria nao mentir sobre bytes perdidos.
		w.dropped += int64(len(p))
		w.mu.Unlock()
		return
	}
	if len(w.chunks) >= w.maxItems || w.bytes+len(p) > w.maxBytes {
		if n := len(w.chunks); n > 0 {
			w.chunks[n-1] = append(w.chunks[n-1], p...)
		} else {
			w.chunks = append(w.chunks, append([]byte(nil), p...))
		}
		w.bytes += len(p)
		w.trimLocked()
	} else {
		w.chunks = append(w.chunks, append([]byte(nil), p...))
		w.bytes += len(p)
	}
	w.mu.Unlock()
	w.signal()
}

// trimLocked descarta os bytes mais antigos enquanto exceder o teto. Primeiro
// remove chunks inteiros; se restar UM chunk ainda acima do teto (caso em que
// o coalesce acumulou tudo nele), descarta a parte mais antiga dele — sem isso
// a fila "bounded" cresceria sem limite. Chamador segura w.mu.
func (w *bufferedWriter) trimLocked() {
	for len(w.chunks) > 1 && w.bytes > w.maxBytes {
		w.bytes -= len(w.chunks[0])
		w.dropped += int64(len(w.chunks[0]))
		w.chunks = w.chunks[1:]
	}
	if len(w.chunks) == 1 && w.bytes > w.maxBytes {
		excess := w.bytes - w.maxBytes
		if excess > len(w.chunks[0]) {
			excess = len(w.chunks[0])
		}
		w.chunks[0] = append([]byte(nil), w.chunks[0][excess:]...)
		w.bytes -= excess
		w.dropped += int64(excess)
	}
}

func (w *bufferedWriter) signal() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *bufferedWriter) loop() {
	for {
		select {
		case <-w.done:
			return
		case <-w.notify:
			w.drain()
		}
	}
}

// drain escreve os chunks pendentes com deadline curto. Serializado por
// drainMu para o flush final (Close) não competir com o loop.
func (w *bufferedWriter) drain() {
	w.drainMu.Lock()
	defer w.drainMu.Unlock()
	for {
		w.mu.Lock()
		if len(w.chunks) == 0 {
			w.mu.Unlock()
			return
		}
		chunk := w.chunks[0]
		w.chunks = w.chunks[1:]
		w.bytes -= len(chunk)
		w.mu.Unlock()

		_ = w.conn.SetWriteDeadline(time.Now().Add(w.writeTimeout))
		if _, err := w.conn.Write(chunk); err != nil {
			// Telemetria: bytes que não chegaram ao pipe (sem retry — reenfileirar
			// poderia inverter a ordem ou crescer sem limite).
			w.mu.Lock()
			w.dropped += int64(len(chunk))
			w.mu.Unlock()
			log.Printf("[dispatcher] pipe out write falhou (%d bytes perdidos): %v", len(chunk), err)
			return
		}
	}
}

// Close interrompe o loop e esvazia o que restou (flush no encerramento do
// shell, antes de fechar o pipe).
func (w *bufferedWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.done)
	w.drain()
	// Métrica final (o agente a converte em .stats).
	w.logDroppedIfAny()
}

// Dropped retorna os bytes descartados por overflow (telemetria).
func (w *bufferedWriter) Dropped() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

// ── Dispatcher do terminal — executa o ConPTY num processo filho ──
//
// Contexto: o ConPTY rodado no processo principal do agente (Wails GUI)
// é suscetível ao crash 0xC0000142 (STATUS_DLL_INIT_FAILED) durante o boot por
// injeção de DLL de AV/injetores. A solução é rodar o terminal num processo
// filho isolado (dispatcher).
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
// injeção de DLL), o dispatcher isola o ConPTY num processo filho,
// mantendo TUI/setas/história funcionando.
// Opt-out explícito: DISCOVERY_TERM_DISPATCHER=0/false/no volta ao comportamento
// antigo (ConPTY in-process → legacy). Opt-in continua aceito (1/true/yes).

// dispatcherPipeSDDL monta o descritor de segurança dos named pipes do
// dispatcher: SYSTEM (GA) + o usuário do PRÓPRIO processo (o dispatcher é um
// filho do binário do agente, então o usuário é o mesmo do pai). Sem SDDL
// explícito, o default do winio não restringe quem conecta: qualquer processo
// da máquina poderia abrir o pipe e injetar input no shell do usuário (ou ler
// o output — keylog de terminal). Mesma política do canal IPC
// (ipc_windows.go: SDDL explícito obrigatório, revisão 2026-09-04).
func dispatcherPipeSDDL() string {
	sddl := "D:(A;;GA;;;SY)"
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err == nil {
		if user, uerr := token.GetTokenUser(); uerr == nil {
			sddl += "(A;;GA;;;" + user.User.Sid.String() + ")"
		}
		_ = token.Close()
	}
	return sddl
}

// validSessionToken verifica o charset do token de sessão recebido na command
// line. O token vira parte do NOME do pipe (\\.\pipe\discovery-term-<session>-*):
// aceitar qualquer charset permitiria injetar namespace/caminho no nome
// (segmento com '\\' apontando para o pipe de OUTRA sessão). Aceita apenas o
// formato UUID: ^[0-9a-fA-F-]+$.
func validSessionToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F', c == '-':
		default:
			return false
		}
	}
	return true
}

// pipeClientPID devolve o PID do processo cliente conectado no named pipe
// (GetNamedPipeClientProcessId). 0 quando não é possível determinar (conexão
// sem handle de pipe, ex.: net.Pipe em teste).
func pipeClientPID(conn net.Conn) uint32 {
	fdConn, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fdConn.Fd()), &pid); err != nil {
		return 0
	}
	return pid
}

// RunDispatcher executa o modo dispatcher e bloqueia até o shell encerrar.
// É chamado do main.go quando "--terminal-dispatcher" está presente nos args.
func RunDispatcher() {
	args := os.Args
	var session, shellKind, runAsFlag string
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
		case strings.HasPrefix(a, "--terminal-runas="):
			runAsFlag = strings.TrimPrefix(a, "--terminal-runas=")
		}
	}
	if session == "" || shellKind == "" {
		log.Printf("[term-dispatcher] args invalidos: session vazio ou shell vazio (%v)", args[1:])
		os.Exit(1)
	}
	// Run-as: valor invalido NUNCA derruba o dispatcher — usa identidade herdada.
	if runAsFlag != "" && !ValidRunAs(runAsFlag) {
		log.Printf("[term-dispatcher] runas invalido %q — usando identidade herdada", runAsFlag)
		runAsFlag = ""
	}
	runAs := ParseRunAs(runAsFlag)
	// Segurança: o token da sessão vira parte do NOME do pipe — só o charset
	// UUID (hex/hífen) é aceito; qualquer outro caractere permitiria injetar
	// namespace/caminho no nome do pipe (ver validSessionToken).
	if !validSessionToken(session) {
		log.Printf("[term-dispatcher] token de sessao invalido (charset fora de ^[0-9a-fA-F-]+$)")
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
	// SDDL explícito: sem isso o default do winio deixa o pipe aberto a
	// qualquer processo local (injeção de input/leitura de output).
	sddl := dispatcherPipeSDDL()
	outL, err := winio.ListenPipe(outPipe, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		log.Printf("[dispatcher] ListenPipe(out): %v", err)
		os.Exit(1)
	}
	defer outL.Close()
	inL, err := winio.ListenPipe(inPipe, &winio.PipeConfig{SecurityDescriptor: sddl})
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

	// Valida o chamador conectado: só o processo pai (o agente que lançou este
	// dispatcher) pode se conectar. O SDDL já restringe por usuário; aqui
	// fechamos a janela de outro processo DO MESMO usuário. Se o PID não puder
	// ser determinado (handle indisponível), o SDDL permanece como defesa.
	if ppid := parentPID(); ppid != 0 {
		for name, c := range map[string]net.Conn{"in": inConn, "out": outConn} {
			if pid := pipeClientPID(c); pid != 0 && pid != ppid {
				log.Printf("[dispatcher] cliente do pipe %s não é o agente pai (pid=%d, esperado=%d) — recusando", name, pid, ppid)
				os.Exit(1)
			}
		}
	}

	// Cria o ConPTY + shell com retry por morte prematura — a MESMA política
	// do caminho in-process (iserial_windows.go): o 0xC0000142 do AV/injetor
	// também mata o ConPTY DENTRO do dispatcher (o isolamento reduz, não
	// elimina). Antes: uma única tentativa — falhou, exit(1), e o agente não
	// tinha fallback (terminal morto em silêncio). onOutput → pipe outConn.
	// firstOutput sinaliza saída já produzida: a sonda de startup sai cedo
	// (produzir output = ConPTY saudável), em vez de esperar o timeout inteiro.
	// T6: o callback do ConPTY apenas enfileira (nunca bloqueia); a escrita no
	// named pipe fica numa goroutine dedicada com deadline curto.
	outWriter := newBufferedWriter(outConn, pipeOutQueueItems, pipeOutQueueBytes)
	defer outWriter.Close()

	var firstOutput atomic.Bool
	newShell := func() (IShell, error) {
		return NewConPTYShellRunAs(ShellKind(shellKind), cols, rows, func(output string) {
			firstOutput.Store(true)
			outWriter.Write([]byte(output))
		}, runAs)
	}
	var ish IShell
	var lastErr error
	for attempt := 0; attempt <= dispatcherInnerRetries; attempt++ {
		cand, cerr := newShell()
		if cerr != nil {
			lastErr = cerr
			log.Printf("[dispatcher] NewConPTYShell falhou (tentativa %d/%d): %v", attempt+1, dispatcherInnerRetries+1, cerr)
		} else if probeForEarlyDeath(cand, conptyProbeTimeout, &firstOutput) {
			lastErr = fmt.Errorf("ConPTY morreu prematuramente no startup")
			if code, ok := cand.(interface{ ExitCodeNow() (int, bool) }); ok {
				if c, ok2 := code.ExitCodeNow(); ok2 {
					log.Printf("[dispatcher] ConPTY morreu prematuramente (tentativa %d/%d) exitCode=0x%08X; tentando novamente",
						attempt+1, dispatcherInnerRetries+1, uint32(c))
				} else {
					log.Printf("[dispatcher] ConPTY morreu prematuramente (tentativa %d/%d); tentando novamente",
						attempt+1, dispatcherInnerRetries+1)
				}
			} else {
				log.Printf("[dispatcher] ConPTY morreu prematuramente (tentativa %d/%d); tentando novamente",
					attempt+1, dispatcherInnerRetries+1)
			}
			_ = cand.Close()
		} else {
			ish = cand
			break
		}
		// Backoff progressivo (150ms → 1s): rajada de spawns aciona regras
		// comportamentais de AV/EDR (diagnóstico 2026-10-26).
		if attempt < dispatcherInnerRetries {
			backoff := conptyRetryBackoff * time.Duration(attempt+1)
			if backoff > time.Second {
				backoff = time.Second
			}
			time.Sleep(backoff)
		}
	}
	if ish == nil {
		log.Printf("[dispatcher] ConPTY instável após %d tentativas (último: %v)", dispatcherInnerRetries+1, lastErr)
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
		// T6: drena a fila ANTES de fechar o pipe (o shell pode ter produzido
		// output final que ainda não foi escrito).
		outWriter.Close()
		log.Printf("[dispatcher] shell encerrado — fila drenada e pipes fechados (notifica cliente via EOF)")
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
			if len(pending) > pipeInPendingMax {
				// Overflow do buffer de input: nenhum '\n' chegou — cliente
				// mal-comportado (ou hostil). Desconecta em vez de crescer
				// sem limite.
				log.Printf("[dispatcher] buffer de input excedeu %d bytes sem linha completa — desconectando", pipeInPendingMax)
				break
			}
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
