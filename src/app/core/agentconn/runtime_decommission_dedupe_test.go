package agentconn

import (
	"testing"
	"time"
)

// shouldCacheCommandResult decide se um resultado vira terminal no dedupe.
//
// Regressão: um DecommissionAgent que FALHOU (sem uninstaller, sem elevação,
// launch recusado) era cacheado como resultado terminal. O servidor reentrega
// o comando de descomissionamento enquanto não houver confirmação; com o erro
// cacheado, cada reentrega apenas republicava "falha" e NUNCA reexecutava — a
// máquina ficava instalada para sempre mesmo depois de o motivo da falha
// desaparecer (o instalador termina de gravar o uninstall.exe, por exemplo).
//
// O SUCESSO também não é cacheado, e por um motivo diferente: o handler responde
// exitCode=0 antes de lançar o uninstaller (que roda numa goroutine ~5s depois).
// Se aquele lançamento falhar, um resultado terminal já cacheado impediria para
// sempre a nova tentativa, com a máquina ainda instalada.
//
// Outros comandos mantêm o comportamento antigo: reentrega não reexecuta.
func TestShouldCacheCommandResult(t *testing.T) {
	cases := []struct {
		name     string
		cmdType  string
		exitCode int
		want     bool
	}{
		{"descomissionamento com falha precisa poder tentar de novo", agentDecommissionWireType, 1, false},
		{"descomissionamento nao e terminal nem no sucesso (launch e assincrono)", agentDecommissionWireType, 0, false},
		{"outro comando com falha segue terminal (sem reexecucao)", "restart", 1, true},
		{"outro comando com sucesso e terminal", "restart", 0, true},
		{"tipo vazio com falha segue terminal", "", 1, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCacheCommandResult(tc.cmdType, tc.exitCode); got != tc.want {
				t.Fatalf("shouldCacheCommandResult(%q, %d) = %v, queria %v", tc.cmdType, tc.exitCode, got, tc.want)
			}
		})
	}
}

// O dedupe precisa ser realmente liberado: após a falha, a reentrega do MESMO
// CommandId tem de voltar a executar (e não republishar o erro cacheado).
func TestReleaseCommandDedupePermiteReentregaAposFalha(t *testing.T) {
	var deleted []string
	r := NewRuntime(Options{
		DeleteCommandDedupe: func(key string) error {
			deleted = append(deleted, key)
			return nil
		},
	})

	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "cmd-falha"})

	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("primeira entrega deve executar")
	}
	// Em curso / falhou: a reentrega imediata é suprimida.
	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); shouldExecute {
		t.Fatal("reentrega durante a execucao nao pode reexecutar")
	}

	r.releaseCommandDedupe(key)
	if len(deleted) != 1 || deleted[0] != key {
		t.Fatalf("a copia persistida deve ser removida, veio %+v", deleted)
	}

	// A reentrega agora precisa executar de novo.
	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("apos liberar o dedupe a reentrega PRECISA reexecutar o comando")
	}
}

// A reserva liberada nao pode sobreviver no disco: um reboot no meio do ciclo
// restauraria a supressao e a reentrega ficaria travada de novo.
func TestReleaseCommandDedupeNaoSobreviveAoReboot(t *testing.T) {
	disk := map[string]CommandDedupeEntry{}

	r := NewRuntime(Options{
		PersistCommandDedupe: func(entry CommandDedupeEntry) error {
			disk[entry.Key] = entry
			return nil
		},
		DeleteCommandDedupe: func(key string) error {
			delete(disk, key)
			return nil
		},
	})

	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "cmd-reboot"})
	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("primeira entrega deve executar")
	}
	if _, ok := disk[key]; !ok {
		t.Fatal("a reserva deveria ter sido persistida")
	}

	r.releaseCommandDedupe(key)
	if _, ok := disk[key]; ok {
		t.Fatal("a reserva removida nao pode continuar no disco")
	}

	restarted := NewRuntime(Options{
		LoadCommandDedupe: func() []CommandDedupeEntry {
			entries := make([]CommandDedupeEntry, 0, len(disk))
			for _, entry := range disk {
				entries = append(entries, entry)
			}
			return entries
		},
	})

	if _, shouldExecute := restarted.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("apos o reboot a reentrega ainda precisa reexecutar o descomissionamento")
	}

	_ = time.Now
}
