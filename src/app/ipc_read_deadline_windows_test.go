package app

import (
	"os"
	"strings"
	"testing"
	"time"

	winio "github.com/Microsoft/go-winio"
)

// Guarda a premissa da correção de 2026-10-20 (REVISAO §24): o winio honra
// SetReadDeadline em named pipes e a leitura ociosa RETORNA com erro de
// timeout — é o que permite ao readLoop sair sozinho quando o serviço fecha o
// lado dele (o Close() externo não desbloqueia ReadFile no Windows).
func TestWinioReadDeadline_RetornaPorSi(t *testing.T) {
	if os.Getenv("CI") == "true" {
		t.Skip("requer named pipes locais do Windows")
	}
	name := `\\.\pipe\discovery-test-rddeadline-` + time.Now().Format("150405.000000000")
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Skipf("named pipe indisponível neste ambiente: %v", err)
	}
	defer l.Close()

	go func() {
		c, err := l.Accept()
		if err == nil {
			// Servidor fica mudo de propósito: fica BLOQUEADO lendo (o cliente
			// nunca escreve nada) até o teste encerrar — fechar aqui em seguida
			// entregaria EOF e mascararia o deadline.
			defer c.Close()
			var sink [16]byte
			_, _ = c.Read(sink[:])
			// Se o deadline do cliente funcionou, o teste encerra e a conn
			// fecha via defer; se não, este Read fica preso e o teste falha
			// por timeout do próprio go test — que é o cenário que queremos
			// tornar impossível no readLoop real.
		}
	}()

	to := 2 * time.Second
	client, err := winio.DialPipe(name, &to)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline deveria ser suportado: %v", err)
	}
	start := time.Now()
	buf := make([]byte, 16)
	_, err = client.Read(buf)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("leitura deveria falhar por deadline")
	}
	if elapsed > time.Second {
		t.Fatalf("deadline não foi honrado: retorno após %s", elapsed.Round(time.Millisecond))
	}
	if !strings.Contains(err.Error(), "i/o timeout") && !os.IsTimeout(err) {
		t.Fatalf("erro inesperado (não parece timeout): %v", err)
	}
	t.Logf("erro de deadline: %v (retorno em %s)", err, elapsed.Round(time.Millisecond))
}
