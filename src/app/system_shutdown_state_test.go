package app

import (
	"testing"
	"time"
)

// O SCM notifica PreShutdown/Shutdown poucos segundos depois do RestartPrompt
// disparar o reboot. O caminho de power action usa esse sinal para não tratar o
// reboot como falha do PSADT (falso negativo exit=1 com a máquina reiniciando).
func TestSystemShutdownFlagAndWait(t *testing.T) {
	resetSystemShutdownForTest()
	defer resetSystemShutdownForTest()

	if SystemShutdownInProgress() {
		t.Fatal("flag deveria iniciar limpo")
	}
	if waitForSystemShutdown(0) {
		t.Fatal("timeout zero não pode reportar encerramento")
	}

	// O sinal chega durante a espera.
	go func() {
		time.Sleep(150 * time.Millisecond)
		noteSystemShutdown()
	}()

	start := time.Now()
	if !waitForSystemShutdown(5 * time.Second) {
		t.Fatal("esperava detectar o encerramento em curso")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("espera longa demais: %s", elapsed)
	}

	// Depois do sinal o flag fica fixado (o processo morre no encerramento).
	if !waitForSystemShutdown(time.Second) {
		t.Fatal("flag fixado deveria reportar encerramento imediatamente")
	}
}

func TestWaitForSystemShutdownTimeoutSemSinal(t *testing.T) {
	resetSystemShutdownForTest()
	defer resetSystemShutdownForTest()

	start := time.Now()
	if waitForSystemShutdown(600 * time.Millisecond) {
		t.Fatal("sem sinal do SCM não pode reportar encerramento")
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("espera curta demais para o timeout: %s", elapsed)
	}
}
