package app

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// uiWatchdogSessionFake simula o modo multiusuário: sessões ativas + contagem
// de processos por sessão (o equivalente injetável de WTSEnumerateSessions e
// ProcessIdToSessionId).
type uiWatchdogSessionFake struct {
	hasUI    bool
	sessions []uint32
	counts   map[uint32]int
	spawns   []string
	kills    int
	spawnErr error
	logs     []string
	now      time.Time
}

func newUIWatchdogSessionFake(f *uiWatchdogSessionFake) *uiWatchdog {
	if f.now.IsZero() {
		f.now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	}
	if f.counts == nil {
		f.counts = make(map[uint32]int)
	}
	return newUIWatchdog(uiWatchdogDeps{
		HasUI:        func() bool { return f.hasUI },
		ProcessCount: func() int { return uiProcessTotal(f.counts) },
		ActiveSessions: func() []uint32 {
			return append([]uint32(nil), f.sessions...)
		},
		UISessionCounts: func() map[uint32]int {
			out := make(map[uint32]int, len(f.counts))
			for sid, n := range f.counts {
				out[sid] = n
			}
			return out
		},
		SpawnUISession: func(sid uint32, reason string) error {
			f.spawns = append(f.spawns, sessionLabel(sid, reason))
			if f.spawnErr != nil {
				return f.spawnErr
			}
			if f.counts == nil {
				f.counts = make(map[uint32]int)
			}
			f.counts[sid]++
			return nil
		},
		KillZombieUI: func() int {
			f.kills++
			killed := uiProcessTotal(f.counts)
			f.counts = make(map[uint32]int)
			return killed
		},
		Log: func(message string) { f.logs = append(f.logs, message) },
		Now: func() time.Time { return f.now },
	})
}

func sessionLabel(sid uint32, reason string) string {
	return strconv.FormatUint(uint64(sid), 10) + ":" + reason
}

func (f *uiWatchdogSessionFake) advance(d time.Duration) { f.now = f.now.Add(d) }

// Duas sessões ativas sem UI: cada uma recebe a sua UI (nada de "a UI" global).
func TestUIWatchdogSessions_SpawnsOnePerActiveSession(t *testing.T) {
	f := &uiWatchdogSessionFake{sessions: []uint32{1, 2}}
	w := newUIWatchdogSessionFake(f)

	w.Tick()
	if len(f.spawns) != 0 {
		t.Fatalf("primeiro tick não deve relançar (limiar=%d); spawns=%v", uiWatchdogMissThreshold, f.spawns)
	}
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("uma UI por sessão ativa; spawns=%v", f.spawns)
	}
	if !strings.Contains(f.spawns[0], "1:ausente") || !strings.Contains(f.spawns[1], "2:ausente") {
		t.Fatalf("sessões inesperadas: %v", f.spawns)
	}
}

// Sessão que JÁ tem processo de UI não é duplicada; a outra é atendida mesmo
// com uma UI conectada no IPC (HasUI global).
func TestUIWatchdogSessions_SkipsSessionWithExistingUI(t *testing.T) {
	f := &uiWatchdogSessionFake{
		hasUI:    true,
		sessions: []uint32{1, 2},
		counts:   map[uint32]int{1: 1, 2: 0},
	}
	w := newUIWatchdogSessionFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("só a sessão sem UI deve ser relançada; spawns=%v", f.spawns)
	}
	if !strings.Contains(f.spawns[0], "2:sessao-sem-ui") {
		t.Fatalf("sessão errada relançada: %v", f.spawns)
	}
	if f.kills != 0 {
		t.Fatalf("não pode matar UI com IPC conectado; kills=%d", f.kills)
	}
}

// Todas as sessões com UI: nada acontece.
func TestUIWatchdogSessions_DoesNothingWhenEverySessionHasUI(t *testing.T) {
	f := &uiWatchdogSessionFake{
		hasUI:    true,
		sessions: []uint32{1, 2},
		counts:   map[uint32]int{1: 1, 2: 1},
	}
	w := newUIWatchdogSessionFake(f)

	for i := 0; i < 5; i++ {
		w.Tick()
	}
	if len(f.spawns) != 0 || f.kills != 0 {
		t.Fatalf("spawns=%v kills=%d, esperado nenhum", f.spawns, f.kills)
	}
}

