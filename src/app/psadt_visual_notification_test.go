package app

import (
	"strings"
	"testing"
)

func TestBuildPSADTVisualScript_DialogBoxIncludesParameters(t *testing.T) {
	req := PSADTVisualNotificationRequest{
		NotifType:           "dialog_box",
		Title:               "Installation Notice",
		Message:             "Proceed with installation?",
		DialogButtons:       "YesNo",
		DialogDefault:       "Second",
		DialogIcon:          "Exclamation",
		DialogTimeout:       600,
		DialogNoWait:        true,
		DialogExitOnTimeout: true,
		DialogNotTopMost:    true,
		DialogForce:         true,
	}

	script, _ := buildPSADTVisualScript(req)
	if !strings.Contains(script, "Show-ADTDialogBox @dialogParams") {
		t.Fatalf("expected Show-ADTDialogBox invocation")
	}
	if !strings.Contains(script, "$dialogParams.Timeout = $psadtDialogTimeout") {
		t.Fatalf("expected timeout mapping")
	}
	if !strings.Contains(script, "$dialogParams.NoWait = $true") {
		t.Fatalf("expected no-wait mapping")
	}
	if !strings.Contains(script, "$dialogParams.ExitOnTimeout = $true") {
		t.Fatalf("expected ExitOnTimeout mapping")
	}
	if !strings.Contains(script, "$dialogParams.NotTopMost = $true") {
		t.Fatalf("expected NotTopMost mapping")
	}
	if !strings.Contains(script, "$dialogParams.Force = $true") {
		t.Fatalf("expected Force mapping")
	}
}

func TestNormalizeDialogHelpers(t *testing.T) {
	if got := normalizeDialogButtons("yesno"); got != "YesNo" {
		t.Fatalf("unexpected normalizeDialogButtons result: %s", got)
	}
	if got := normalizeDialogDefault("third"); got != "Third" {
		t.Fatalf("unexpected normalizeDialogDefault result: %s", got)
	}
	if got := normalizeDialogIcon("info"); got != "Information" {
		t.Fatalf("unexpected normalizeDialogIcon result: %s", got)
	}
	if got := boolEnvValue(true); got != "1" {
		t.Fatalf("expected boolEnvValue(true)=1")
	}
	if got := boolEnvValue(false); got != "0" {
		t.Fatalf("expected boolEnvValue(false)=0")
	}
}
