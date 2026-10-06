//go:build windows

package remotesession

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/sessioncontrol"
)

// lockTestClock relógio controlado (atômico: o watchdog lê em outra goroutine).
type lockTestClock struct{ nanos int64 }

func (c *lockTestClock) now() time.Time { return time.Unix(0, atomic.LoadInt64(&c.nanos)) }
func (c *lockTestClock) advance(d time.Duration) {
	atomic.AddInt64(&c.nanos, int64(d))
}

type fakeLockIndicator struct {
	rig *lockTestRig
}

func (f *fakeLockIndicator) Show(string) error {
	f.rig.mu.Lock()
	f.rig.showCalls++
	err := f.rig.indErr
	f.rig.mu.Unlock()
	return err
}

func (f *fakeLockIndicator) Hide() error {
	f.rig.mu.Lock()
	f.rig.hideCalls++
	f.rig.mu.Unlock()
	return nil
}

func (f *fakeLockIndicator) Close() {
	f.rig.mu.Lock()
	f.rig.indCloseCalls++
	f.rig.mu.Unlock()
}

type lockTestRig struct {
	mgr   *InputLockManager
	clock *lockTestClock

	mu            sync.Mutex
	states        []InputLockState
	blockCalls    int
	unblockCalls  int
	showCalls     int
	hideCalls     int
	indCloseCalls int
	newIndCalls   int
	blockErr      error
	unblockErr    error
	indErr        error
	indicatorErr  error
	unblocked     chan struct{}
}

func newLockRig(t *testing.T) *lockTestRig {
	t.Helper()
	oldTick := inputLockWatchdogTick
	inputLockWatchdogTick = 10 * time.Millisecond

	clock := &lockTestClock{}
	rig := &lockTestRig{clock: clock, unblocked: make(chan struct{}, 16)}
	// Injeção NA CONSTRUÇÃO: o watchdog já inicia lendo now/unblock — mutar
	// esses campos depois do start é data race (detectado com -race).
	rig.mgr = newInputLockManager("sess-lock", func(st InputLockState) {
		rig.mu.Lock()
		rig.states = append(rig.states, st)
		rig.mu.Unlock()
	}, inputLockOptions{
		now: clock.now,
		block: func() (string, error) {
			rig.mu.Lock()
			rig.blockCalls++
			err := rig.blockErr
			rig.mu.Unlock()
			if err != nil {
				return "", err
			}
			return "blockinput", nil
		},
		unblock: func() error {
			rig.mu.Lock()
			rig.unblockCalls++
			err := rig.unblockErr
			rig.mu.Unlock()
			if err == nil {
				select {
				case rig.unblocked <- struct{}{}:
				default:
				}
			}
			return err
		},
		newInd: func() (lockIndicator, error) {
			rig.mu.Lock()
			rig.newIndCalls++
			err := rig.indicatorErr
			rig.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return &fakeLockIndicator{rig: rig}, nil
		},
		tick: inputLockWatchdogTick,
	})

	t.Cleanup(func() {
		rig.mgr.Close()
		inputLockWatchdogTick = oldTick
	})
	return rig
}

func (r *lockTestRig) lastState(t *testing.T) InputLockState {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		t.Fatal("nenhum estado publicado")
	}
	return r.states[len(r.states)-1]
}

// waitState espera (com timeout) um estado publicado que satisfaça pred.
// NÃO usar o sinal de unblock para sincronizar: Release chama unblock ANTES de
// publicar o estado, então ler o último estado logo após o sinal é uma corrida.
func (r *lockTestRig) waitState(t *testing.T, timeout time.Duration, pred func(InputLockState) bool) InputLockState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		var last InputLockState
		has := len(r.states) > 0
		if has {
			last = r.states[len(r.states)-1]
		}
		r.mu.Unlock()
		if has && pred(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("estado esperado nao foi publicado em %s (ultimo=%+v)", timeout, last)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *lockTestRig) counts() (block, unblock, show, hide, indClose int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blockCalls, r.unblockCalls, r.showCalls, r.hideCalls, r.indCloseCalls
}

