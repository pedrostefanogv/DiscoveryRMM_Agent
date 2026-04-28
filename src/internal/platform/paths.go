// Package platform centraliza resolução de caminhos de diretórios e arquivos
// usados por toda a aplicação, eliminando duplicação de os.Getenv e filepath.Join.
package platform

import (
	"discovery/internal/envutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ─── Diretórios Base ───────────────────────────────────────────────

// DataDir retorna o diretório de dados principal.
// Prioridade (Windows): ProgramData\Discovery → LOCALAPPDATA\Discovery → ~/.discovery
func DataDir() string {
	if runtime.GOOS == "windows" {
		if pd := envutil.ProgramData(); pd != "" {
			return filepath.Join(pd, "Discovery")
		}
		if lad := envutil.LocalAppData(); lad != "" {
			return filepath.Join(lad, "Discovery")
		}
	}
	if home := envutil.HomeDir(); home != "" {
		return filepath.Join(home, ".discovery")
	}
	return "."
}

// ConfigDir retorna DataDir() — alias para clareza semântica.
func ConfigDir() string { return DataDir() }

// TempDir retorna o diretório temporário do Discovery.
// Windows: %WINDIR%\Temp\Discovery
// Outros: <DataDir>/Temp
func TempDir() string {
	if runtime.GOOS == "windows" {
		windowsDir := envutil.WindowsDir()
		return filepath.Join(windowsDir, "Temp", "Discovery")
	}
	return filepath.Join(DataDir(), "Temp")
}

// P2PTempDir retorna o diretório para arquivos temporários P2P.
func P2PTempDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(envutil.WindowsDir(), "Temp", "Discovery", "P2P_Temp")
	}
	return filepath.Join(DataDir(), "TempP2P")
}

// ─── Caminhos de Arquivos de Configuração ──────────────────────────

// ConfigPathCandidates retorna caminhos candidatos para config.json.
// Ordem: ProgramData → LOCALAPPDATA → dir do exe → ~/.discovery → .
func ConfigPathCandidates() []string {
	return collectPathCandidates("config.json")
}

// InstallerOverridePathCandidates retorna caminhos candidatos para installer.json.
func InstallerOverridePathCandidates() []string {
	return collectPathCandidates("installer.json")
}

// SharedConfigPath retorna o caminho primário para a configuração compartilhada.
// Windows: %ProgramData%\Discovery\config.json
func SharedConfigPath() string {
	if runtime.GOOS == "windows" {
		if pd := envutil.ProgramData(); pd != "" {
			return filepath.Join(pd, "Discovery", "config.json")
		}
	}
	return filepath.Join(DataDir(), "config.json")
}

// LogFilePath retorna o caminho para o arquivo de log, se configurado.
func LogFilePath() string {
	return envutil.LogFile()
}

// ─── Caminhos do Chat Config ────────────────────────────────────────

// ChatConfigPathCandidates retorna caminhos candidatos para o arquivo de config do chat.
func ChatConfigPathCandidates(filename string) []string {
	return collectPathCandidates(filename)
}

// ─── Caminhos do Mesh Central ───────────────────────────────────────

// MeshAgentPathCandidates retorna candidatos para MeshAgent.exe.
func MeshAgentPathCandidates() []string {
	paths := []string{
		`C:\Program Files\Mesh Agent\MeshAgent.exe`,
		`C:\Program Files (x86)\Mesh Agent\MeshAgent.exe`,
		`C:\ProgramData\Mesh Agent\MeshAgent.exe`,
	}
	if pd := envutil.ProgramData(); pd != "" {
		paths = append(paths,
			filepath.Join(pd, "Mesh Agent", "MeshAgent.exe"),
			filepath.Join(pd, "Mesh Agent", "MeshAgent.msh"),
		)
	}
	return paths
}

// MeshNodeIDPathCandidates retorna candidatos para MeshAgent.msh (contém node ID).
func MeshNodeIDPathCandidates() []string {
	paths := []string{
		`C:\Program Files\Mesh Agent\MeshAgent.msh`,
		`C:\Program Files (x86)\Mesh Agent\MeshAgent.msh`,
		`C:\ProgramData\Mesh Agent\MeshAgent.msh`,
	}
	if pd := envutil.ProgramData(); pd != "" {
		paths = append(paths, filepath.Join(pd, "Mesh Agent", "MeshAgent.msh"))
	}
	return paths
}

// ─── Caminhos do osquery ────────────────────────────────────────────

// OsquerySocketPath retorna o caminho do socket osquery.
func OsquerySocketPath() string {
	return filepath.Join(envutil.HomeDir(), ".osquery", "shell.em")
}

// ─── Caminhos de Fontes PDF ─────────────────────────────────────────

// PDFFontPath retorna DISCOVERY_PDF_FONT_PATH.
func PDFFontPath() string {
	return strings.TrimSpace(os.Getenv("DISCOVERY_PDF_FONT_PATH"))
}

// PDFFontConfigDir retorna DISCOVERY_PDF_FONT_CONFIG_DIR.
func PDFFontConfigDir() string {
	return strings.TrimSpace(os.Getenv("DISCOVERY_PDF_FONT_CONFIG_DIR"))
}

// ─── Locale ─────────────────────────────────────────────────────────

// Locale retorna o valor da variável de locale detectada.
func Locale() string {
	return envutil.LocaleEnv()
}

// WindowFrame retorna o modo de frame da janela.
func WindowFrame() string {
	return envutil.WindowFrame()
}

// ─── Segurança TLS ──────────────────────────────────────────────────

// AllowInsecureTLS retorna true se TLS inseguro estiver permitido via env.
func AllowInsecureTLS() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("DISCOVERY_ALLOW_INSECURE_TLS")))
	return v == "1" || v == "true" || v == "yes"
}

// AllowInsecureTransport retorna true se transporte inseguro estiver permitido via env.
func AllowInsecureTransport() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("DISCOVERY_ALLOW_INSECURE_TRANSPORT")))
	return v == "1" || v == "true" || v == "yes"
}

// ─── Helpers Internos ────────────────────────────────────────────────

// collectPathCandidates constrói a lista canônica de candidatos de path para um arquivo.
// Ordem: ProgramData\Discovery\<file> → LOCALAPPDATA\Discovery\<file> → exeDir\<file> → ~/.discovery/<file> → ./<file>
func collectPathCandidates(filename string) []string {
	paths := make([]string, 0, 5)

	if runtime.GOOS == "windows" {
		if pd := envutil.ProgramData(); pd != "" {
			paths = append(paths, filepath.Join(pd, "Discovery", filename))
		}
		if lad := envutil.LocalAppData(); lad != "" {
			paths = append(paths, filepath.Join(lad, "Discovery", filename))
		}
	}

	if exeDir := envutil.ExeDir(); exeDir != "" {
		paths = append(paths, filepath.Join(exeDir, filename))
	}

	if home := envutil.HomeDir(); home != "" {
		paths = append(paths, filepath.Join(home, ".discovery", filename))
	}

	paths = append(paths, filepath.Join(".", filename))
	return paths
}
