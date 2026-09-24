package agentcommands

import "testing"

func TestParsePowerCommandPayload_NotifyUserDefaultsToTrue(t *testing.T) {
	// Ausente (API antiga) = comportamento legado: notificar.
	p := ParsePowerCommandPayload(map[string]any{"delaySeconds": float64(20), "force": true})
	if !p.NotifyUser {
		t.Fatalf("notifyUser ausente deveria assumir true (legado), veio false")
	}
	if p.DelaySeconds != 20 || !p.Force {
		t.Fatalf("campos basicos nao parseados: %+v", p)
	}
}

func TestParsePowerCommandPayload_NotifyUserFalse(t *testing.T) {
	p := ParsePowerCommandPayload(map[string]any{"notifyUser": false, "delaySeconds": float64(30)})
	if p.NotifyUser {
		t.Fatalf("notifyUser=false deveria ser respeitado")
	}
}

func TestParsePowerCommandPayload_NotifyUserTrue(t *testing.T) {
	p := ParsePowerCommandPayload(map[string]any{"notifyUser": true})
	if !p.NotifyUser {
		t.Fatalf("notifyUser=true deveria ser respeitado")
	}
}
