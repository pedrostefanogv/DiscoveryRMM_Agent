//go:build windows

package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	xencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

// Shell representa um terminal interativo (cmd.exe ou powershell.exe).
type Shell struct {
	cmd    *exec.Cmd
	shell  string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	childPid uint32 // PID do processo filho (para AttachConsole em VT/resize)
	cols     int
	rows     int

	mu       sync.Mutex
	closed   bool
	onOutput func(string) // callback para saida
}

// NewShell cria um novo shell interativo (console real oculto, via pipes).
// Este é o caminho LEGADO/fallback usado quando ConPTY se mostra instável
// (ex.: injetor/AV mata o processo ConPTY com 0xC0000142). Usa
// CREATE_NEW_CONSOLE (console real) + HideWindow, semelhante ao terminal
// legado do MeshCentral — mais resistente a injetores/AV do que ConPTY.
// shell: "powershell" ou "cmd".
func NewShell(shell string, onOutput func(string)) (*Shell, error) {
	resolvedKind, _ := ResolveShell(ShellKind(shell))
	shell = string(resolvedKind)

	var cmd *exec.Cmd
	switch resolvedKind {
	case ShellPowerShell:
		// Wrapper UTF-8 no spawn legacy (bug 2026-09-20): sem ele, o texto do
		// PS (erros de parse, cmdlets) saía na code page do console oculto
		// (OEM/ANSI) com best-fit destrutivo (ã→Æ em CP437) e o typed input
		// acentuado era decodificado com a OEM CP → mojibake nos dois lados.
		// [Console]::Output/InputEncoding=UTF8 força o próprio PS a falar
		// UTF-8 na pipe; chcp 65001 não afeta NATIVOS em pipe (ping continua
		// OEM — coberto pela decodificação OEM em normalizeToUtf8).
		cmd = exec.Command("powershell.exe", "-NoLogo", "-NoExit", "-Command",
			"chcp 65001 >$null; [Console]::OutputEncoding=[System.Text.Encoding]::UTF8; [Console]::InputEncoding=[System.Text.Encoding]::UTF8")
	default:
		// cmd: chcp 65001 faz o próprio cmd (echo/prompt/erro) emitir UTF-8.
		cmd = exec.Command("cmd.exe", "/k", "chcp 65001>nul")
	}

	// Configura pipes
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_CONSOLE, // pseudo-terminal behavior
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdoutPipe.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdoutPipe.Close()
		stderrPipe.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Shell{
		cmd:      cmd,
		shell:    shell,
		stdin:    stdin,
		stdout:   stdoutPipe,
		stderr:   stderrPipe,
		onOutput: onOutput,
	}
	if cmd.Process != nil {
		s.childPid = uint32(cmd.Process.Pid)
	}

	// Leitura assincrona da saida
	go s.readLoop(stdoutPipe)
	go s.readLoop(stderrPipe)

	// Habilita VT/ANSI no console real do filho (apos o start) para que
	// cores/sequencias ANSI sejam processadas sempre que o host suportar.
	// O resize inicial é feito pelo chamador (NewLegacyShell → s.Resize).
	enableVtOnChildConsole(s.childPid)

	return s, nil
}

// ShellKind retorna o tipo de shell em uso (conformidade com IShell).
func (s *Shell) ShellKind() ShellKind {
	return ShellKind(s.shell)
}

// Alive reporta se o processo shell ainda está em execução (conformidade com
// IShell), sem consumir o Wait().
func (s *Shell) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return processAlive(s.cmd.Process.Pid)
}

// Wait aguarda o processo do shell terminar (conformidade com IShell).
// Útil para o gerenciador de sessão detectar falha prematura (ex.:
// 0xC0000142 quando um injetor/AV mata o processo no DllMain).
func (s *Shell) Wait() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("processo nao inicializado")
	}
	return s.cmd.Wait()
}

func (s *Shell) readLoop(r io.Reader) {
	buf := make([]byte, 4096)
	// carry: bytes retidos entre reads quando o chunk termina no meio de uma
	// possível runa UTF-8 incompleta. Sem isso, a metade inicial de "í"
	// (0xC3) era normalizada isoladamente no limite do read (utf8.Valid false
	// → decodificava como CP1252 → mojibake intermitente em pt-BR). O tail
	// incompleto é unido ao próximo chunk ANTES da normalização.
	var carry []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			tail := Utf8IncompleteTail(carry)
			switch {
			case tail > 0 && tail < len(carry):
				// Despacha o prefixo válido; retém o tail para o próximo read.
				keep := append([]byte(nil), carry[len(carry)-tail:]...)
				s.emitChunk(carry[:len(carry)-tail])
				carry = keep
			case tail > 0 && tail == len(carry) && err == nil:
				// Todo o carry é tail incompleto — aguarda o próximo read.
			default:
				s.emitChunk(carry)
				carry = nil
			}
		}
		if err != nil {
			// Fim do stream: flush do que ficou retido.
			if len(carry) > 0 {
				s.emitChunk(carry)
			}
			return
		}
	}
}

// emitChunk normaliza o chunk para UTF-8 e entrega ao callback da sessão.
func (s *Shell) emitChunk(chunk []byte) {
	output := normalizeToUtf8(chunk)
	s.mu.Lock()
	if s.onOutput != nil && !s.closed {
		s.onOutput(output)
	}
	s.mu.Unlock()
}

