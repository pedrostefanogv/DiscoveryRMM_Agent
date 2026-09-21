package app

import (
	"os"
	"path/filepath"
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
	if !strings.Contains(s, "PSADT_RESULT=") {
		t.Fatalf("expected structured PSADT_RESULT marker for input prompt")
	}
}

// A resposta do dialogo deve ser extraida do marcador PSADT_RESULT=.
func TestExtractVisualDialogResult(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "input dialog com texto digitado",
			output: "Resultado: PSADT.UserInterface.DialogResults.InputDialogResult\nPSADT_RESULT={\"Result\":\"Submit\",\"Text\":\"meu texto aqui\"}",
			want:   "meu texto aqui",
		},
		{
			name:   "input dialog sem texto",
			output: "PSADT_RESULT={\"Result\":\"Ok\"}",
			want:   "Ok",
		},
		{
			name:   "dialog box string simples",
			output: "PSADT_RESULT=\"Yes\"",
			want:   "Yes",
		},
		{
			name:   "sem marcador",
			output: "BalloonTip exibido com sucesso\nExitCode: 0",
			want:   "",
		},
		{
			name:   "marcador com null",
			output: "PSADT_RESULT=null",
			want:   "",
		},
	}
	for _, tc := range cases {
		if got := extractVisualDialogResult(tc.output); got != tc.want {
			t.Errorf("%s: extractVisualDialogResult() = %q, want %q", tc.name, got, tc.want)
		}
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
		"":             "",
		"none":         "",
		"info":         "Information",
		"information":  "Information",
		"warning":      "Exclamation",
		"error":        "Error",
		"question":     "Question",
		"shield":       "Shield",
		"hand":         "Hand",
		"asterisk":     "Asterisk",
		"application":  "Application",
		"winlogo":      "WinLogo",
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

// O header do script gerado deve preparar o PSADT para o config de staging
// (branding) via Initialize-ADTModule -ScriptDirectory.
func TestBuildPSADTVisualScript_StagingBrandingHook(t *testing.T) {
	req := PSADTVisualNotificationRequest{NotifType: "prompt_ok", Title: "T", Message: "M", AppName: "A"}
	script, _ := buildPSADTVisualScript(req)
	if !strings.Contains(script, "$env:PSADT_STAGING_CONFIG -eq '1'") {
		t.Fatalf("expected staging config guard in script header")
	}
	if !strings.Contains(script, "Initialize-ADTModule -ScriptDirectory (Split-Path -Parent $PSCommandPath)") {
		t.Fatalf("expected Initialize-ADTModule -ScriptDirectory hook")
	}
}

func TestNormalizeHexAccent(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"4A9EFF":     "0xff4a9eff",
		"#4A9EFF":    "0xff4a9eff",
		"0xFF4A9EFF": "0xff4a9eff",
		"FF4A9EFF":   "0xff4a9eff",
		"XYZ":        "",
		"12345":      "",
	}
	for in, want := range cases {
		if got := normalizeHexAccent(in); got != want {
			t.Errorf("normalizeHexAccent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeDialogStyle(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"fluent":  "Fluent",
		"CLASSIC": "Classic",
		"other":   "",
	}
	for in, want := range cases {
		if got := normalizeDialogStyle(in); got != want {
			t.Errorf("normalizeDialogStyle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWritePSADTVisualBranding(t *testing.T) {
	dir := t.TempDir()
	iconSrc := filepath.Join(dir, "src-icon.png")
	if err := os.WriteFile(iconSrc, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("falha ao criar icone de teste: %v", err)
	}
	scriptPath := filepath.Join(dir, "psadt-visual-test.ps1")
	if err := os.WriteFile(scriptPath, []byte("# teste"), 0o644); err != nil {
		t.Fatalf("falha ao criar script de teste: %v", err)
	}

	// Sem personalizacao: nada criado.
	files, err := writePSADTVisualBranding(PSADTVisualNotificationRequest{}, scriptPath)
	if err != nil || len(files) != 0 {
		t.Fatalf("sem branding deveria retornar nil/nil, veio %v, %v", files, err)
	}

	req := PSADTVisualNotificationRequest{
		BrandingIconPath:  iconSrc,
		FluentAccentColor: "#4A9EFF",
		DialogStyle:       "classic",
	}
	files, err = writePSADTVisualBranding(req, scriptPath)
	if err != nil {
		t.Fatalf("falha inesperada: %v", err)
	}
	var configPath string
	var hasIcon bool
	for _, f := range files {
		if filepath.Base(f) == "config.psd1" {
			configPath = f
		}
		if filepath.Base(f) == "discovery-icon.png" {
			hasIcon = true
		}
	}
	if configPath == "" || !hasIcon {
		t.Fatalf("arquivos de staging ausentes: %v", files)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("falha ao ler config gerado: %v", err)
	}
	cfg := string(data)
	for _, want := range []string{
		"Logo = 'discovery-icon.png'",
		"DialogStyle = 'Classic'",
		"FluentAccentColor = 0xff4a9eff",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.psd1 gerado nao contem %q:\n%s", want, cfg)
		}
	}
}

// Dialogs Fluent sem logo definido devem padronizar com o icon.ico do agent.
func TestWritePSADTVisualBranding_AgentIconDefault(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "psadt-visual-test.ps1")
	if err := os.WriteFile(scriptPath, []byte("# teste"), 0o644); err != nil {
		t.Fatalf("falha ao criar script de teste: %v", err)
	}

	req := PSADTVisualNotificationRequest{NotifType: "prompt_ok", Title: "T", Message: "M", AppName: "A"}
	files, err := writePSADTVisualBranding(req, scriptPath)
	if err != nil {
		t.Fatalf("falha inesperada: %v", err)
	}
	var configPath string
	for _, f := range files {
		if filepath.Base(f) == "config.psd1" {
			configPath = f
		}
	}
	if configPath == "" {
		t.Fatalf("config.psd1 de staging ausente: %v", files)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("falha ao ler config gerado: %v", err)
	}
	cfg := string(data)
	if !strings.Contains(cfg, "Logo = 'discovery-agent-icon.ico'") {
		t.Errorf("config deveria usar o icon.ico do agent como Logo:\n%s", cfg)
	}
	if !strings.Contains(cfg, "LogoDark = 'discovery-agent-icon.ico'") {
		t.Errorf("config deveria usar o icon.ico do agent como LogoDark:\n%s", cfg)
	}

	// Balloon e Dialog Box (Win32) nao usam dialogs Fluent: sem branding nada e criado.
	files, err = writePSADTVisualBranding(PSADTVisualNotificationRequest{NotifType: "balloon_info"}, scriptPath)
	if err != nil || len(files) != 0 {
		t.Errorf("balloon sem branding deveria retornar nil/nil, veio %v, %v", files, err)
	}
	files, err = writePSADTVisualBranding(PSADTVisualNotificationRequest{NotifType: "dialog_box"}, scriptPath)
	if err != nil || len(files) != 0 {
		t.Errorf("dialog_box sem branding deveria retornar nil/nil, veio %v, %v", files, err)
	}
}
