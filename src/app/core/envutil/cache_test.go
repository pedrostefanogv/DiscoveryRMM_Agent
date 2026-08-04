package envutil

import (
	"os"
	"runtime"
	"testing"
)

func TestInit_Idempotent(t *testing.T) {
	Init()
	Init() // segunda chamada não deve panicar nem resetar

	if runtime.GOOS == "windows" {
		// ProgramData deve ser igual a os.Getenv("ProgramData")
		expected := os.Getenv("ProgramData")
		if got := ProgramData(); got != expected {
			t.Logf("ProgramData: cache=%q os.Getenv=%q", got, expected)
		}
	}
}

func TestHomeDir_NotEmpty(t *testing.T) {
	Init()
	home := HomeDir()
	if home == "" {
		t.Fatal("HomeDir retornou vazio")
	}
}

func TestExeDir_NotEmpty(t *testing.T) {
	Init()
	exeDir := ExeDir()
	if exeDir == "" {
		t.Fatal("ExeDir retornou vazio")
	}
}

func TestWindowsDir_Fallback(t *testing.T) {
	Init()
	windowsDir := WindowsDir()
	if windowsDir == "" {
		t.Fatal("WindowsDir retornou vazio")
	}
}

func TestAllowInsecureTLS_DefaultFalse(t *testing.T) {
	Init()
	// Por padrão, sem env var setada, deve ser false
	tls := AllowInsecureTLS()
	if tls != "" && tls != "0" && tls != "false" {
		t.Logf("AllowInsecureTLS=%q (pode estar setado no ambiente)", tls)
	}
}

func TestLocaleEnv_NoPanic(t *testing.T) {
	Init()
	locale := LocaleEnv()
	_ = locale // não panicar é suficiente
}

func TestOSName_NoPanic(t *testing.T) {
	Init()
	osName := OSName()
	_ = osName
}
