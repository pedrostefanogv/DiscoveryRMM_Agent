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

// PSADT 4.1.8: Show-ADTInstallationProgress NAO possui -WindowTitle.
func TestBuildPSADTVisualScript_ProgressUsesValidParameters(t *testing.T) {
	req := PSADTVisualNotificationRequest{
		NotifType:       "progress",
		Title:           "Discovery Agent",
		Message:         "Instalando...",
		Subtitle:        "Copiando arquivos",
		AppName:         "TestApp",
		DurationSeconds: 5,
	}

	script, _ := buildPSADTVisualScript(req)
	if strings.Contains(script, "-WindowTitle") {
		t.Fatalf("progress script must not use -WindowTitle (invalid in PSADT 4.1.8)")
	}
	if !strings.Contains(script, "Show-ADTInstallationProgress @progressParams") {
		t.Fatalf("expected splatted Show-ADTInstallationProgress invocation")
	}
	if !strings.Contains(script, "$progressParams.StatusMessageDetail = $psadtSubtitle") {
		t.Fatalf("expected StatusMessageDetail mapping")
	}
	if !strings.Contains(script, "Close-ADTInstallationProgress") {
		t.Fatalf("expected progress close after test duration")
	}
}

// PSADT 4.1.8: -Icon do Show-ADTInstallationPrompt usa DialogSystemIcon;
// 'Info' e invalido. O valor normalizado deve ser 'Information'.
func TestBuildPSADTVisualScript_PromptUsesValidSystemIcon(t *testing.T) {
	req := PSADTVisualNotificationRequest{
		NotifType:     "prompt_ok",
		Title:         "Discovery Agent",
		Message:       "Instalacao concluida",
		AppName:       "TestApp",
		PromptIcon:    "info",
		PromptTimeout: 60,
	}

	script, _ := buildPSADTVisualScript(req)
	if strings.Contains(script, "'Info'") {
		t.Fatalf("prompt script must not use invalid icon value 'Info'")
	}
	if !strings.Contains(script, "$promptParams.Icon = $psadtPromptIcon") {
		t.Fatalf("expected icon mapping via splatting")
	}
	if !strings.Contains(script, "$promptParams.Timeout = $psadtPromptTimeout") {
		t.Fatalf("expected timeout mapping")
	}
	if !strings.Contains(script, "Show-ADTInstallationPrompt @promptParams") {
		t.Fatalf("expected splatted Show-ADTInstallationPrompt invocation")
	}
}

func TestBuildPSADTVisualScript_PromptButtonsAndInput(t *testing.T) {
	yesno := PSADTVisualNotificationRequest{NotifType: "prompt_yesno", Title: "T", Message: "M", AppName: "A"}
	s, _ := buildPSADTVisualScript(yesno)
	if !strings.Contains(s, "$promptParams.ButtonLeftText = 'Sim'") || !strings.Contains(s, "$promptParams.ButtonRightText = 'Nao'") {
		t.Fatalf("expected yes/no prompt buttons")
	}

	input := PSADTVisualNotificationRequest{NotifType: "prompt_input", Title: "T", Message: "M", AppName: "A"}
	s, _ = buildPSADTVisualScript(input)
	if !strings.Contains(s, "$promptParams.RequestInput = $true") {
		t.Fatalf("expected RequestInput for prompt_input")
	}
}

func TestBuildPSADTVisualScript_BalloonTimeAndNoWait(t *testing.T) {
	req := PSADTVisualNotificationRequest{
		NotifType:          "balloon_warning",
		Title:              "Discovery Agent",
		Message:            "Reinicializacao necessaria",
		AppName:            "TestApp",
		BalloonTimeSeconds: 30,
		BalloonNoWait:      true,
	}

	script, _ := buildPSADTVisualScript(req)
	if !strings.Contains(script, "$balloonParams.BalloonTipTime = $psadtBalloonTime * 1000") {
		t.Fatalf("expected BalloonTipTime mapping")
	}
	if !strings.Contains(script, "$balloonParams.NoWait = $true") {
		t.Fatalf("expected NoWait mapping")
	}
	if !strings.Contains(script, "BalloonTipIcon = 'Warning'") {
		t.Fatalf("expected warning balloon icon")
	}
}

func TestBuildPSADTVisualScript_RestartAndWelcome(t *testing.T) {
	restart := PSADTVisualNotificationRequest{NotifType: "restart_prompt", Title: "T", Message: "M", AppName: "A", RestartCountdownSeconds: 120}
	s, _ := buildPSADTVisualScript(restart)
	if !strings.Contains(s, "Show-ADTInstallationRestartPrompt @restartParams") {
		t.Fatalf("expected restart prompt invocation")
	}
	if !strings.Contains(s, "$restartParams.CountdownSeconds = $psadtRestartCountdown") {
		t.Fatalf("expected countdown mapping")
	}

	welcome := PSADTVisualNotificationRequest{
		NotifType:      "welcome",
		Title:          "T",
		Message:        "M",
		AppName:        "A",
		CloseProcesses: "winword,excel",
		AllowDefer:     true,
		DeferTimes:     3,
		BlockExecution: true,
	}
	s, _ = buildPSADTVisualScript(welcome)
	if !strings.Contains(s, "Show-ADTInstallationWelcome @welcomeParams") {
		t.Fatalf("expected welcome invocation")
	}
	if !strings.Contains(s, "$welcomeParams.CloseProcesses = $welcomeProcs") {
		t.Fatalf("expected close processes mapping")
	}
	if !strings.Contains(s, "$welcomeParams.AllowDefer = $true") {
		t.Fatalf("expected defer mapping")
	}
	if !strings.Contains(s, "$welcomeParams.BlockExecution = $true") {
		t.Fatalf("expected block execution mapping")
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

// Os valores retornados devem ser membros validos do enum DialogSystemIcon
// do PSADT 4.x (Application, Asterisk, Error, Exclamation, Hand, Information,
// Question, Shield, Warning, WinLogo).
func TestNormalizePromptIcon(t *testing.T) {
	valid := map[string]bool{
		"Application": true, "Asterisk": true, "Error": true, "Exclamation": true,
		"Hand": true, "Information": true, "Question": true, "Shield": true,
		"Warning": true, "WinLogo": true, "": true,
	}
	cases := map[string]string{
		"":            "",
		"none":        "",
		"info":        "Information",
		"information": "Information",
		"warning":     "Exclamation",
		"error":       "Error",
		"question":    "Question",
		"shield":      "Shield",
		"hand":        "Hand",
		"asterisk":    "Asterisk",
		"application": "Application",
		"winlogo":     "WinLogo",
		"desconhecido": "Information",
	}
	for in, want := range cases {
		if got := normalizePromptIcon(in); got != want {
			t.Fatalf("normalizePromptIcon(%q) = %q, want %q", in, got, want)
		}
		if !valid[want] {
			t.Fatalf("normalizePromptIcon(%q) = %q, que nao e membro do enum DialogSystemIcon", in, want)
		}
	}
}
