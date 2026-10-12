package agentconn

import (
	"testing"
	"time"
)

func TestIsPowerActionWireType(t *testing.T) {
	for _, tc := range []string{"restart", "RESTART", " reboot ", "shutdown"} {
		if !isPowerActionWireType(tc) {
			t.Fatalf("%q deveria ser comando de energia", tc)
		}
	}
	for _, tc := range []string{"", "ticketchatinvite", "systeminfo", "remotesessionstart", "notification"} {
		if isPowerActionWireType(tc) {
			t.Fatalf("%q não deveria ser comando de energia", tc)
		}
	}
}

// Regressão do loop de reinício: um comando de energia reinicia a máquina antes
// do result/ack, então o dedupe precisa sobreviver ao reboot — sem isso a
// reentrega do servidor reexecutava o reboot a cada ~5 min por até 24h.
func TestCommandDedupePersistidoSobreviveAoReboot(t *testing.T) {
	var persisted []CommandDedupeEntry
	r := NewRuntime(Options{
		PersistCommandDedupe: func(entry CommandDedupeEntry) error {
			persisted = append(persisted, entry)
			return nil
		},
	})

	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "cmd-1"})
	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("primeira entrega deve executar")
	}
	if len(persisted) != 1 || persisted[0].Key != key {
		t.Fatalf("reserva deve ser persistida, veio %+v", persisted)
	}

	r.completeFanoutDispatch(key, natsResultEnvelope{CommandID: "cmd-1", ExitCode: 0, Output: "reinicio agendado"})
	if len(persisted) != 2 || persisted[1].Result == nil || persisted[1].Result.Output != "reinicio agendado" {
		t.Fatalf("resultado deve ser persistido junto da chave, veio %+v", persisted)
	}

	// "Reboot": novo processo lê o que ficou gravado e NÃO reexecuta.
	restarted := NewRuntime(Options{
		LoadCommandDedupe: func() []CommandDedupeEntry { return persisted },
	})
	cached, shouldExecute := restarted.reserveFanoutDispatch(key, commandAgentDedupeTTL)
	if shouldExecute {
		t.Fatal("após o reboot a reentrega não pode reexecutar o comando")
	}
	if cached == nil || cached.Output != "reinicio agendado" {
		t.Fatalf("deveria republicar o resultado cacheado, veio %+v", cached)
	}
}

func TestCommandDedupeNaoPersisteChaveDeFanout(t *testing.T) {
	writes := 0
	r := NewRuntime(Options{
		PersistCommandDedupe: func(CommandDedupeEntry) error {
			writes++
			return nil
		},
	})

	fanoutKey := fanoutDedupeKey(natsCommandEnvelope{IdempotencyKey: "idem", DispatchID: "dispatch-1"})
	if _, shouldExecute := r.reserveFanoutDispatch(fanoutKey, commandFanoutDedupeDefaultTTL); !shouldExecute {
		t.Fatal("primeira entrega de fan-out deve executar")
	}
	if writes != 0 {
		t.Fatalf("chave de fan-out não deve ser persistida (gravações=%d)", writes)
	}
}

func TestCommandDedupeIgnoraEntradaExpirada(t *testing.T) {
	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "cmd-expirado"})
	r := NewRuntime(Options{
		LoadCommandDedupe: func() []CommandDedupeEntry {
			return []CommandDedupeEntry{{Key: key, ExpiresAt: time.Now().Add(-time.Hour)}}
		},
	})

	if _, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); !shouldExecute {
		t.Fatal("entrada expirada não pode suprimir a execução")
	}
}
