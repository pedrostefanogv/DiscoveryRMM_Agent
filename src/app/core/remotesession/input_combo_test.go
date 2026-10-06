//go:build windows

package remotesession

import (
	"errors"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// orderRecorder registra a ORDEM das injeções de teclado (o stubRecorder de
// input_controller_test.go só conta), necessária para validar que uma
// combinação pressiona na ordem e libera na ordem inversa.
type orderRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *orderRecorder) keyDown(vk uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "down:0x"+strings.ToUpper(hex2(vk)))
	return nil
}

func (r *orderRecorder) keyUp(vk uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "up:0x"+strings.ToUpper(hex2(vk)))
	return nil
}

func (r *orderRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

// hex2 formata um VK em hexadecimal sem importar fmt só para isto.
func hex2(v uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[(v>>4)&0xF], digits[v&0xF]})
}

// installOrderStub substitui os pontos de injeção de teclado por gravadores de
// ordem e restaura no fim do teste.
func installOrderStub(t *testing.T) *orderRecorder {
	t.Helper()
	r := &orderRecorder{}
	origDown, origUp := injectKeyDown, injectKeyUp
	injectKeyDown = r.keyDown
	injectKeyUp = r.keyUp
	t.Cleanup(func() {
		injectKeyDown = origDown
		injectKeyUp = origUp
	})
	return r
}