func TestInputLock_LockReleaseWithIndicator(t *testing.T) {
	rig := newLockRig(t)

	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 60})
	st := rig.lastState(t)
	if !st.Locked || st.Method != "blockinput" || st.Reason != "locked" {
		t.Fatalf("estado apos bloquear: %+v", st)
	}
	if st.LeaseRemainingSeconds <= 0 {
		t.Fatalf("lease restante deveria ser > 0: %+v", st)
	}
	if block, _, show, _, _ := rig.counts(); block != 1 || show != 1 {
		t.Fatalf("block=%d show=%d, esperado 1/1", block, show)
	}

	rig.mgr.HandleCommand(map[string]any{"locked": false})
	if st := rig.lastState(t); st.Locked || st.Reason != "viewer_request" {
		t.Fatalf("estado apos liberar: %+v", st)
	}
	if _, unblock, _, hide, _ := rig.counts(); unblock != 1 || hide != 1 {
		t.Fatalf("unblock=%d hide=%d, esperado 1/1", unblock, hide)
	}
}

func TestInputLock_RepeatedLockOnlyRenews(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 60})
	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 60})

	if st := rig.lastState(t); !st.Locked || st.Reason != "renewed" {
		t.Fatalf("segundo lock deveria ser renovacao: %+v", st)
	}
	if block, _, _, _, _ := rig.counts(); block != 1 {
		t.Fatalf("BlockInput deveria ter sido chamado 1x, chamado %d", block)
	}
}

func TestInputLock_BlockFailureStaysUnlocked(t *testing.T) {
	rig := newLockRig(t)
	rig.mu.Lock()
	rig.blockErr = errors.New("BlockInput(TRUE) recusado: acesso negado (errno=5)")
	rig.mu.Unlock()

	rig.mgr.HandleCommand(map[string]any{"locked": true})

	st := rig.lastState(t)
	if st.Locked {
		t.Fatalf("nao deveria marcar como bloqueado: %+v", st)
	}
	if !strings.Contains(st.Reason, "block_failed") {
		t.Fatalf("motivo deveria indicar block_failed: %q", st.Reason)
	}
	if _, _, show, _, _ := rig.counts(); show != 0 {
		t.Fatalf("indicador nao deveria aparecer sem bloqueio efetivo")
	}
}

func TestInputLock_IndicatorFailureDoesNotPreventLock(t *testing.T) {
	rig := newLockRig(t)
	rig.mu.Lock()
	rig.indErr = errors.New("Shell_NotifyIconW falhou")
	rig.mu.Unlock()

	rig.mgr.HandleCommand(map[string]any{"locked": true})
	if st := rig.lastState(t); !st.Locked {
		t.Fatalf("indicador best-effort: bloqueio deveria valer mesmo com falha: %+v", st)
	}
}

func TestInputLock_IndicatorCreationFailureDoesNotPreventLock(t *testing.T) {
	rig := newLockRig(t)
	rig.mu.Lock()
	rig.indicatorErr = errors.New("CreateWindowExW falhou")
	rig.mu.Unlock()

	rig.mgr.HandleCommand(map[string]any{"locked": true})
	if st := rig.lastState(t); !st.Locked {
		t.Fatalf("bloqueio deveria valer sem indicador: %+v", st)
	}
}

func TestInputLock_LeaseExpiresReleases(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 20})
	// Espera o bloqueio ser publicado antes de avançar o relógio (evita que o
	// watchdog veja o lease já expirado no primeiro tick).
	rig.waitState(t, 2*time.Second, func(st InputLockState) bool { return st.Locked })
	rig.clock.advance(21 * time.Second)

	rig.waitState(t, 3*time.Second, func(st InputLockState) bool {
		return !st.Locked && st.Reason == "lease_expired"
	})
	if _, unblock, _, _, _ := rig.counts(); unblock != 1 {
		t.Fatalf("UnblockInput deveria ter sido chamado 1x, chamado %d", unblock)
	}
}

func TestInputLock_RenewExtendsLease(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 15})

	rig.clock.advance(10 * time.Second)
	rig.mgr.Renew()
	rig.mgr.PublishCurrent("check")
	if st := rig.lastState(t); !st.Locked || st.LeaseRemainingSeconds < 14 {
		t.Fatalf("Renew deveria estender o lease: %+v", st)
	}

	// 10s desde a renovação (20s desde o lock) ainda dentro do lease novo.
	rig.clock.advance(10 * time.Second)
	time.Sleep(80 * time.Millisecond)
	if st := rig.lastState(t); !st.Locked {
		t.Fatalf("lease renovado nao deveria expirar: %+v", st)
	}
}

