//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"sync"
)

// Bloqueio de entrada do host remoto (KVM input lock).
//
// BlockInput (user32) bloqueia teclado e mouse do DESKTOP atual. Requer
// processo elevado — o worker de acesso remoto roda como SYSTEM/High na sessão
// interativa, então atende ao requisito.
//
// IMPORTANTE (thread): a operação é executada numa goroutine PINADA a uma OS
// thread (runtime.LockOSThread). O Windows libera o bloqueio na saída da thread
// que o aplicou; se block e unblock rodassem em threads diferentes do pool do
// Go, a liberação poderia não valer. A mesma thread faz os dois — e, se o
// processo morrer, a thread morre junto e o Windows libera a entrada.
var (
	procBlockInput = user32DLL.NewProc("BlockInput")

	inputBlockOps  = make(chan func())
	inputBlockOnce sync.Once
)

// inputBlockThread garante a goroutine pinada que executa block/unblock.
func inputBlockThread() {
	inputBlockOnce.Do(func() {
		go func() {
			runtime.LockOSThread()
			for op := range inputBlockOps {
				op()
			}
		}()
	})
}

// runOnInputBlockThread executa fn na thread pinada e devolve o erro produzido.
func runOnInputBlockThread(fn func() error) error {
	inputBlockThread()
	done := make(chan error, 1)
	inputBlockOps <- func() { done <- fn() }
	return <-done
}

// BlockInputSystem bloqueia o teclado/mouse do desktop e devolve o método usado.
//
// Falhas comuns (retorno FALSE): processo sem elevação suficiente; entrada já
// bloqueada por outro processo (outro RMM/protetor de tela). O erro descreve o
// caso para o operador, em vez de marcar um bloqueio que não existe.
func BlockInputSystem() (string, error) {
	if err := procBlockInput.Find(); err != nil {
		return "", fmt.Errorf("BlockInput indisponível: %v", err)
	}
	err := runOnInputBlockThread(func() error {
		ret, _, callErr := procBlockInput.Call(1) // TRUE = bloquear
		if ret == 0 {
			return fmt.Errorf("BlockInput(TRUE) recusado: %v (errno=%d) — processo sem elevação ou entrada já bloqueada por outro processo",
				describeErr(callErr), errnoOf(callErr))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "blockinput", nil
}

// UnblockInputSystem libera a entrada bloqueada por BlockInputSystem. Deve ser
// chamada em TODO caminho de encerramento da sessão/worker.
func UnblockInputSystem() error {
	if err := procBlockInput.Find(); err != nil {
		return fmt.Errorf("BlockInput indisponível: %v", err)
	}
	return runOnInputBlockThread(func() error {
		ret, _, callErr := procBlockInput.Call(0) // FALSE = liberar
		if ret == 0 {
			return fmt.Errorf("BlockInput(FALSE) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
		}
		return nil
	})
}
