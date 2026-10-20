package app

import (
	"strings"
	"testing"
	"time"
)

// Graça pós-spawn (multiusuário): a UI relançada leva ~1 min para nascer e
// conectar; o watchdog não pode matá-la no parto (ciclo dos logs de
// 2026-10-20: spawn 18:21:44 → "processos vivos sem IPC" 18:22:35+ → kill →
// relançar → repetir).
func TestUIWatchdogSessions_SpawnRecenteNaoEhZumbi(t *testing.T) {
	f := &uiWatchdogSessionFake{sessions: []uint32{1}}
	w := newUIWatchdogSessionFake(f)

	// Pedido imediato (convite de chat) relança a UI da sessão 1.
	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 1 {
		t.Fatalf("esperava 1 spawn, obtive %d (%v)", len(f.spawns), f.spawns)
	}
	if f.counts[1] != 1 {
		t.Fatalf("esperava 1 processo da sessão 1, obtive %d", f.counts[1])
	}

	// Dois ticks DENTRO da graça: nada de kill, nada de spawn extra.
	f.advance(uiWatchdogSpawnGrace - time.Second)
	w.Tick()
	w.Tick()
	if f.kills != 0 {
		t.Fatalf("graça pós-spawn não respeitada: %d kill(s)", f.kills)
	}
	if len(f.spawns) != 1 {
		t.Fatalf("graça gerou spawn extra: %v", f.spawns)
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "aguardando a primeira conexão IPC") {
		t.Fatalf("esperava log de graça, obtive %v", f.logs)
	}

	// Passada a graça, o caminho zumbi volta a valer: 2 verificações → kill + relança.
	f.advance(2 * time.Second)
	w.Tick() // zombieChecks = 1 (< limiar)
	if f.kills != 0 {
		t.Fatalf("kill antes do limiar: %d", f.kills)
	}
	f.advance(uiWatchdogTickInterval)
	w.Tick() // zombieChecks = 2 → kill + spawn
	if f.kills != 1 {
		t.Fatalf("esperava 1 kill após a graça, obtive %d", f.kills)
	}
	if len(f.spawns) != 2 || !strings.Contains(f.spawns[1], "zumbi") {
		t.Fatalf("esperava respawn 'zumbi', obtive %v", f.spawns)
	}
}

// Mesma graça no modo legado (uma sessão implícita).
func TestUIWatchdog_SpawnRecenteNaoEhZumbi(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.RequestSpawn("chat_invite")
	f.procs = 1 // processo vivo, IPC ainda não conectado

	f.advance(uiWatchdogSpawnGrace - time.Second)
	w.Tick()
	w.Tick()
	if f.kills != 0 {
		t.Fatalf("graça pós-spawn não respeitada: %d kill(s)", f.kills)
	}
	if len(f.spawns) != 1 {
		t.Fatalf("graça gerou spawn extra: %v", f.spawns)
	}

	f.advance(2 * time.Second)
	w.Tick()
	f.advance(uiWatchdogTickInterval)
	w.Tick()
	if f.kills != 1 {
		t.Fatalf("esperava 1 kill após a graça, obtive %d", f.kills)
	}
	if len(f.spawns) != 2 || !strings.Contains(f.spawns[1], "zumbi") {
		t.Fatalf("esperava respawn 'zumbi', obtive %v", f.spawns)
	}
}
