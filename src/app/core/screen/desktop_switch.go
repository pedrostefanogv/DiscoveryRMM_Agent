//go:build windows

package screen

// Desktop switch detection (base: MeshAgent kvm.c CheckDesktopSwitch).
//
// Quando o desktop ativo muda (tela de logon, lock screen, prompt UAC/secure
// desktop), a captura GDI/DXGI pode continuar retornando o desktop antigo ou
// falhar. O padrão do MeshAgent é: a cada iteração do loop de captura, abrir o
// input desktop (OpenInputDesktop), anexar o thread a ele (SetThreadDesktop) e
// comparar o nome do desktop (GetUserObjectInformationA). Se mudou → o caller
// força um refresh (novo frame completo).

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procOpenInputDesktop          = user32DLL.NewProc("OpenInputDesktop")
	procCloseDesktop              = user32DLL.NewProc("CloseDesktop")
	procSetThreadDesktop          = user32DLL.NewProc("SetThreadDesktop")
	procGetUserObjectInformationW = user32DLL.NewProc("GetUserObjectInformationW")
)

const (
	UOI_NAME = 2
)

// threadDesktopNames guarda o último desktop anexado POR GOROUTINE. O
// SetThreadDesktop é uma propriedade do THREAD do Windows — chamadas de
// goroutines diferentes (loop de captura, handler de input) anexam threads
// distintos, e cada um precisa comparar com o SEU estado anterior. Um
// estado global compartilhado causaria falsos "switches" entre threads
// (A registra "Default", B compara e acha que mudou).
var threadDesktopNames sync.Map // map[goroutine-key]string

// CheckDesktopSwitch anexa o thread atual ao input desktop ativo e retorna
// true quando o desktop mudou desde a última chamada DESTE GOROUTINE (ex.:
// usuário travou a tela, UAC apareceu, voltou para o desktop Default). O
// estado é por-goroutine porque SetThreadDesktop afeta o thread Windows
// subjacente — captura e input rodam em goroutines/threads distintos.
//
// Retorna (switched, err). Em caso de erro, o caller deve continuar com o
// comportamento anterior (não é fatal — ex.: serviços em sessão 0 sem desktop
// interativo falham no OpenInputDesktop).
func CheckDesktopSwitch() (bool, error) {
	h, _, callErr := procOpenInputDesktop.Call(
		0, // dwFlags
		0, // fInherit
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.GENERIC_EXECUTE, // dwDesiredAccess
	)
	if h == 0 {
		return false, fmt.Errorf("OpenInputDesktop falhou: %v", callErr)
	}
	defer procCloseDesktop.Call(h)

	// Nome do desktop (WCHAR[]).
	buf := make([]uint16, 256)
	var needed uint32
	ok, _, _ := procGetUserObjectInformationW.Call(
		h,
		UOI_NAME,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&needed)),
	)
	if ok == 0 {
		return false, fmt.Errorf("GetUserObjectInformationW falhou")
	}
	name := windows.UTF16ToString(buf)

	// Anexa o thread ao desktop ativo — necessário para captura e SendInput
	// funcionarem no desktop correto (logon, UAC, Default).
	if ret, _, setErr := procSetThreadDesktop.Call(h); ret == 0 {
		return false, fmt.Errorf("SetThreadDesktop falhou: %v", setErr)
	}

	key := goroutineKey()
	prev, loaded := threadDesktopNames.Load(key)
	threadDesktopNames.Store(key, name)
	if !loaded {
		// Primeira chamada deste goroutine: não é "switch", mas registra.
		return false, nil
	}
	return prev.(string) != name, nil
}

// goroutineKey gera uma chave única por goroutine (via stack do runtime —
// barato e estável durante a vida do goroutine).
func goroutineKey() any {
	return goroutineID()
}

// goroutineID extrai um identificador estável para o goroutine atual a partir
// do stack trace do runtime (técnica estabelecida — usada por libs como
// go-stack e pelo próprio runtime em debug). Não é um ID numérico oficial —
// serve apenas como chave de map por-goroutine.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Linha 0: "goroutine 123 [running]:..."
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// CurrentDesktopName retorna o nome do input desktop ativo (diagnóstico).
func CurrentDesktopName() string {
	h, _, _ := procOpenInputDesktop.Call(0, 0,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.GENERIC_EXECUTE)
	if h == 0 {
		return ""
	}
	defer procCloseDesktop.Call(h)
	buf := make([]uint16, 256)
	var needed uint32
	ok, _, _ := procGetUserObjectInformationW.Call(
		h, UOI_NAME,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&needed)),
	)
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
