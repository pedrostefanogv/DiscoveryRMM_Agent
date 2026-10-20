package app

import (
	"testing"
	"time"
)

// Gatilhos concorrentes (adoção de estado + evento IPC) logavam e
// reapresentavam o convite no mesmo milissegundo (18:33:44.366, duas linhas
// idênticas nos logs). O check-and-mark atômico garante uma só reapresentação.
func TestAllowSupportChatReinvite_UmaVezNaJanelaDeTTL(t *testing.T) {
	a := &App{}
	if !a.allowSupportChatReinvite("s1") {
		t.Fatal("primeira chamada deveria passar")
	}
	if a.allowSupportChatReinvite("s1") {
		t.Fatal("segunda chamada na mesma janela deveria ser suprimida")
	}
	// Sessão diferente não é afetada.
	if !a.allowSupportChatReinvite("s2") {
		t.Fatal("sessão diferente deveria passar")
	}
	// Entrada expirada libera nova reapresentação.
	a.supportChatNotifyMu.Lock()
	a.supportChatNotifyReinvited["s1"] = time.Now().Add(-supportChatReinviteDedupTTL - time.Second)
	a.supportChatNotifyMu.Unlock()
	if !a.allowSupportChatReinvite("s1") {
		t.Fatal("entrada expirada deveria liberar reapresentação")
	}
	// Poda: entradas com mais de 1 min saem do mapa.
	a.supportChatNotifyMu.Lock()
	a.supportChatNotifyReinvited["s3"] = time.Now().Add(-2 * time.Minute)
	a.supportChatNotifyMu.Unlock()
	_ = a.allowSupportChatReinvite("s4")
	a.supportChatNotifyMu.Lock()
	_, exists := a.supportChatNotifyReinvited["s3"]
	a.supportChatNotifyMu.Unlock()
	if exists {
		t.Fatal("entrada velha deveria ter sido podada")
	}
}

func TestAllowSupportChatReinvite_ArgumentosInvalidos(t *testing.T) {
	a := &App{}
	if a.allowSupportChatReinvite("") {
		t.Fatal("sessão vazia deveria recusar")
	}
	var nilApp *App
	if nilApp.allowSupportChatReinvite("s1") {
		t.Fatal("App nil deveria recusar")
	}
}
