//go:build windows

package screen

import (
	"testing"
	"time"

	"discovery/app/core/winui"
)

func TestTrayElapsedClock(t *testing.T) {
	cases := map[time.Duration]string{
		0:                 "00:00",
		-3 * time.Second:  "00:00",
		5 * time.Second:   "00:05",
		61 * time.Second:  "01:01",
		600 * time.Second: "10:00",
	}
	for elapsed, want := range cases {
		if got := trayElapsedClock(elapsed); got != want {
			t.Errorf("trayElapsedClock(%s) = %q, want %q", elapsed, got, want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("truncateRunes curto = %q", got)
	}
	long := "áéíóúáéíóúáéíóú"
	got := truncateRunes(long, 5)
	if len([]rune(got)) != 5 {
		t.Fatalf("truncateRunes = %q (%d runes)", got, len([]rune(got)))
	}
}

func TestSessionTrayShowHideMantemIcone(t *testing.T) {
	tray, err := NewSessionTray(SessionTrayOptions{Operator: "Pedro", Text: "Sessão remota", AllowDisconnect: true})
	if err != nil {
		t.Skipf("bandeja indisponível neste ambiente: %v", err)
	}
	defer tray.Close()

	if err := tray.Show("Entrada bloqueada pelo suporte remoto"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	tray.mu.Lock()
	override := tray.override
	tray.mu.Unlock()
	if override != "Entrada bloqueada pelo suporte remoto" {
		t.Fatalf("override = %q", override)
	}
	if err := tray.Hide(); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	tray.mu.Lock()
	override = tray.override
	added := tray.added
	tray.mu.Unlock()
	if override != "" {
		t.Fatalf("override após Hide = %q", override)
	}
	if !added {
		t.Fatal("ícone não deveria ser removido no Hide (ele pertence à sessão)")
	}
}

func TestSessionTrayDisconnectCommand(t *testing.T) {
	called := make(chan struct{}, 1)
	tray, err := NewSessionTray(SessionTrayOptions{
		Operator: "Pedro", Text: "Sessão remota", AllowDisconnect: true,
		OnDisconnect: func() {
			select {
			case called <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Skipf("bandeja indisponível neste ambiente: %v", err)
	}
	defer tray.Close()

	winui.PostMessage(tray.hwnd, trayWMCommand, trayMenuDisconnect, 0)
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("comando do menu não disparou OnDisconnect")
	}
}

func TestSessionTrayDisconnectRespeitaPolitica(t *testing.T) {
	called := make(chan struct{}, 1)
	tray, err := NewSessionTray(SessionTrayOptions{
		Operator: "Pedro", Text: "Sessão remota", AllowDisconnect: false,
		OnDisconnect: func() {
			select {
			case called <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Skipf("bandeja indisponível neste ambiente: %v", err)
	}
	defer tray.Close()

	winui.PostMessage(tray.hwnd, trayWMCommand, trayMenuDisconnect, 0)
	select {
	case <-called:
		t.Fatal("allowDisconnect=false não pode desconectar")
	case <-time.After(300 * time.Millisecond):
	}
}
