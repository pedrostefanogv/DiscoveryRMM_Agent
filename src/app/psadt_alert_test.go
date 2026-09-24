//go:build windows

package app

import "testing"

func TestMapPSADTPromptResult_MatchesActionLabelOrValue(t *testing.T) {
	p := PsadtAlertPayload{Actions: []PsadtAlertAction{
		{Label: "Sim", Value: "restart_now"},
		{Label: "Nao", Value: "defer"},
	}}

	if got := mapPSADTPromptResult("Sim", p); got != "restart_now" {
		t.Fatalf("label -> value: got %q, want restart_now", got)
	}
	if got := mapPSADTPromptResult("defer", p); got != "defer" {
		t.Fatalf("value -> value: got %q, want defer", got)
	}
}

func TestMapPSADTPromptResult_TimeoutAndEmptyUseDefaultAction(t *testing.T) {
	p := PsadtAlertPayload{DefaultAction: "timeout_default"}

	if got := mapPSADTPromptResult("Timeout", p); got != "timeout_default" {
		t.Fatalf("timeout + default action: got %q", got)
	}
	if got := mapPSADTPromptResult("  ", p); got != "timeout_default" {
		t.Fatalf("empty + default action: got %q", got)
	}
}

func TestMapPSADTPromptResult_DefaultsToOk(t *testing.T) {
	if got := mapPSADTPromptResult("OK", PsadtAlertPayload{}); got != "ok" {
		t.Fatalf("OK without actions: got %q, want ok", got)
	}
	if got := mapPSADTPromptResult("Timeout", PsadtAlertPayload{}); got != "timeout" {
		t.Fatalf("timeout without default: got %q, want timeout", got)
	}
}

func TestPsadtActionLabel_FallsBackToValue(t *testing.T) {
	if got := psadtActionLabel(PsadtAlertAction{Value: "v"}); got != "v" {
		t.Fatalf("label empty: got %q, want v", got)
	}
	if got := psadtActionLabel(PsadtAlertAction{Label: "  L  ", Value: "v"}); got != "L" {
		t.Fatalf("label trim: got %q, want L", got)
	}
}
