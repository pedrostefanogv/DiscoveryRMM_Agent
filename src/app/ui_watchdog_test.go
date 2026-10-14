package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// uiWatchdogFake é o estado observável do watchdog nos testes.
type uiWatchdogFake struct {
	hasUI     bool
	procs     int
	noSession bool // false = há usuário logado (default dos testes)
	spawns    []string
	kills     int
	spawnErr  error
	logs      []string
	now       time.Time
}

func newUIWatchdogFake(f *uiWatchdogFake) *uiWatchdog {
	if f.now.IsZero() {
		f.now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	}
	return newUIWatchdog(uiWatchdogDeps{
		HasUI:                func() bool { return f.hasUI },
		ProcessCount:         func() int { return f.procs },
		UserSessionAvailable: func() bool { return !f.noSession },
		SpawnUI: func(reason string) error {
			f.spawns = append(f.spawns, reason)
			return f.spawnErr
		},
		KillZombieUI: func() int {
			f.kills++
			killed := f.procs
			f.procs = 0
			return killed
		},
		Log: func(message string) { f.logs = append(f.logs, message) },
		Now: func() time.Time { return f.now },
	})
}

func (f *uiWatchdogFake) advance(d time.Duration) { f.now = f.now.Add(d) }

// Sem UI: o primeiro tick só conta; o segundo (limiar) relança.
func TestUIWatchdog_RelaunchAfterMissedChecks(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.Tick()
	if len(f.spawns) != 0 {
		t.Fatalf("primeiro tick não deve relançar (limiar=%d); spawns=%v", uiWatchdogMissThreshold, f.spawns)
	}

	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("segundo tick deveria relançar uma vez; spawns=%v", f.spawns)
	}
	if !strings.Contains(f.spawns[0], "ausente") {
		t.Fatalf("motivo inesperado: %q", f.spawns[0])
	}
}

// UI conectada no IPC: nada acontece, nem relançamento nem kill.
func TestUIWatchdog_DoesNothingWhenUIConnected(t *testing.T) {
	f := &uiWatchdogFake{hasUI: true, procs: 1}
	w := newUIWatchdogFake(f)

	for i := 0; i < 5; i++ {
		w.Tick()
	}
	if len(f.spawns) != 0 || f.kills != 0 {
		t.Fatalf("com UI conectada não pode relançar nem matar; spawns=%v kills=%d", f.spawns, f.kills)
	}
}

// Cooldown: depois de relançar, não relança de novo antes do intervalo.
func TestUIWatchdog_SpawnCooldown(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("esperado 1 spawn; spawns=%v", f.spawns)
	}

	// Ticks dentro do cooldown (mesmo passando o limiar de novos misses).
	f.advance(time.Minute)
	w.Tick()
	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("cooldown deveria barrar novos spawns; spawns=%v", f.spawns)
	}

	// Passado o cooldown, o limiar de misses volta a valer.
	f.advance(uiWatchdogSpawnCooldown)
	w.Tick()
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("depois do cooldown deveria relançar; spawns=%v", f.spawns)
	}
}

// Processo vivo SEM IPC = UI travada: encerra e relança.
func TestUIWatchdog_ZombieUIIsKilledAndRelaunched(t *testing.T) {
	f := &uiWatchdogFake{procs: 1}
	w := newUIWatchdogFake(f)

	w.Tick()
	if f.kills != 0 {
		t.Fatalf("não pode matar antes do limiar de zumbi; kills=%d", f.kills)
	}

	w.Tick()
	if f.kills != 1 {
		t.Fatalf("UI travada (processo vivo sem IPC) deveria ser encerrada; kills=%d", f.kills)
	}
	if len(f.spawns) != 1 || !strings.Contains(f.spawns[0], "zumbi") {
		t.Fatalf("esperado relançamento por zumbi; spawns=%v", f.spawns)
	}
}

// UI que fica conectando/desconectando zera o contador de zumbi (não mata uma
// UI que está apenas subindo).
func TestUIWatchdog_ZombieCounterResetsWhenUIReturns(t *testing.T) {
	f := &uiWatchdogFake{procs: 1}
	w := newUIWatchdogFake(f)

	w.Tick() // zumbi #1
	f.hasUI = true
	w.Tick() // UI conectou: zera
	f.hasUI = false
	w.Tick() // zumbi #1 de novo

	if f.kills != 0 {
		t.Fatalf("contador deveria ter sido zerado quando a UI conectou; kills=%d", f.kills)
	}
}

// Sem usuário logado não há onde abrir a UI — e o cooldown não é consumido.
func TestUIWatchdog_WaitsForUserSession(t *testing.T) {
	f := &uiWatchdogFake{noSession: true}
	w := newUIWatchdogFake(f)

	for i := 0; i < 4; i++ {
		w.Tick()
	}
	if len(f.spawns) != 0 {
		t.Fatalf("sem sessão de usuário não deve relançar; spawns=%v", f.spawns)
	}

	f.noSession = false
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("com o usuário logado o próximo tick deve relançar; spawns=%v", f.spawns)
	}
}

