//go:build windows && (amd64 || arm64)

package screen

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Guarda de layout: o callback do hook lê KBDLLHOOKSTRUCT/MSLLHOOKSTRUCT
// diretamente da memória apontada pelo lParam. Offset/ tamanho errados fariam
// a leitura da flag INJECTED pegar lixo — engolindo o SendInput do operador
// (exatamente o bug de campo que motivou trocar BlockInput por hook).
func TestHookStructLayouts(t *testing.T) {
	if got := unsafe.Sizeof(hookMSG{}); got != 48 {
		t.Fatalf("MSG com %d bytes, esperado 48", got)
	}
	if got := unsafe.Sizeof(kbDllHookStruct{}); got != 24 {
		t.Fatalf("KBDLLHOOKSTRUCT com %d bytes, esperado 24", got)
	}
	if got := unsafe.Sizeof(msDllHookStruct{}); got != 32 {
		t.Fatalf("MSLLHOOKSTRUCT com %d bytes, esperado 32", got)
	}

	// Offsets das flags: é AQUI que o callback lê o bit INJECTED. Offset errado
	// faria a leitura pegar lixo e engolir o SendInput do operador.
	if got := unsafe.Offsetof(kbDllHookStruct{}.flags); got != 8 {
		t.Fatalf("KBDLLHOOKSTRUCT.flags em offset %d, esperado 8", got)
	}
	if got := unsafe.Offsetof(msDllHookStruct{}.flags); got != 12 {
		t.Fatalf("MSLLHOOKSTRUCT.flags em offset %d, esperado 12", got)
	}
}

// O runtime precisa aceitar a assinatura dos callbacks (lParam como
// unsafe.Pointer). Compilar o callback NÃO instala hook nenhum — é apenas o
// registro da função no runtime.
func TestHookCallbacksAreRegistrable(t *testing.T) {
	if ptr := windows.NewCallback(keyboardHookProc); ptr == 0 {
		t.Fatal("callback de teclado nao registrou")
	}
	if ptr := windows.NewCallback(mouseHookProc); ptr == 0 {
		t.Fatal("callback de mouse nao registrou")
	}
}

// Integração: instala e remove os hooks de verdade para validar o caminho que
// falhou em campo (module handle, ids de hook, registro do callback). O filtro
// fica DESLIGADO (inputHookSwallow=false), então nada do usuário é engolido.
func TestHookInstallAndUnhook(t *testing.T) {
	if err := procSetWindowsHookExW.Find(); err != nil {
		t.Skipf("SetWindowsHookExW indisponivel: %v", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hMod, _, _ := procGetModuleHandleW.Call(0)
	kbProc, msProc := hookProcPointers()

	kb, _, kbErr := procSetWindowsHookExW.Call(whKeyboardLL, kbProc, hMod, 0)
	if kb == 0 {
		t.Fatalf("hook de teclado nao instalou: %v (errno=%d)", describeErr(kbErr), errnoOf(kbErr))
	}
	defer procUnhookWindowsHookEx.Call(kb)

	ms, _, msErr := procSetWindowsHookExW.Call(whMouseLL, msProc, hMod, 0)
	if ms == 0 {
		t.Fatalf("hook de mouse nao instalou: %v (errno=%d)", describeErr(msErr), errnoOf(msErr))
	}
	defer procUnhookWindowsHookEx.Call(ms)

	if inputHookSwallow.Load() {
		t.Fatal("o teste nao pode engolir input do usuario")
	}
}

// O filtro precisa deixar passar TUDO que é injetado (SendInput do operador) e
// engolir apenas o físico enquanto o bloqueio está ativo.
func TestHookFilterInjectedPassthrough(t *testing.T) {
	if !shouldSwallowKeyboard(true, 0) {
		t.Fatal("físico deveria ser engolido com bloqueio ativo")
	}
	if shouldSwallowKeyboard(true, llkhfInjected) {
		t.Fatal("evento injetado (SendInput) NÃO pode ser engolido")
	}
	if !shouldSwallowMouse(true, 0) {
		t.Fatal("mouse físico deveria ser engolido com bloqueio ativo")
	}
	if shouldSwallowMouse(true, llmhfInjected) {
		t.Fatal("mouse injetado (SendInput) NÃO pode ser engolido")
	}
	if shouldSwallowKeyboard(false, 0) || shouldSwallowMouse(false, 0) {
		t.Fatal("sem bloqueio ativo nada pode ser engolido")
	}
}
