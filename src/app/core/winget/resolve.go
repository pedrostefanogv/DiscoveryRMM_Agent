package winget

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Localização do winget (nativo primeiro; fallback só se necessário)
//
// O winget NÃO é um executável comum: vem junto do pacote MSIX
// "Microsoft.DesktopAppInstaller" e é exposto por um App Execution Alias.
//
// O alias é um reparse point de 0 byte em
//   %LOCALAPPDATA%\Microsoft\WindowsApps\winget.exe
// (por usuário) e esse diretório NÃO faz parte do PATH da máquina. O pacote
// real fica em C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_*.
//
// Consequência: um processo rodando como SERVIÇO (LocalSystem) não encontra
// "winget" no PATH e falha com
//   exec: "winget": executable file not found in %PATH%
// reportando o inventário SEM updates (o dashboard perde os botões de
// atualizar/desinstalar).
//
// Ordem de resolução — sempre nativa primeiro, sem subprocesso quando possível:
//   1. PATH do processo
//   2. pacote instalado para a MÁQUINA (Program Files\WindowsApps)
//   3. alias nos perfis de usuário (scan de C:\Users\*)
//   4. alias do SYSTEM
//   5. pacote registrado no Windows (PowerShell/Get-AppxPackage)
//   6. osquery (último recurso, só se disponível na máquina)
// ─────────────────────────────────────────────────────────────────────────────

var (
	wingetOnce   sync.Once
	wingetPath   string
	wingetOrigin string
)

// resolveTimeout limita as estratégias que invocam subprocesso, para não travar
// o scan de inventário numa máquina onde elas não respondem.
const resolveTimeout = 25 * time.Second

// OsqueryLookup é injetado pelo caller para o fallback de último recurso:
// recebe uma query SQL e devolve as linhas resultantes. Fica como dependência
// injetada para o pacote winget não depender do pacote de inventário (evita
// ciclo de import).
var OsqueryLookup func(ctx context.Context, sql string) []string

// ResolveExecutable devolve o caminho do winget utilizável neste processo e a
// origem da descoberta (para log/diagnóstico). Vazio quando não encontra.
func ResolveExecutable() (string, string) {
	wingetOnce.Do(func() {
		wingetPath, wingetOrigin = resolveExecutableUncached()
	})
	return wingetPath, wingetOrigin
}

// InvalidateResolvedExecutable descarta o cache (após instalar/atualizar o winget).
func InvalidateResolvedExecutable() {
	wingetOnce = sync.Once{}
	wingetPath = ""
	wingetOrigin = ""
}

func resolveExecutableUncached() (string, string) {
	// 1) PATH do processo — agente rodando na sessão do usuário.
	if p, err := exec.LookPath("winget"); err == nil && usable(p) {
		return p, "PATH"
	}

	// 2) Pacote instalado para a MÁQUINA (acessível a qualquer conta).
	if p := fromMachinePackage(); p != "" {
		return p, "pacote da maquina (WindowsApps)"
	}

	// 3) Alias nos perfis de usuário — cenário mais comum no parque.
	if p := fromUserProfiles(); p != "" {
		return p, "alias de perfil de usuario"
	}

	// 4) Alias do próprio SYSTEM (winget provisionado para todos).
	if p := fromSystemProfile(); p != "" {
		return p, "alias do SYSTEM"
	}

	// 5) App Paths no registro: o instalador do winget registra o caminho REAL
	//    do executável (C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_<ver>_x64__8wekyb3d8bbwe\winget.exe).
	//    É nativo, sem subprocesso e sobrevive à ausência do alias no perfil.
	if p := fromAppPathsRegistry(); p != "" {
		return p, "registro App Paths"
	}

	// 6) Pacote registrado no Windows (cobre layout fora do padrão).
	if p := fromAppxPackage(); p != "" {
		return p, "Get-AppxPackage"
	}

	// 7) Último recurso: osquery, quando disponível na máquina.
	if p := fromOsquery(OsqueryLookup); p != "" {
		return p, "osquery"
	}

	return "", "nao encontrado"
}

// usable confirma que o caminho aponta para um arquivo existente. O alias de 0
// byte também passa — ele funciona quando o pacote está instalado para o
// usuário dono do perfil.
func usable(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// fromMachinePackage procura o pacote em WindowsApps (visível a qualquer
// conta). Vários builds podem coexistir — escolhe o mais recente.
func fromMachinePackage() string {
	roots := []string{
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles"),
		"C:\\Program Files",
		"C:\\Program Files (x86)",
	}

	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		pattern := filepath.Join(root, "WindowsApps",
			"Microsoft.DesktopAppInstaller_*_*__8wekyb3d8bbwe", "winget.exe")
		if p := newestMatch(pattern); p != "" {
			return p
		}
	}
	return ""
}