func TestNormalizeSpecialKey(t *testing.T) {
	cases := map[string]string{
		"CTRL+ALT+DEL":             "ctrl+alt+del",
		" control + alt + delete ": "ctrl+alt+del",
		"Control+Alt+Delete":       "ctrl+alt+del",
		"Win+L":                    "win+l",
		"windows+l":                "win+l",
		"Alt+Tab":                  "alt+tab",
		"PrintScreen":              "printscreen",
		"prtsc":                    "printscreen",
		"PrtScrn":                  "printscreen",
		"":                         "",
	}
	for in, want := range cases {
		if got := normalizeSpecialKey(in); got != want {
			t.Errorf("normalizeSpecialKey(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestIsSupportedSpecialKey(t *testing.T) {
	supported := []string{
		"ctrl+alt+del", "ctrl+alt+delete", "win+l", "ctrl+shift+esc",
		"alt+tab", "alt+shift+tab", "alt+f4", "ctrl+esc",
		"win", "win+d", "win+e", "win+r", "win+x", "win+tab",
		"printscreen", "alt+printscreen",
	}
	for _, id := range supported {
		if !IsSupportedSpecialKey(id) {
			t.Errorf("%q deveria estar na allow-list", id)
		}
	}
	// ctrl+alt+end saiu da lista: só é SAS DENTRO de uma sessão RDP, e o worker
	// sempre injeta na sessão do console (WTSGetActiveConsoleSessionId) — o item
	// não teria efeito nenhum e poluía o menu.
	for _, id := range []string{"", "combo inexistente", "ctrl+alt+f13", "ctrl+alt+end", "shutdown"} {
		if IsSupportedSpecialKey(id) {
			t.Errorf("%q NAO deveria estar na allow-list", id)
		}
	}
}

func TestInjectSpecialKey_SequencePressAndReleaseOrder(t *testing.T) {
	r := installOrderStub(t)

	method, err := InjectSpecialKey("ctrl+shift+esc")
	if err != nil {
		t.Fatalf("InjectSpecialKey: %v", err)
	}
	if method != comboMethodSequence {
		t.Fatalf("metodo = %q, esperado %q", method, comboMethodSequence)
	}
	want := []string{"down:0x11", "down:0x10", "down:0x1B", "up:0x1B", "up:0x10", "up:0x11"}
	got := r.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ordem de injecao inesperada:\n got=%v\nwant=%v", got, want)
	}
}

func TestInjectSpecialKey_UnknownRejectedWithoutInjection(t *testing.T) {
	r := installOrderStub(t)

	if _, err := InjectSpecialKey("ctrl+alt+f13"); err == nil {
		t.Fatal("combinacao desconhecida deveria retornar erro")
	}
	if len(r.snapshot()) != 0 {
		t.Fatalf("nenhuma injecao deveria ocorrer, ocorreram: %v", r.snapshot())
	}
}

func TestInjectSpecialKey_SASUsesDedicatedAPI(t *testing.T) {
	origSAS := injectSendSAS
	t.Cleanup(func() { injectSendSAS = origSAS })

	// Sem stub: a injecao de teclado nao pode ser tocada pelo caminho SAS.
	r := installOrderStub(t)
	called := 0
	injectSendSAS = func() error {
		called++
		return nil
	}

	method, err := InjectSpecialKey("Control+Alt+Delete")
	if err != nil {
		t.Fatalf("InjectSpecialKey(ctrl+alt+del): %v", err)
	}
	if method != comboMethodSAS || called != 1 {
		t.Fatalf("SAS: method=%q called=%d", method, called)
	}
	if len(r.snapshot()) != 0 {
		t.Fatalf("SAS nao deve usar SendInput, eventos=%v", r.snapshot())
	}
}

func TestInjectSpecialKey_PropagatesSASError(t *testing.T) {
	origSAS := injectSendSAS
	t.Cleanup(func() { injectSendSAS = origSAS })

	wantErr := errors.New("SoftwareSASGeneration desabilitada")
	injectSendSAS = func() error { return wantErr }

	method, err := InjectSpecialKey("ctrl+alt+del")
	if method != comboMethodSAS {
		t.Fatalf("method=%q", method)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("erro do SendSAS deveria ser propagado, got=%v", err)
	}
}

func TestInjectSpecialKey_LockUsesDedicatedAPI(t *testing.T) {
	origLock, origSAS := injectLockWorkstation, injectSendSAS
	t.Cleanup(func() { injectLockWorkstation, injectSendSAS = origLock, origSAS })

	lockCalled, sasCalled := 0, 0
	injectLockWorkstation = func() error { lockCalled++; return nil }
	injectSendSAS = func() error { sasCalled++; return nil }

	method, err := InjectSpecialKey("win+l")
	if err != nil {
		t.Fatalf("InjectSpecialKey(win+l): %v", err)
	}
	if method != comboMethodLock || lockCalled != 1 || sasCalled != 0 {
		t.Fatalf("lock: method=%q lock=%d sas=%d", method, lockCalled, sasCalled)
	}
}

// Garante que uma falha no meio da sequencia libera as teclas ja pressionadas
// (sem tecla "presa" no remoto).
func TestInjectSpecialKey_ReleasesPressedKeysOnFailure(t *testing.T) {
	origDown, origUp := injectKeyDown, injectKeyUp
	t.Cleanup(func() { injectKeyDown, injectKeyUp = origDown, origUp })

	var ups []uint16
	failAt := 2 // segunda tecla (Shift) falha
	calls := 0
	injectKeyDown = func(vk uint16) error {
		calls++
		if calls == failAt {
			return errors.New("falha simulada no keydown")
		}
		return nil
	}
	injectKeyUp = func(vk uint16) error {
		ups = append(ups, vk)
		return nil
	}

	if _, err := InjectSpecialKey("ctrl+shift+esc"); err == nil {
		t.Fatal("falha no keydown deveria retornar erro")
	}
	if len(ups) != 1 || ups[0] != VK_CONTROL {
		t.Fatalf("apenas o Ctrl ja pressionado deveria ser liberado, ups=%v", ups)
	}
}

func TestHandleInput_LegacyKeyComboInvokesHandler(t *testing.T) {
	c := newTestController(t)
	installKeyboardStub(t)

	var gotCombo, gotMethod string
	var gotErr error
	called := 0
	c.SetSpecialKeyHandler(func(combo, method string, err error) {
		called++
		gotCombo, gotMethod, gotErr = combo, method, err
	})

	c.HandleInput([]byte("{\"type\":\"keycombo\",\"combo\":\"alt+tab\"}"))

	if called != 1 || gotCombo != "alt+tab" || gotMethod != comboMethodSequence || gotErr != nil {
		t.Fatalf("handler: called=%d combo=%q method=%q err=%v", called, gotCombo, gotMethod, gotErr)
	}
}

func TestHandleInput_V1KeyComboInvokesHandler(t *testing.T) {
	c := newTestController(t)
	installKeyboardStub(t)

	var gotCombo, gotMethod string
	var gotErr error
	c.SetSpecialKeyHandler(func(combo, method string, err error) {
		gotCombo, gotMethod, gotErr = combo, method, err
	})

	c.HandleInput([]byte("{\"version\":1,\"type\":\"key.combo\",\"combo\":\"printscreen\"}"))

	if gotCombo != "printscreen" || gotMethod != comboMethodSequence || gotErr != nil {
		t.Fatalf("handler v1: combo=%q method=%q err=%v", gotCombo, gotMethod, gotErr)
	}
}

// Regressão: um releaseAllKeys do watchdog NÃO pode se intrometer no meio de
// uma combinação especial (ver injectMu em input_controller.go). O teste segura
// a injeção no meio da combinação, dispara o release em paralelo e verifica que
// nenhum keyup sai antes de a combinação terminar.
func TestReleaseAllKeysWaitsForCombo(t *testing.T) {
	origDown, origUp := injectKeyDown, injectKeyUp
	t.Cleanup(func() { injectKeyDown, injectKeyUp = origDown, origUp })

	var mu sync.Mutex
	var events []string
	comboStarted := make(chan struct{})
	releaseCombo := make(chan struct{})
	// Garante que o teste nunca deixe a combinação presa (mesmo com t.Fatal).
	defer func() {
		select {
		case <-releaseCombo:
		default:
			close(releaseCombo)
		}
	}()

	injectKeyDown = func(vk uint16) error {
		if vk == VK_MENU {
			select {
			case <-comboStarted:
			default:
				close(comboStarted)
			}
			<-releaseCombo // segura a combinação no meio (injectMu em mãos)
		}
		mu.Lock()
		events = append(events, "down")
		mu.Unlock()
		return nil
	}
	injectKeyUp = func(vk uint16) error {
		mu.Lock()
		events = append(events, "up")
		mu.Unlock()
		return nil
	}

	c := newTestController(t)
	// Viewer segura Shift: fica rastreado para o release ter o que liberar.
	c.HandleInput(legacyKeyEvent("keydown", "ShiftLeft", "Shift", false, true, false, false))
	mu.Lock()
	events = nil // ignora o down do Shift; interessa o que vier agora
	mu.Unlock()

	comboDone := make(chan struct{})
	go func() {
		c.HandleInput([]byte("{\"type\":\"keycombo\",\"combo\":\"alt+tab\"}"))
		close(comboDone)
	}()

	<-comboStarted // combinação no meio: injectMu adquirido

	releaseDone := make(chan struct{})
	go func() {
		c.releaseAllKeys("teste")
		close(releaseDone)
	}()

	select {
	case <-releaseDone:
		t.Fatal("releaseAllKeys NAO deveria concluir durante a combinacao")
	case <-time.After(80 * time.Millisecond):
	}

	mu.Lock()
	n := len(events)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("nenhuma injecao deveria ocorrer durante o bloqueio, obtidas %d", n)
	}

	close(releaseCombo) // libera a combinação
	<-comboDone
	<-releaseDone

	mu.Lock()
	total := len(events)
	mu.Unlock()
	// 2 downs (Alt, Tab) + 2 ups (Tab, Alt) da combinação + 1 up do Shift.
	if total != 5 {
		t.Fatalf("esperado 5 injecoes (2 down + 3 up), obtidas %d", total)
	}
}

func TestSupportedSpecialKeysSortedAndVirtualKeys(t *testing.T) {
	keys := SupportedSpecialKeys()
	if len(keys) != len(specialKeyCombos) {
		t.Fatalf("SupportedSpecialKeys devolveu %d ids, esperado %d", len(keys), len(specialKeyCombos))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("lista nao esta ordenada: %q antes de %q", keys[i-1], keys[i])
		}
	}

	// Combinações SendInput expõem os VKs; as dedicadas não.
	vks := SpecialKeyVirtualKeys("ctrl+shift+esc")
	if len(vks) != 3 || vks[0] != VK_CONTROL || vks[2] != VK_ESCAPE {
		t.Fatalf("VKs inesperados: %v", vks)
	}
	for _, dedicated := range []string{"ctrl+alt+del", "win+l"} {
		if got := SpecialKeyVirtualKeys(dedicated); len(got) != 0 {
			t.Fatalf("%s nao deveria expor VKs, got=%v", dedicated, got)
		}
	}
	if got := SpecialKeyVirtualKeys("nao-existe"); got != nil {
		t.Fatalf("combinacao desconhecida deveria devolver nil, got=%v", got)
	}
}

