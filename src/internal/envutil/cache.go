// Package envutil fornece cache lazy de variáveis de ambiente frequentemente acessadas,
// evitando syscalls repetidas de os.Getenv. Seguro para uso concorrente.
package envutil

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	initOnce      sync.Once
	programData   string
	localAppData  string
	windowsDir    string
	homeDir       string
	exeDir        string
	osName        string
	logFile       string
	frameMode     string
	localeEnv     string
	insecureTLS   string
	insecureTrans string
)

// Init pré-carrega variáveis de ambiente comuns. Chamar no startup.
// Seguro para múltiplas chamadas (sync.Once).
func Init() {
	initOnce.Do(func() {
		programData = strings.TrimSpace(os.Getenv("ProgramData"))
		localAppData = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		windowsDir = strings.TrimSpace(os.Getenv("WINDIR"))
		osName = strings.TrimSpace(os.Getenv("OS"))
		logFile = strings.TrimSpace(os.Getenv("DISCOVERY_LOG_FILE"))
		frameMode = strings.TrimSpace(os.Getenv("DISCOVERY_WINDOW_FRAME"))
		localeEnv = coalesceEnv("DISCOVERY_LOCALE", "LC_ALL", "LANG", "LANGUAGE")
		insecureTLS = strings.TrimSpace(os.Getenv("DISCOVERY_ALLOW_INSECURE_TLS"))
		insecureTrans = strings.TrimSpace(os.Getenv("DISCOVERY_ALLOW_INSECURE_TRANSPORT"))

		if home, err := os.UserHomeDir(); err == nil {
			homeDir = strings.TrimSpace(home)
		}
		if exe, err := os.Executable(); err == nil {
			exeDir = strings.TrimSpace(filepath.Dir(exe))
		}
		if windowsDir == "" {
			windowsDir = filepath.Join("C:\\", "Windows")
		}
	})
}

// ProgramData retorna %ProgramData% (Windows) com cache.
func ProgramData() string { Init(); return programData }

// LocalAppData retorna %LOCALAPPDATA% (Windows) com cache.
func LocalAppData() string { Init(); return localAppData }

// WindowsDir retorna %WINDIR% (Windows) com cache.
// Fallback para C:\Windows.
func WindowsDir() string { Init(); return windowsDir }

// HomeDir retorna o home directory do usuário com cache.
func HomeDir() string { Init(); return homeDir }

// ExeDir retorna o diretório do executável em execução com cache.
func ExeDir() string { Init(); return exeDir }

// OSName retorna o valor de %OS% (Windows).
func OSName() string { Init(); return osName }

// LogFile retorna DISCOVERY_LOG_FILE.
func LogFile() string { Init(); return logFile }

// WindowFrame retorna DISCOVERY_WINDOW_FRAME.
func WindowFrame() string { Init(); return frameMode }

// LocaleEnv retorna a primeira variável de locale disponível
// (DISCOVERY_LOCALE, LC_ALL, LANG, LANGUAGE).
func LocaleEnv() string { Init(); return localeEnv }

// AllowInsecureTLS retorna DISCOVERY_ALLOW_INSECURE_TLS.
func AllowInsecureTLS() string { Init(); return insecureTLS }

// AllowInsecureTransport retorna DISCOVERY_ALLOW_INSECURE_TRANSPORT.
func AllowInsecureTransport() string { Init(); return insecureTrans }

// coalesceEnv retorna o primeiro valor não vazio entre as variáveis.
func coalesceEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
