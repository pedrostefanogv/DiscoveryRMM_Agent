//go:build windows

package remotesession

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// vkA é o VK da tecla 'a'/'A' (0x41).
const vkA uint16 = 0x41

// stubRecorder registra as injeções feitas pelo InputController nos stubs.
type stubRecorder struct {
	mu   sync.Mutex
	down map[uint16]int
	up   map[uint16]int
}

func newStubRecorder() *stubRecorder {
	return &stubRecorder{down: map[uint16]int{}, up: map[uint16]int{}}
}

func (r *stubRecorder) keyDown(vk uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down[vk]++
	return nil
}

func (r *stubRecorder) keyUp(vk uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up[vk]++
	return nil
}

func (r *stubRecorder) countDown(vk uint16) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.down[vk]
}

func (r *stubRecorder) countUp(vk uint16) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.up[vk]
}

// installKeyboardStub substitui os pontos de injeção de teclado por stubs e
// devolve o gravador; restaura os originais no fim do teste.
func installKeyboardStub(t *testing.T) *stubRecorder {
	t.Helper()
	r := newStubRecorder()
	origDown, origUp := injectKeyDown, injectKeyUp
	injectKeyDown = r.keyDown
	injectKeyUp = r.keyUp
	t.Cleanup(func() {
		injectKeyDown = origDown
		injectKeyUp = origUp
	})
	return r
}

// newTestController cria um controller com watchdog acelerado para o teste.
func newTestController(t *testing.T) *InputController {
	t.Helper()
	oldTimeout, oldTick := keyStuckTimeout, keyWatchdogTick
	keyStuckTimeout = 150 * time.Millisecond
	keyWatchdogTick = 25 * time.Millisecond
	c := NewInputController("test-session")
	t.Cleanup(func() {
		c.Close() // aguarda o watchdog sair antes de restaurar as vars
		keyStuckTimeout = oldTimeout
		keyWatchdogTick = oldTick
	})
	return c
}

// legacyKeyEvent monta o payload que o viewer (RemoteScreenViewer.tsx) envia —
// JSON legado sem "version", processado por handleLegacyInput.
func legacyKeyEvent(typ, code, key string, ctrl, shift, alt, meta bool) []byte {
	return []byte(fmt.Sprintf(
		`{"type":%q,"key":%q,"code":%q,"ctrl":%t,"shift":%t,"alt":%t,"meta":%t,"frameWidth":1920,"frameHeight":1080}`,
		typ, key, code, ctrl, shift, alt, meta))
}

// TestHandleKeyForwardsAutoRepeat verifica o fix central: keydown repetido
// (auto-repeat do browser durante hold) é REPASSADO ao remoto — antes do fix,
// o dedup K2 descartava os repeats e segurar backspace/espaço/letra registrava
// uma única tecla.
func TestHandleKeyForwardsAutoRepeat(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)
	mods := InputModifiers{}

	// Press + 5 repeats (hold) → 6 downs injetados.
	c.handleKey("Backspace", "Backspace", true, mods)
	for i := 0; i < 5; i++ {
		c.handleKey("Backspace", "Backspace", true, mods)
	}
	if got := r.countDown(VK_BACK); got != 6 {
		t.Fatalf("esperado 6 keydowns injetados (1 press + 5 repeats), obtido %d", got)
	}
	if got := r.countUp(VK_BACK); got != 0 {
		t.Fatalf("nenhum keyup esperado durante o hold, obtido %d", got)
	}

	// Release → 1 up.
	c.handleKey("Backspace", "Backspace", false, mods)
	if got := r.countUp(VK_BACK); got != 1 {
		t.Fatalf("esperado 1 keyup no release, obtido %d", got)
	}
}

// TestLegacyPathForwardsAutoRepeat cobre o caminho real do viewer (JSON legado
// via HandleInput) end-to-end.
func TestLegacyPathForwardsAutoRepeat(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	// Segura a barra de espaço: press + 3 repeats + release.
	c.HandleInput(legacyKeyEvent("keydown", "Space", " ", false, false, false, false))
	for i := 0; i < 3; i++ {
		c.HandleInput(legacyKeyEvent("keydown", "Space", " ", false, false, false, false))
	}
	c.HandleInput(legacyKeyEvent("keyup", "Space", " ", false, false, false, false))

	if got := r.countDown(VK_SPACE); got != 4 {
		t.Fatalf("esperado 4 keydowns (1 press + 3 repeats), obtido %d", got)
	}
	if got := r.countUp(VK_SPACE); got != 1 {
		t.Fatalf("esperado 1 keyup, obtido %d", got)
	}
}

