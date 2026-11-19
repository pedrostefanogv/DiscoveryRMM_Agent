package psadt

import (
	"runtime"
	"strings"
	"testing"
)

func TestDecodePowerShellOutput(t *testing.T) {
	if got := decodePowerShellOutput(nil); got != "" {
		t.Fatalf("empty nil = %q, want empty", got)
	}
	if got := decodePowerShellOutput([]byte{}); got != "" {
		t.Fatalf("empty slice = %q, want empty", got)
	}

	// Valid UTF-8 with leading/trailing whitespace.
	if got := decodePowerShellOutput([]byte("  Instalação concluída\r\n")); got != "Instalação concluída" {
		t.Fatalf("utf8 decode = %q, want %q", got, "Instalação concluída")
	}

	// CP850 bytes (non-UTF8): 0x82 = 'é', 0x87 = 'ç', 0xA0 = 'á'.
	cp850Bytes := []byte{' ', 'A', 0x82, 0x87, 0xA0, '\r', '\n'}
	if got := decodePowerShellOutput(cp850Bytes); got != "Aéçá" {
		t.Fatalf("cp850 decode = %q, want %q", got, "Aéçá")
	}
}

func TestEmitDebugNotificationDefaultsAndOverrides(t *testing.T) {
	var captured NotificationRequest
	var logs []string

	svc := New(Deps{
		Logf: func(line string) { logs = append(logs, line) },
		DispatchNotification: func(req NotificationRequest) NotificationResponse {
			captured = req
			return NotificationResponse{Accepted: true, Message: "ok"}
		},
	})

	// Blank fields should receive canonical defaults.
	if err := svc.EmitDebugNotification("  ", "", "", "", "", "amber", true); err != nil {
		t.Fatalf("EmitDebugNotification defaults failed: %v", err)
	}
	if captured.Title != "Teste PSADT" || captured.Message != "Notificação de teste" ||
		captured.Mode != "notify_only" || captured.Severity != "medium" || captured.Layout != "toast" {
		t.Fatalf("unexpected defaults: %+v", captured)
	}
	if captured.EventType != "psadt_debug_runtime" || captured.TimeoutSeconds != 45 {
		t.Fatalf("unexpected fixed fields: %+v", captured)
	}
	if !strings.HasPrefix(captured.NotificationID, "psadt-debug-") {
		t.Fatalf("NotificationID = %q, expected psadt-debug-*", captured.NotificationID)
	}
	if captured.Metadata["source"] != "psadt-debug" || captured.Metadata["accent"] != "amber" || captured.Metadata["requireAck"] != true {
		t.Fatalf("unexpected metadata: %+v", captured.Metadata)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], captured.NotificationID) {
		t.Fatalf("expected log line containing %q, got %v", captured.NotificationID, logs)
	}

	// Explicit fields override defaults.
	if err := svc.EmitDebugNotification("Titulo", "Corpo", "modal", "high", "card", "blue", false); err != nil {
		t.Fatalf("EmitDebugNotification explicit failed: %v", err)
	}
	if captured.Title != "Titulo" || captured.Message != "Corpo" ||
		captured.Mode != "modal" || captured.Severity != "high" || captured.Layout != "card" {
		t.Fatalf("explicit values not preserved: %+v", captured)
	}
}

func TestEmitDebugNotificationRejected(t *testing.T) {
	logs := 0
	svc := New(Deps{
		Logf: func(string) { logs++ },
		DispatchNotification: func(req NotificationRequest) NotificationResponse {
			return NotificationResponse{Accepted: false, Message: "  bloqueado  "}
		},
	})

	err := svc.EmitDebugNotification("T", "M", "notify_only", "low", "toast", "", false)
	if err == nil || !strings.Contains(err.Error(), "bloqueado") {
		t.Fatalf("expected rejection error containing 'bloqueado', got %v", err)
	}
	if logs != 0 {
		t.Fatalf("expected no log on rejected notification, got %d", logs)
	}
}

func TestGetScriptTemplate(t *testing.T) {
	svc := New(Deps{})
	tpl := svc.GetScriptTemplate()
	if !strings.Contains(tpl, "Write-Host") || !strings.Contains(tpl, "exit 0") {
		t.Fatalf("unexpected script template: %q", tpl)
	}
}

func TestExecuteCustomScriptValidation(t *testing.T) {
	var logs []string
	svc := New(Deps{Logf: func(line string) { logs = append(logs, line) }})

	res := svc.ExecuteCustomScript("   \t\n  ")
	if res.Success || res.ExitCode != 1 || res.ExecutedAtUTC == "" {
		t.Fatalf("unexpected empty script result: %+v", res)
	}
	if runtime.GOOS == "windows" && res.Error != "Script vazio" {
		t.Fatalf("Error = %q, want 'Script vazio'", res.Error)
	}
	if runtime.GOOS != "windows" && res.Error != "suportado apenas em Windows" {
		t.Fatalf("Error = %q, want non-Windows message", res.Error)
	}

	if runtime.GOOS == "windows" {
		okRes := svc.ExecuteCustomScript("Write-Output 'psadt-unit-ok'; exit 0")
		if !okRes.Success || okRes.ExitCode != 0 || okRes.Output != "psadt-unit-ok" {
			t.Fatalf("expected successful script execution, got %+v", okRes)
		}

		failRes := svc.ExecuteCustomScript("Write-Output 'psadt-unit-fail'; exit 7")
		if failRes.Success || failRes.ExitCode != 7 || failRes.Output != "psadt-unit-fail" {
			t.Fatalf("expected exit 7 failure result, got %+v", failRes)
		}
	}
}
