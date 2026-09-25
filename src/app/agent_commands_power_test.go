package app

import "testing"

func TestResolvePowerStrategy(t *testing.T) {
	// notifyUser=true, force=false -> notificacao adiavel.
	s := resolvePowerStrategy(powerCommandPayload{NotifyUser: true, DelaySeconds: 15})
	if s.Mode != powerModeNotify {
		t.Fatalf("mode=%d, want notify", s.Mode)
	}
	if !s.Deferrable {
		t.Fatalf("esperava Deferrable=true sem force")
	}
	if s.DelaySeconds != 15 {
		t.Fatalf("delay=%d, want 15", s.DelaySeconds)
	}

	// notifyUser=true, force=true -> notificacao NAO adiavel.
	s = resolvePowerStrategy(powerCommandPayload{NotifyUser: true, Force: true})
	if s.Deferrable {
		t.Fatalf("esperava Deferrable=false com force")
	}
	if !s.Force {
		t.Fatalf("esperava Force=true")
	}

	// notifyUser=false -> timer interno (sem dialogo nativo); nao ha nada a adiar.
	s = resolvePowerStrategy(powerCommandPayload{NotifyUser: false, DelaySeconds: 30})
	if s.Mode != powerModeSilentTimer {
		t.Fatalf("mode=%d, want silent-timer", s.Mode)
	}
	if s.Deferrable {
		t.Fatalf("silent-timer nao tem notificacao; Deferrable deveria ser false")
	}
}
