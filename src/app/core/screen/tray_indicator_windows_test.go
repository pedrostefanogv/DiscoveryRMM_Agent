//go:build windows && (amd64 || arm64)

package screen

import (
	"testing"
	"unsafe"
)

// Guarda de layout: cbSize errado faz o shell recusar NIM_ADD silenciosamente
// (indicador nunca aparece). Os tamanhos abaixo são os do Windows x64/arm64.
func TestNotifyIconDataLayout(t *testing.T) {
	if got := unsafe.Sizeof(notifyIconDataW{}); got != 976 {
		t.Fatalf("NOTIFYICONDATAW com %d bytes, esperado 976", got)
	}
}

func TestWndClassExLayout(t *testing.T) {
	if got := unsafe.Sizeof(wndClassExW{}); got != 80 {
		t.Fatalf("WNDCLASSEXW com %d bytes, esperado 80", got)
	}
}