func TestInputLock_HardCapReleases(t *testing.T) {
	rig := newLockRig(t)
	// Lease longo (600s) e teto curto (60s): o teto vence primeiro.
	rig.mgr.HandleCommand(map[string]any{"locked": true, "leaseSeconds": 600, "maxSeconds": 60})
	rig.waitState(t, 2*time.Second, func(st InputLockState) bool { return st.Locked })
	rig.clock.advance(61 * time.Second)

	rig.waitState(t, 3*time.Second, func(st InputLockState) bool {
		return !st.Locked && st.Reason == "max_duration"
	})
}

func TestInputLock_QueryAndInvalidCommand(t *testing.T) {
	rig := newLockRig(t)

	rig.mgr.HandleCommand(map[string]any{"query": true})
	if st := rig.lastState(t); st.Locked || st.Reason != "query" {
		t.Fatalf("query destravado: %+v", st)
	}

	rig.mgr.HandleCommand(map[string]any{"locked": true})
	rig.mgr.HandleCommand(map[string]any{"query": true})
	if st := rig.lastState(t); !st.Locked || st.Reason != "query" {
		t.Fatalf("query travado: %+v", st)
	}

	rig.mgr.HandleCommand(map[string]any{"foo": "bar"})
	if st := rig.lastState(t); st.Locked || st.Reason != "invalid_command" {
		t.Fatalf("comando invalido: %+v", st)
	}
}

func TestInputLock_CloseReleasesAndIsIdempotent(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.HandleCommand(map[string]any{"locked": true})

	rig.mgr.Close()
	rig.mgr.Close()

	if _, unblock, _, _, indClose := rig.counts(); unblock != 1 || indClose != 1 {
		t.Fatalf("Close deveria liberar 1x e fechar o indicador 1x: unblock=%d indClose=%d", unblock, indClose)
	}
	if st := rig.lastState(t); st.Locked || st.Reason != "manager_closed" {
		t.Fatalf("estado apos Close: %+v", st)
	}
}

func TestInputLock_ReleaseWhenNotLockedIsNoop(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.Release("teste")
	if _, unblock, _, _, _ := rig.counts(); unblock != 0 {
		t.Fatalf("Release sem bloqueio nao deveria chamar UnblockInput")
	}
}

func TestSameSessionIDToleratesHyphens(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2f1d3c4b-5a6e-7f80-9a1b-2c3d4e5f6071", "2f1d3c4b5a6e7f809a1b2c3d4e5f6071", true},
		{"2F1D3C4B-5A6E-7F80-9A1B-2C3D4E5F6071", "2f1d3c4b5a6e7f809a1b2c3d4e5f6071", true},
		{" 2f1d3c4b5a6e7f809a1b2c3d4e5f6071 ", "2f1d3c4b5a6e7f809a1b2c3d4e5f6071", true},
		{"2f1d3c4b5a6e7f809a1b2c3d4e5f6071", "outra-sessao", false},
		{"", "", true},
	}
	for _, c := range cases {
		if got := sameSessionID(c.a, c.b); got != c.want {
			t.Fatalf("sameSessionID(%q,%q)=%t, esperado %t", c.a, c.b, got, c.want)
		}
	}
}

// Regressão (fail-safe): se o unblock FALHA, a máquina pode continuar
// bloqueada — o estado deve permanecer "travado" (verdade) e um novo pedido
// de destrave deve TENTAR de novo, em vez de mentir "liberado".
func TestInputLock_UnblockFailureKeepsLockedAndRetries(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.HandleCommand(map[string]any{"locked": true})
	rig.waitState(t, 2*time.Second, func(st InputLockState) bool { return st.Locked })

	rig.mu.Lock()
	rig.unblockErr = errors.New("BlockInput(FALSE) falhou: errno=5")
	rig.mu.Unlock()
	rig.mgr.HandleCommand(map[string]any{"locked": false})
	rig.waitState(t, 2*time.Second, func(st InputLockState) bool {
		return st.Locked && strings.HasPrefix(st.Reason, "unblock_failed")
	})

	rig.mu.Lock()
	rig.unblockErr = nil
	rig.mu.Unlock()
	rig.mgr.HandleCommand(map[string]any{"locked": false})
	rig.waitState(t, 2*time.Second, func(st InputLockState) bool {
		return !st.Locked && st.Reason == "viewer_request"
	})

	if _, unblock, _, _, _ := rig.counts(); unblock != 2 {
		t.Fatalf("UnblockInput deveria ser tentado 2x, tentado %d", unblock)
	}
}