func TestInjectSpecialKey_ReleasesAllKeysWhenKeyUpFails(t *testing.T) {
	origDown, origUp := injectKeyDown, injectKeyUp
	t.Cleanup(func() { injectKeyDown, injectKeyUp = origDown, origUp })

	var ups []uint16
	injectKeyDown = func(vk uint16) error { return nil }
	calls := 0
	injectKeyUp = func(vk uint16) error {
		calls++
		ups = append(ups, vk)
		if calls == 1 {
			return errors.New("falha simulada no primeiro keyup")
		}
		return nil
	}

	if _, err := InjectSpecialKey("ctrl+shift+esc"); err == nil {
		t.Fatal("keyup com falha deveria retornar erro")
	}
	if len(ups) != 3 {
		t.Fatalf("todos os keyups deveriam ser tentados, ups=%v", ups)
	}
	if ups[0] != VK_ESCAPE || ups[1] != VK_SHIFT || ups[2] != VK_CONTROL {
		t.Fatalf("ordem de liberacao inesperada: %v", ups)
	}
}

// Regressao: o viewer segura Ctrl e o operador envia Ctrl+Shift+Esc. A
// combinacao injeta o up do Ctrl; o rastreamento NAO pode continuar dizendo que
// o Ctrl esta ativo (senao Ctrl+letra vira letra solta ate o watchdog).
func TestHandleCombo_ForgetsViewerHeldModifier(t *testing.T) {
	c := newTestController(t)
	r := installKeyboardStub(t)

	// Viewer segura Ctrl: rastreamento marca modsActive e injeta o down.
	c.HandleInput(legacyKeyEvent("keydown", "ControlLeft", "Control", true, false, false, false))
	if got := r.countDown(VK_CONTROL); got != 1 {
		t.Fatalf("esperado 1 keydown de Ctrl, obtido %d", got)
	}

	// Combinação que usa Ctrl (forget + injeta Ctrl/Shift/Esc).
	c.HandleInput([]byte("{\"type\":\"keycombo\",\"combo\":\"ctrl+shift+esc\"}"))

	c.mu.Lock()
	trackedDown := len(c.keysDown)
	trackedMods := len(c.modsActive)
	c.mu.Unlock()
	if trackedDown != 0 || trackedMods != 0 {
		t.Fatalf("rastreamento deveria estar limpo apos a combinacao: keysDown=%d modsActive=%d", trackedDown, trackedMods)
	}

	// Próximo keydown do viewer com Ctrl re-injeta o modificador (auto-cura).
	c.HandleInput(legacyKeyEvent("keydown", "KeyA", "a", true, false, false, false))
	if got := r.countDown(VK_CONTROL); got != 3 {
		t.Fatalf("Ctrl deveria ser re-injetado apos a combinacao (1 viewer + 1 combo + 1 re-sync), keydowns=%d", got)
	}
}

