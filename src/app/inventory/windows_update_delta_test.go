package inventory

import (
	"encoding/json"
	"testing"

	"discovery/app/core/windowsupdate"
)

func previousEnvelope(t *testing.T, updates ...windowsupdate.Update) []byte {
	t.Helper()
	envelope := agentWindowsUpdateEnvelope{
		AgentID:     "agent-1",
		CollectedAt: "2026-10-29T10:00:00Z",
		Scan:        agentWindowsUpdateScan{Status: "ok", Trigger: "periodic"},
		Updates:     updates,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return payload
}

func deltaUpdate(key, title string) windowsupdate.Update {
	return windowsupdate.Update{UpdateKey: key, Title: title, Severity: "Important"}
}

func TestPlanDeltaNeedsPreviousPayload(t *testing.T) {
	plan := PlanWindowsUpdateDelta([]windowsupdate.Update{deltaUpdate("a", "A")}, nil, "change")
	if plan.UseDelta {
		t.Fatal("sem payload anterior nao ha delta")
	}
}

func TestPlanDeltaOnlyForChangeTrigger(t *testing.T) {
	previous := previousEnvelope(t, deltaUpdate("a", "A"))
	plan := PlanWindowsUpdateDelta([]windowsupdate.Update{deltaUpdate("a", "A")}, previous, "periodic")
	if plan.UseDelta {
		t.Fatal("periodic sempre manda inventario completo")
	}
}

func TestPlanDeltaDetectsChangeAndRemoval(t *testing.T) {
	previous := previousEnvelope(t,
		deltaUpdate("a", "A"), deltaUpdate("b", "B"), deltaUpdate("c", "C"),
		deltaUpdate("e", "E"), deltaUpdate("f", "F"))
	current := []windowsupdate.Update{
		deltaUpdate("a", "A"), deltaUpdate("b", "B-v2"),
		deltaUpdate("e", "E"), deltaUpdate("f", "F"), deltaUpdate("d", "D")}

	plan := PlanWindowsUpdateDelta(current, previous, "change")
	if !plan.UseDelta {
		t.Fatal("mudanca minoritaria deveria usar delta")
	}
	if len(plan.Changed) != 2 {
		t.Fatalf("esperava 2 alterados (b,d); got %+v", plan.Changed)
	}
	if len(plan.Removed) != 1 || plan.Removed[0] != "c" {
		t.Fatalf("esperava remocao de c; got %v", plan.Removed)
	}
}

func TestPlanDeltaFallsBackToFullOnMajorityChange(t *testing.T) {
	previous := previousEnvelope(t, deltaUpdate("a", "A"), deltaUpdate("b", "B"))
	current := []windowsupdate.Update{deltaUpdate("a", "A2"), deltaUpdate("b", "B2")}
	plan := PlanWindowsUpdateDelta(current, previous, "change")
	if plan.UseDelta {
		t.Fatal("50% ou mais alterado deveria mandar o inventario completo")
	}
}

func TestPlanDeltaNoChangeIsEmptyDelta(t *testing.T) {
	previous := previousEnvelope(t, deltaUpdate("a", "A"), deltaUpdate("b", "B"))
	current := []windowsupdate.Update{deltaUpdate("a", "A"), deltaUpdate("b", "B")}
	plan := PlanWindowsUpdateDelta(current, previous, "change")
	if !plan.UseDelta || len(plan.Changed) != 0 || len(plan.Removed) != 0 {
		t.Fatalf("sem mudanca o delta deve ser vazio; got %+v", plan)
	}
}