// Regressão (fail-safe): Close durante o block() de um lock EM VOO não pode
// deixar a entrada bloqueada sem ninguém para liberar (o watchdog já parou e
// nenhum teardown voltaria a passar).
func TestInputLock_CloseDuringBlockUndoesLock(t *testing.T) {
	rig := newLockRig(t)
	blockEntered := make(chan struct{})
	blockRelease := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(blockRelease) })

	rig.mgr.block = func() (string, error) {
		rig.mu.Lock()
		rig.blockCalls++
		rig.mu.Unlock()
		close(blockEntered)
		<-blockRelease
		return "blockinput", nil
	}

	cmdDone := make(chan struct{})
	go func() {
		rig.mgr.HandleCommand(map[string]any{"locked": true})
		close(cmdDone)
	}()
	<-blockEntered

	closeDone := make(chan struct{})
	go func() {
		rig.mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close travou esperando o applyLock em voo")
	}

	releaseOnce.Do(func() { close(blockRelease) })
	select {
	case <-cmdDone:
	case <-time.After(3 * time.Second):
		t.Fatal("applyLock em voo nao terminou")
	}

	if _, unblock, _, _, _ := rig.counts(); unblock != 1 {
		t.Fatalf("o bloqueio em voo deveria ser desfeito (unblock=1), unblock=%d", unblock)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	for _, st := range rig.states {
		if st.Locked {
			t.Fatalf("nenhum estado travado deveria ser publicado apos Close: %+v", st)
		}
	}
}

// Close antes de qualquer lock: o indicador não deve nem ser criado.
func TestInputLock_IndicatorNotCreatedAfterClose(t *testing.T) {
	rig := newLockRig(t)
	rig.mgr.Close()

	if ind := rig.mgr.ensureIndicator(); ind != nil {
		t.Fatal("ensureIndicator nao deveria criar indicador apos Close")
	}
	rig.mu.Lock()
	calls := rig.newIndCalls
	rig.mu.Unlock()
	if calls != 0 {
		t.Fatalf("newInd deveria ter 0 chamadas apos Close, teve %d", calls)
	}
}

func TestInputLockStatePayloadTruncatesLongReason(t *testing.T) {
	long := strings.Repeat("x", 600)
	st := InputLockState{Locked: true, Reason: "block_failed: " + long}
	payload := st.Payload()
	reason, _ := payload["reason"].(string)
	if len([]rune(reason)) > 210 {
		t.Fatalf("motivo deveria ser truncado, tem %d runas", len([]rune(reason)))
	}
	if !strings.HasSuffix(reason, "...") {
		t.Fatalf("motivo truncado deveria terminar em ..., got %q", reason[len(reason)-10:])
	}
	raw, err := encodeRemoteSessionControl(sessioncontrol.NewEnvelope(sessioncontrol.RoleAgent, ControlTypeInputLockChanged, "sess-1", 1, payload))
	if err != nil {
		t.Fatalf("envelope com motivo longo deveria codificar: %v", err)
	}
	if len(raw) > sessioncontrol.MaxEnvelopeBytes {
		t.Fatalf("envelope com %d bytes excede o limite", len(raw))
	}
}
func TestClampLockSeconds(t *testing.T) {
	cases := []struct{ in, min, max, want int }{
		{5, 15, 600, 15},
		{60, 15, 600, 60},
		{9000, 15, 600, 600},
		{0, 15, 600, 15},
	}
	for _, c := range cases {
		if got := clampLockSeconds(c.in, c.min, c.max); got != c.want {
			t.Fatalf("clampLockSeconds(%d,%d,%d)=%d, esperado %d", c.in, c.min, c.max, got, c.want)
		}
	}
}

func TestInputLockStatePayload(t *testing.T) {
	p := InputLockState{Locked: true, Method: "blockinput", LeaseRemainingSeconds: 42, Reason: "locked"}.Payload()
	if p["locked"] != true || p["method"] != "blockinput" || p["leaseRemainingSeconds"] != 42 || p["reason"] != "locked" {
		t.Fatalf("payload inesperado: %+v", p)
	}
	empty := InputLockState{Locked: false}.Payload()
	if empty["locked"] != false {
		t.Fatalf("payload destravado: %+v", empty)
	}
	if _, ok := empty["method"]; ok {
		t.Fatalf("method vazio nao deveria ir no payload: %+v", empty)
	}
}
