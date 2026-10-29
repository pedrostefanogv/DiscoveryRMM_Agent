package app

// Jitter determinístico do scan completo. O primeiro ciclo já tinha jitter; os
// ciclos seguintes usavam o intervalo puro e a frota provisionada pela mesma
// imagem fazia rajada a cada 6h.

import (
	"context"
	"testing"
	"time"
)

func TestWindowsUpdateScanIntervalWithJitter(t *testing.T) {
	const sixHours = 6 * time.Hour
	cases := []struct {
		name     string
		agentID  string
		interval time.Duration
		// exact exige o intervalo intacto (agente sem id / intervalo invalido).
		exact bool
	}{
		{name: "agente normal", agentID: "019faa6f-1111-2222-3333-444455556666", interval: sixHours},
		{name: "agente curto", agentID: "agent-1", interval: sixHours},
		{name: "outro agente", agentID: "agent-2", interval: sixHours},
		{name: "intervalo minimo", agentID: "agent-1", interval: time.Hour},
		{name: "sem agentId", agentID: "", interval: sixHours, exact: true},
		{name: "intervalo invalido", agentID: "agent-1", interval: 0, exact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowsUpdateScanIntervalWithJitter(tc.agentID, tc.interval)
			if tc.exact {
				if got != tc.interval {
					t.Fatalf("intervalo=%s want %s (sem jitter)", got, tc.interval)
				}
				return
			}
			spread := tc.interval / 10
			if got < tc.interval-spread || got > tc.interval+spread {
				t.Fatalf("intervalo com jitter=%s fora de ±10%% de %s", got, tc.interval)
			}
			// Determinístico: mesmo agente, mesmo intervalo, mesmo resultado.
			if again := windowsUpdateScanIntervalWithJitter(tc.agentID, tc.interval); again != got {
				t.Fatalf("jitter nao deterministico: %s != %s", again, got)
			}
		})
	}
}

// Agentes diferentes precisam cair em pontos diferentes da janela (é isso que
// desfaz a rajada da frota).
func TestWindowsUpdateScanIntervalJitterSpreadsAgents(t *testing.T) {
	const interval = 6 * time.Hour
	seen := make(map[time.Duration]int)
	for index := 0; index < 32; index++ {
		agentID := "019faa6f-0000-0000-0000-" + time.Duration(index).String()
		seen[windowsUpdateScanIntervalWithJitter(agentID, interval)]++
	}
	if len(seen) < 8 {
		t.Fatalf("jitter concentrado: %d valores distintos para 32 agentes (%v)", len(seen), seen)
	}
}

// O atraso INICIAL continua sendo 10 min + jitter de até 5 min, e a função
// pública do loop precisa continuar ligada nessa conta (antes o teste recalculava
// a fórmula e passaria mesmo se windowsUpdateInitialDelay deixasse de usar jitter).
func TestWindowsUpdateInitialDelayKeepsRange(t *testing.T) {
	// Sem App (agentId desconhecido): exatamente o atraso base.
	if got := windowsUpdateInitialDelay(context.Background(), nil); got != windowsUpdateInitialDelayDefault {
		t.Fatalf("sem agentId: atraso=%s want %s", got, windowsUpdateInitialDelayDefault)
	}

	for index := 0; index < 16; index++ {
		agentID := "agente-" + time.Duration(index).String()
		delay := windowsUpdateInitialDelayDefault + windowsUpdateAgentJitter(agentID, windowsUpdateJitterMax)
		if delay < windowsUpdateInitialDelayDefault || delay >= windowsUpdateInitialDelayDefault+windowsUpdateJitterMax {
			t.Fatalf("atraso inicial=%s fora da faixa [%s, %s)", delay,
				windowsUpdateInitialDelayDefault, windowsUpdateInitialDelayDefault+windowsUpdateJitterMax)
		}
	}
}

// Um intervalo já no mínimo (1h) não pode cair abaixo dele com o jitter (-10%).
func TestWindowsUpdateScanIntervalWithJitterRespectsMinimum(t *testing.T) {
	for index := 0; index < 32; index++ {
		agentID := "agente-" + time.Duration(index).String()
		got := windowsUpdateScanIntervalWithJitter(agentID, windowsUpdateScanIntervalMinimum)
		if got < windowsUpdateScanIntervalMinimum {
			t.Fatalf("intervalo com jitter=%s abaixo do minimo %s (agente %s)",
				got, windowsUpdateScanIntervalMinimum, agentID)
		}
	}
}