// TestSpecialKeyIDsParityWithViewer garante que a allow-list do agent e a do
// viewer (specialKeys.ts) continuam identicas. A lista existe dos dois lados de
// proposito (viewer exibe, agent valida); renomear um id so de um lado passa
// nos testes unitarios de cada lado e falha em runtime. O teste e skipado
// quando o checkout do site nao esta ao lado (ex.: CI que roda so o agent).
func TestSpecialKeyIDsParityWithViewer(t *testing.T) {
	const viewerPath = "../../../../../DiscoveryRMM_Site/src/modules/remote-screen/specialKeys.ts"
	raw, err := os.ReadFile(viewerPath)
	if err != nil {
		t.Skipf("viewer de teclas especiais ausente neste checkout (%v) — paridade nao verificada", err)
	}

	matches := regexp.MustCompile("id: '([^']+)'").FindAllStringSubmatch(string(raw), -1)
	viewerIDs := make([]string, 0, len(matches))
	for _, m := range matches {
		viewerIDs = append(viewerIDs, m[1])
	}
	if len(viewerIDs) == 0 {
		t.Fatalf("nenhum id encontrado em %s", viewerPath)
	}
	sort.Strings(viewerIDs)

	agentIDs := SupportedSpecialKeys() // ja ordenado
	if !reflect.DeepEqual(viewerIDs, agentIDs) {
		t.Fatalf("allow-lists divergentes:\n viewer=%v\n agent =%v", viewerIDs, agentIDs)
	}
}