// TestShiftHeldModifierNotReinjected verifica que, segurando Shift+letra:
//   - o Shift é injetado UMA vez (repeats da letra não re-injetam o modificador);
//   - o keyup da letra NÃO libera o Shift ainda fisicamente pressionado;
//   - o keyup da própria tecla Shift é quem libera.
func TestShiftHeldModifierNotReinjected(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	shiftMods := InputModifiers{Shift: true}

	// Press Shift (evento próprio do modificador).
	c.handleKey("ShiftLeft", "Shift", true, shiftMods)
	if got := r.countDown(VK_SHIFT); got != 1 {
		t.Fatalf("esperado 1 shift down, obtido %d", got)
	}

	// Press 'a' com Shift ativo + repeats.
	c.handleKey("KeyA", "a", true, shiftMods)
	for i := 0; i < 4; i++ {
		c.handleKey("KeyA", "a", true, shiftMods)
	}
	if got := r.countDown(VK_SHIFT); got != 1 {
		t.Fatalf("shift não deve ser re-injetado nos repeats; esperado 1, obtido %d", got)
	}
	if got := r.countDown(vkA); got != 5 {
		t.Fatalf("esperado 5 'a' downs (1 press + 4 repeats), obtido %d", got)
	}

	// Keyup 'a' com Shift ainda ativo → 'a' sobe, Shift permanece.
	c.handleKey("KeyA", "a", false, shiftMods)
	if got := r.countUp(vkA); got != 1 {
		t.Fatalf("esperado 1 'a' up, obtido %d", got)
	}
	if got := r.countUp(VK_SHIFT); got != 0 {
		t.Fatalf("shift deve permanecer pressionado; obtido %d ups", got)
	}

	// Keyup da própria tecla Shift → libera.
	c.handleKey("ShiftLeft", "Shift", false, InputModifiers{})
	if got := r.countUp(VK_SHIFT); got != 1 {
		t.Fatalf("esperado 1 shift up no keyup do modificador, obtido %d", got)
	}
}

// TestSyncModifiersReleasesOnFlagsDrop cobre o caso em que o modificador foi
// injetado via flags (sem keydown próprio recebido — ex.: foco entrou no meio
// do hold) e os flags do keyup indicam que o modificador já não está ativo.
func TestSyncModifiersReleasesOnFlagsDrop(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	// 'a' com flag de Shift, sem keydown prévio de Shift.
	c.handleKey("KeyA", "a", true, InputModifiers{Shift: true})
	if got := r.countDown(VK_SHIFT); got != 1 {
		t.Fatalf("esperado shift down injetado via flags, obtido %d", got)
	}

	// Keyup 'a' sem Shift nos flags → shift liberado.
	c.handleKey("KeyA", "a", false, InputModifiers{})
	if got := r.countUp(VK_SHIFT); got != 1 {
		t.Fatalf("esperado shift up liberado pelos flags do keyup, obtido %d", got)
	}
}

// TestWatchdogReleasesStuckKey verifica que uma tecla "down" sem nenhum
// evento de teclado por keyStuckTimeout é liberada pelo watchdog (keyup
// perdido — viewer fechado/foco perdido), e que a tecla volta a funcionar.
func TestWatchdogReleasesStuckKey(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	c.handleKey("Backspace", "Backspace", true, InputModifiers{})

	deadline := time.Now().Add(3 * time.Second)
	for r.countUp(VK_BACK) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.countUp(VK_BACK); got == 0 {
		t.Fatal("watchdog não liberou a tecla presa (nenhum keyup injetado)")
	}

	c.mu.Lock()
	n := len(c.keysDown)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("keysDown deveria estar vazio após release do watchdog; restam %d", n)
	}

	// Tecla volta a funcionar: novo press injeta down novamente.
	before := r.countDown(VK_BACK)
	c.handleKey("Backspace", "Backspace", true, InputModifiers{})
	if after := r.countDown(VK_BACK); after <= before {
		t.Fatalf("esperado novo keydown após release do watchdog (antes=%d depois=%d)", before, after)
	}
}

// TestCloseReleasesKeys verifica que Close() libera teclas/modificadores
// pendentes e que o controller ignora eventos após fechado (idempotente).
func TestCloseReleasesKeys(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	c.handleKey("KeyA", "a", true, InputModifiers{Shift: true})
	if r.countDown(vkA) != 1 || r.countDown(VK_SHIFT) != 1 {
		t.Fatalf("estado inicial inesperado: a=%d shift=%d", r.countDown(vkA), r.countDown(VK_SHIFT))
	}

	c.Close()
	if got := r.countUp(vkA); got != 1 {
		t.Fatalf("esperado keyup de 'a' no Close, obtido %d", got)
	}
	if got := r.countUp(VK_SHIFT); got != 1 {
		t.Fatalf("esperado keyup de Shift no Close, obtido %d", got)
	}

	// Idempotente: Close extra não injeta mais keyups.
	c.Close()
	if got := r.countUp(vkA); got != 1 {
		t.Fatalf("Close deve ser idempotente; esperado 1 keyup, obtido %d", got)
	}

	// Eventos após Close são ignorados.
	c.handleKey("KeyA", "a", true, InputModifiers{})
	if got := r.countDown(vkA); got != 1 {
		t.Fatalf("eventos após Close devem ser ignorados; esperado 1 down, obtido %d", got)
	}
}

// TestRateLimitBypassForKeyUp garante que o budget de eventos/s nunca descarta
// um keyup (keyup perdido = tecla presa no remoto).
func TestRateLimitBypassForKeyUp(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	c.mu.Lock()
	c.maxEventsPerS = 2
	c.mu.Unlock()

	// 3 keydowns com budget 2 → o 3º é descartado.
	for i := 0; i < 3; i++ {
		c.HandleInput(legacyKeyEvent("keydown", "KeyA", "a", false, false, false, false))
	}
	if got := r.countDown(vkA); got != 2 {
		t.Fatalf("esperado 2 downs (3º descartado pelo rate limit), obtido %d", got)
	}

	// keyup com budget estourado passa mesmo assim.
	c.HandleInput(legacyKeyEvent("keyup", "KeyA", "a", false, false, false, false))
	if got := r.countUp(vkA); got != 1 {
		t.Fatalf("keyup não deve ser descartado pelo rate limit; esperado 1 up, obtido %d", got)
	}
}