// UI viva sem IPC em duas sessões = zumbis: encerra e relança em cada sessão.
func TestUIWatchdogSessions_ZombieRelaunchesEverySession(t *testing.T) {
	f := &uiWatchdogSessionFake{
		sessions: []uint32{1, 2},
		counts:   map[uint32]int{1: 1, 2: 1},
	}
	w := newUIWatchdogSessionFake(f)

	w.Tick()
	if f.kills != 0 {
		t.Fatalf("não pode matar antes do limiar de zumbi; kills=%d", f.kills)
	}
	w.Tick()
	if f.kills != 1 {
		t.Fatalf("processos sem IPC deveriam ser encerrados; kills=%d", f.kills)
	}
	if len(f.spawns) != 2 {
		t.Fatalf("após o kill, relança em cada sessão; spawns=%v", f.spawns)
	}
}

// Sem sessão ativa não há onde abrir a UI e o cooldown não é consumido; a
// mensagem de estado é deduplicada por tick.
func TestUIWatchdogSessions_NoSessionDeduplicatesNotice(t *testing.T) {
	f := &uiWatchdogSessionFake{}
	w := newUIWatchdogSessionFake(f)

	for i := 0; i < 4; i++ {
		w.Tick()
	}
	if len(f.spawns) != 0 {
		t.Fatalf("sem sessão não deve relançar; spawns=%v", f.spawns)
	}
	const expected = "sem sessão de usuário ativa — UI será relançada quando alguém fizer logon"
	count := 0
	for _, line := range f.logs {
		if line == expected {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mensagem deveria aparecer uma vez; apareceu %d (logs=%v)", count, f.logs)
	}
}

// RequestSpawn (convite de chat) relança NA HORA em todas as sessões sem UI.
func TestUIWatchdogSessions_RequestSpawnIsImmediate(t *testing.T) {
	f := &uiWatchdogSessionFake{sessions: []uint32{1, 2}}
	w := newUIWatchdogSessionFake(f)

	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 2 {
		t.Fatalf("pedido deve relançar em cada sessão sem UI; spawns=%v", f.spawns)
	}
	for _, spawn := range f.spawns {
		if !strings.Contains(spawn, "pedido:chat_invite") {
			t.Fatalf("motivo inesperado: %v", f.spawns)
		}
	}

	// Cooldown curto por sessão barra repetição imediata.
	w.RequestSpawn("chat_invite")
	if len(f.spawns) != 2 {
		t.Fatalf("cooldown deveria barrar repetição; spawns=%v", f.spawns)
	}
}

// Falha de spawn numa sessão não impede a outra (falha isolada).
func TestUIWatchdogSessions_SpawnFailureDoesNotBlockOtherSessions(t *testing.T) {
	f := &uiWatchdogSessionFake{sessions: []uint32{1, 2}, spawnErr: errors.New("sem token")}
	w := newUIWatchdogSessionFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("as duas sessões devem ser tentadas; spawns=%v", f.spawns)
	}
	// Cooldown consumido mesmo na falha: sem retry apertado.
	w.Tick()
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("falha não pode gerar retry imediato; spawns=%v", f.spawns)
	}
}

// Sessão 0 (serviços) e 0xFFFFFFFF ("sem sessão") nunca entram no plano, e
// duplicatas são removidas.
func TestNormalizedUISessions(t *testing.T) {
	got := normalizedUISessions([]uint32{2, 0, 1, 0xFFFFFFFF, 2, 1})
	want := []uint32{1, 2}
	if len(got) != len(want) {
		t.Fatalf("normalizedUISessions = %v, esperado %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizedUISessions = %v, esperado %v", got, want)
		}
	}
}

func TestSessionsMissingUI(t *testing.T) {
	got := sessionsMissingUI([]uint32{1, 2, 3}, map[uint32]int{1: 1, 2: 0, 3: 2})
	want := []uint32{2}
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("sessionsMissingUI = %v, esperado %v", got, want)
	}
}