// Falha no spawn não pode virar retry apertado (o cooldown é consumido).
func TestUIWatchdog_SpawnFailureKeepsCooldown(t *testing.T) {
	f := &uiWatchdogFake{spawnErr: errors.New("sem token")}
	w := newUIWatchdogFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("esperado 1 tentativa; spawns=%v", f.spawns)
	}

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("falha não pode gerar retry imediato; spawns=%v", f.spawns)
	}
}

// RequestSpawn (convite de chat sem UI) relança NA HORA, sem esperar o limiar.
func TestUIWatchdog_RequestSpawnIsImmediate(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 1 {
		t.Fatalf("RequestSpawn deveria relançar imediatamente; spawns=%v", f.spawns)
	}
	if !strings.Contains(f.spawns[0], "chat_invite") {
		t.Fatalf("motivo inesperado: %q", f.spawns[0])
	}

	// Segundo convite logo depois: cooldown curto segura a repetição.
	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 1 {
		t.Fatalf("cooldown do RequestSpawn não foi respeitado; spawns=%v", f.spawns)
	}

	f.advance(uiWatchdogRequestCooldown)
	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 2 {
		t.Fatalf("passado o cooldown deveria tentar de novo; spawns=%v", f.spawns)
	}
}

// RequestSpawn com UI já conectada não faz nada.
func TestUIWatchdog_RequestSpawnSkippedWhenUIConnected(t *testing.T) {
	f := &uiWatchdogFake{hasUI: true}
	w := newUIWatchdogFake(f)

	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 0 {
		t.Fatalf("com UI conectada o convite não precisa relançar nada; spawns=%v", f.spawns)
	}
}

// RequestSpawn não bloqueia na ausência de sessão de usuário.
func TestUIWatchdog_RequestSpawnWaitsForUserSession(t *testing.T) {
	f := &uiWatchdogFake{noSession: true}
	w := newUIWatchdogFake(f)

	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 0 {
		t.Fatalf("sem sessão de usuário não deve relançar; spawns=%v", f.spawns)
	}
}

// Depois de ENCERRAR a UI travada, o relançamento não espera o cooldown longo:
// o processo que segurava o lock já morreu, então a janela de espera só deixaria
// o usuário sem interface. (Sem esta regra, o spawn ficaria barrado pelos 5 min
// do caminho periódico sempre que a UI travasse pouco depois de subir.)
func TestUIWatchdog_ZombieRelaunchIgnoresPeriodicCooldown(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("esperado 1 spawn inicial; spawns=%v", f.spawns)
	}

	// UI volta a existir e trava 2 minutos depois (menos que o cooldown de 5 min).
	f.advance(2 * time.Minute)
	f.hasUI = true
	w.Tick()
	f.hasUI = false
	f.procs = 1
	w.Tick() // zumbi #1
	w.Tick() // zumbi #2: encerra e relança

	if f.kills != 1 {
		t.Fatalf("UI travada deveria ser encerrada; kills=%d", f.kills)
	}
	if len(f.spawns) != 2 {
		t.Fatalf("relançamento pós-kill não pode esperar o cooldown periódico; spawns=%v", f.spawns)
	}
	if !strings.Contains(f.spawns[1], "zumbi") {
		t.Fatalf("segundo spawn deveria ser do tipo zumbi; spawns=%v", f.spawns)
	}
}

// REINCIDÊNCIA: se a UI sobe e trava de novo sem NUNCA conectar, o watchdog não
// pode matar/relançar a cada 2,5 min indefinidamente — passa a usar o cooldown
// longo. Ao conectar, o streak zera e a recuperação volta a ser imediata.
func TestUIWatchdog_ZombieStreakBacksOff(t *testing.T) {
	f := &uiWatchdogFake{procs: 1}
	w := newUIWatchdogFake(f)

	// 1ª travada: encerra e relança já.
	w.Tick()
	w.Tick()
	if f.kills != 1 || len(f.spawns) != 1 {
		t.Fatalf("1ª travada deveria encerrar e relançar; kills=%d spawns=%v", f.kills, f.spawns)
	}

	// A UI relançada volta a travar sem nunca conectar (2 verificações).
	f.procs = 1
	w.Tick()
	w.Tick()
	if f.kills != 2 {
		t.Fatalf("2ª travada deveria ser encerrada; kills=%d", f.kills)
	}
	if len(f.spawns) != 1 {
		t.Fatalf("reincidência não pode relançar de imediato (cooldown longo); spawns=%v", f.spawns)
	}

	// Passado o cooldown longo, tenta de novo.
	f.advance(uiWatchdogSpawnCooldown)
	f.procs = 1
	w.Tick()
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("passado o cooldown longo deveria relançar; spawns=%v", f.spawns)
	}

	// Uma UI que finalmente CONECTA zera o streak: a próxima travada volta a ser
	// tratada com recuperação imediata.
	f.advance(uiWatchdogSpawnCooldown)
	f.hasUI = true
	w.Tick()
	f.hasUI = false
	f.procs = 1
	w.Tick()
	w.Tick()
	if len(f.spawns) != 3 {
		t.Fatalf("travada após conexão deveria relançar de imediato; spawns=%v", f.spawns)
	}
}