// fromUserProfiles varre os perfis de usuário procurando o alias. Resolve o
// cenário mais comum: winget instalado apenas no perfil de quem configurou a
// máquina, enquanto o serviço roda como SYSTEM.
func fromUserProfiles() string {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		if p := aliasUnder(local); p != "" {
			return p
		}
	}
	for _, base := range profileLocalAppDataDirs() {
		if p := aliasUnder(base); p != "" {
			return p
		}
	}
	return ""
}

// profileLocalAppDataDirs lista os %LOCALAPPDATA% dos perfis do disco, em ordem
// determinística.
func profileLocalAppDataDirs() []string {
	drive := strings.TrimSpace(os.Getenv("SystemDrive"))
	if drive == "" {
		drive = "C:"
	}
	usersDir := filepath.Join(drive+string(os.PathSeparator), "Users")

	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return nil
	}

	skip := map[string]struct{}{
		"public": {}, "default": {}, "default user": {}, "all users": {},
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, isSkip := skip[strings.ToLower(e.Name())]; isSkip {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(usersDir, name, "AppData", "Local"))
	}
	return paths
}

// fromSystemProfile cobre o winget provisionado para a conta de sistema.
func fromSystemProfile() string {
	windir := strings.TrimSpace(os.Getenv("windir"))
	if windir == "" {
		windir = "C:\\Windows"
	}
	return aliasUnder(filepath.Join(windir, "System32", "config", "systemprofile", "AppData", "Local"))
}

// aliasUnder monta e valida o caminho do alias sob um %LOCALAPPDATA%.
func aliasUnder(localAppData string) string {
	if strings.TrimSpace(localAppData) == "" {
		return ""
	}
	candidate := filepath.Join(localAppData, "Microsoft", "WindowsApps", "winget.exe")
	if usable(candidate) {
		return candidate
	}
	return ""
}

// fromAppxPackage consulta o Windows pelo pacote instalado (cobre layouts fora
// dos caminhos padrão). Best-effort com timeout.
func fromAppxPackage() string {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	script := strings.Join([]string{
		"try {",
		"$p = Get-AppxPackage -Name Microsoft.DesktopAppInstaller | Select-Object -First 1;",
		"if ($p) { $c = Join-Path $p.InstallLocation \"winget.exe\";",
		"if (Test-Path $c) { Write-Output $c; exit 0 } };",
		"$a = Join-Path $env:LOCALAPPDATA \"Microsoft\\WindowsApps\\winget.exe\";",
		"if (Test-Path $a) { Write-Output $a }",
		"} catch { }",
	}, " ")

	cmd := processutil.HideCommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	return firstWingetPath(string(out))
}

// fromOsquery é o ÚLTIMO recurso: usa o osquery (quando instalado) para listar
// arquivos do pacote DesktopAppInstaller no disco. Só roda depois de esgotadas
// todas as estratégias nativas.
func fromOsquery(lookup func(context.Context, string) []string) string {
	if lookup == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	// Consulta o App Paths via osquery: a tabela "file" NÃO enumera
	// C:\Program Files\WindowsApps (ACL restrita), mas o registro expõe o
	// caminho real gravado pelo instalador.
	const sql = "SELECT data FROM registry WHERE key LIKE '%App Paths\\winget.exe' AND name = ''"
	for _, line := range lookup(ctx, sql) {
		if p := firstWingetPath(line); p != "" {
			return p
		}
	}
	return ""
}

// firstWingetPath extrai o primeiro caminho terminado em winget.exe de um texto
// (uma linha por resultado, com campos separados por "|", tab ou vírgula).
func firstWingetPath(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == '|' || r == '\t' || r == ','
		})
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if !strings.HasSuffix(strings.ToLower(field), "winget.exe") {
				continue
			}
			if usable(field) {
				return field
			}
		}
	}
	return ""
}

// newestMatch devolve o maior caminho entre os matches do glob — com o nome da
// versão no diretório, o maior é o build mais recente.
func newestMatch(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	best := ""
	for _, m := range matches {
		if !usable(m) {
			continue
		}
		if m > best {
			best = m
		}
	}
	return best
}

// command cria o exec.Cmd usando o caminho resolvido do winget.
func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	if exe, _ := ResolveExecutable(); exe != "" {
		return exec.CommandContext(ctx, exe, args...)
	}
	// Sem caminho resolvido: mantém o comportamento anterior (o erro resultante
	// é tratado pelos callers como falha transitória e preserva a última lista).
	return exec.CommandContext(ctx, "winget", args...)
}

// lookPathWinget expõe exec.LookPath para os testes.
func lookPathWinget() (string, error) {
	return exec.LookPath("winget")
}