// normalizeToUtf8 garante que a saída lida da pipe esteja em UTF-8 antes de
// seguir para o pipeline (base64 → frontend TextDecoder('utf-8')).
//
// No backend legacy (CREATE_NEW_CONSOLE + pipes), o stdout/stderr do processo
// filho é redirecionado para um pipe. Nativos Windows (CRT) que escrevem em
// uma pipe usam a code page ANSI do sistema (CP1252 em pt-BR) — NÃO UTF-8.
// Sem intervenção, bytes como 0xED ("í" em CP1252) viram U+FFFD (”)
// quando interpretados como UTF-8, quebrando os acentos ("Estatísticas" →
// "Estat�sticas").
//
// Se o trecho já for UTF-8 válido (ex.: ConPTY normaliza para UTF-8, ou o
// shell já emite UTF-8 após o wrapper de spawn), usamos direto; caso
// contrário, decodificamos com a OEM code page real do sistema e, em último
// caso, Windows-1252.
//
// POR QUÊ OEM (bug 2026-09-20 — mojibake "M¡nimo"/"n£mero"/"M‚dia" no ping):
// nativos Windows (ping.exe etc.) escrevem em pipe usando a OEM code page
// (GetOEMCP; CP437/CP850 no pt-BR/latino), NÃO a ANSI CP1252 — e "chcp 65001"
// NÃO muda isso (provado com harness: ping emite \xa1=í, \xa3=ú, \xa0=á,
// \x82=é mesmo após chcp 65001). Decodificar esses bytes como CP1252 produzia
// exatamente o mojibake reportado. A codepage correta é a do GetOEMCP().
// Resíduo conhecido: quando a OEM CP é 437 (sem ã/õ), o Windows best-fit
// converte ã→Æ no EMISSOR (perda irreversível) — a correção estrutural para o
// texto do PS é o wrapper UTF-8 do spawn (abaixo), não a decodificação.
func normalizeToUtf8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if dec := oemDecoder(); dec != nil {
		if decoded, err := dec.String(string(b)); err == nil {
			return decoded
		}
	}
	// Último recurso: CP1252 (ANSI) — cobre shells que emitem ANSI.
	decoded, _ := charmap.Windows1252.NewDecoder().String(string(b))
	return decoded
}

// procGetOEMCP: kernel32!GetOEMCP — OEM code page do sistema (a usada por
// nativos escrevendo em pipe).
var procGetOEMCP = kernel32.NewProc("GetOEMCP")

// systemOEMCodePage retorna a OEM code page do sistema (0 em caso de falha).
func systemOEMCodePage() uint32 {
	r1, _, _ := procGetOEMCP.Call()
	return uint32(r1)
}

// oemDecoder devolve o decoder para a OEM code page do sistema; nil se a CP
// não tiver tabela no x/text (aí o chamador cai no fallback CP1252).
func oemDecoder() *xencoding.Decoder {
	dec, ok := oemDecoders[systemOEMCodePage()]
	if !ok {
		return nil
	}
	return dec
}

// oemDecoders mapeia as OEM code pages de fato suportadas pelo x/text/charmap.
var oemDecoders = map[uint32]*xencoding.Decoder{
	437: charmap.CodePage437.NewDecoder(),
	850: charmap.CodePage850.NewDecoder(),
	852: charmap.CodePage852.NewDecoder(),
	855: charmap.CodePage855.NewDecoder(),
	858: charmap.CodePage858.NewDecoder(),
	860: charmap.CodePage860.NewDecoder(),
	862: charmap.CodePage862.NewDecoder(),
	863: charmap.CodePage863.NewDecoder(),
	865: charmap.CodePage865.NewDecoder(),
	866: charmap.CodePage866.NewDecoder(),
}

// WriteStdin escreve dados no stdin do shell.
//
// NOTA: no backend legacy (`CREATE_NEW_CONSOLE` + pipes), o stdin do processo
// filho é uma PIPE (cmd.StdinPipe). O processo filho (powershell/cmd) lê o
// input DESSA pipe — e a "edição de linha" (Backspace/Delete) só é feita pela
// API de console quando o input chega via console input, o que não acontece
// aqui. Escrevemos, portanto, na pipe normalmente.
//
// A correção REAL do Backspace/Delete vem do **ConPTY** (in-process ou via
// dispatcher F4), que injeta o input no console de forma nativa. Não usamos
// `WriteConsoleInput` no legacy porque o filho (stdin=pipe) não consome o
// console input — seria uma falsa correção. Ver também console_input_windows.go.
func (s *Shell) WriteStdin(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

// Resize redimensiona o console real do processo filho (backend legacy) para
// as dimensoes cols×rows. Como o ConPTY não está envolvido, usamos
// AttachConsole + SetConsoleScreenBufferSize/SetConsoleWindowInfo no console
// real do filho. É o equivalente a um resize real — corrige quebra de linha e
// alinhamento com o xterm.js.
func (s *Shell) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensoes invalidas: %dx%d", cols, rows)
	}
	if s.childPid == 0 {
		return nil // sem PID (ex.: shell não iniciado) — no-op seguro
	}
	err := resizeChildConsole(s.childPid, cols, rows)
	if err == nil {
		s.cols = cols
		s.rows = rows
	}
	return err
}

// Close encerra o shell.
func (s *Shell) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stdin.Close()
	s.cmd.Process.Kill()
	s.cmd.Wait()
	return nil
}

// Ensure imports
var _ io.ReadCloser
var _ context.Context
var _ os.File
var _ exec.Cmd
var _ = windows.CREATE_NEW_CONSOLE
