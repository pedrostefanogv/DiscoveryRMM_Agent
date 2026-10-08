package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ─── Abrir pasta/aplicativo pedidos pela IA ───
//
// A AUTORIZAÇÃO não é pedida aqui: a política por ação (mcp.ToolConsentFor) faz
// o gate no chat ANTES da tool rodar — mesmo caminho de instalar programa ou
// reiniciar serviço. Este arquivo cuida da RESOLUÇÃO e da VALIDAÇÃO do alvo:
//
//   - pasta: apelido conhecido (downloads, documentos, desktop, ...) ou caminho
//     absoluto LOCAL existente. UNC (\\servidor\share), caminho relativo e
//     arquivo (não-pasta) são recusados.
//   - aplicativo: nome resolvido em atalhos do Menu Iniciar e em App Paths do
//     Registro. NUNCA aceitamos linha de comando, argumentos ou caminho
//     arbitrário — só abrimos um app que JÁ existe na máquina.
//
// Ambas ficam registradas no log do agent ([open]) para auditoria.

// openAppMaxCandidates limita a lista devolvida quando o nome não bate exato.
const openAppMaxCandidates = 8

// installedAppsHardLimit limita o total devolvido por list_installed_apps.
const installedAppsHardLimit = 500

// openFolderAlias descreve como resolver um apelido de pasta.
//
// knownFolder é resolvido pela API do SO (SHGetKnownFolderPath no Windows), que
// respeita pastas REDIRECIONADAS — sem isso "downloads" apontava para
// %USERPROFILE%\Downloads e falhava em máquinas com Downloads em outro disco.
// envParts é o fallback portátil por variáveis de ambiente.
type openFolderAlias struct {
	knownFolder string
	envParts    []string
}

var openFolderAliases = map[string]openFolderAlias{
	"downloads":        {knownFolder: "downloads", envParts: []string{"USERPROFILE", "Downloads"}},
	"baixados":         {knownFolder: "downloads", envParts: []string{"USERPROFILE", "Downloads"}},
	"documentos":       {knownFolder: "documents", envParts: []string{"USERPROFILE", "Documents"}},
	"documents":        {knownFolder: "documents", envParts: []string{"USERPROFILE", "Documents"}},
	"desktop":          {knownFolder: "desktop", envParts: []string{"USERPROFILE", "Desktop"}},
	"area de trabalho": {knownFolder: "desktop", envParts: []string{"USERPROFILE", "Desktop"}},
	"imagens":          {knownFolder: "pictures", envParts: []string{"USERPROFILE", "Pictures"}},
	"pictures":         {knownFolder: "pictures", envParts: []string{"USERPROFILE", "Pictures"}},
	"videos":           {knownFolder: "videos", envParts: []string{"USERPROFILE", "Videos"}},
	"musicas":          {knownFolder: "music", envParts: []string{"USERPROFILE", "Music"}},
	"music":            {knownFolder: "music", envParts: []string{"USERPROFILE", "Music"}},
	"perfil":           {knownFolder: "profile", envParts: []string{"USERPROFILE"}},
	"profile":          {knownFolder: "profile", envParts: []string{"USERPROFILE"}},
	"temp":             {envParts: []string{"TEMP"}},
	"temporarios":      {envParts: []string{"TEMP"}},
	"appdata":          {envParts: []string{"APPDATA"}},
	"localappdata":     {envParts: []string{"LOCALAPPDATA"}},
	"programas":        {envParts: []string{"ProgramData", "Microsoft", "Windows", "Start Menu", "Programs"}},
}

// knownFolderPath é preenchido por plataforma (Windows: SHGetKnownFolderPath).
var knownFolderPath func(string) (string, bool)

// resolveOpenFolderPath resolve o apelido ou valida o caminho absoluto local.
func resolveOpenFolderPath(folder string, lookupEnv func(string) string) (string, error) {
	raw := strings.TrimSpace(folder)
	if raw == "" {
		return "", fmt.Errorf("informe a pasta: um apelido (ex.: downloads, documentos, desktop) ou o caminho completo")
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	alias, ok := openFolderAliases[strings.ToLower(raw)]
	if !ok {
		return validateOpenFolderPath(raw)
	}

	// 1) Pasta conhecida do SO (respeita redirecionamento de perfil).
	if alias.knownFolder != "" && knownFolderPath != nil {
		if p, ok := knownFolderPath(alias.knownFolder); ok && strings.TrimSpace(p) != "" {
			if resolved, err := validateOpenFolderPath(p); err == nil {
				return resolved, nil
			}
		}
	}

	// 2) Fallback por variáveis de ambiente.
	if len(alias.envParts) > 0 {
		base := strings.TrimSpace(lookupEnv(alias.envParts[0]))
		if base == "" {
			return "", fmt.Errorf("a pasta %q nao esta disponivel nesta conta (%s vazio)", raw, alias.envParts[0])
		}
		full := append([]string{base}, alias.envParts[1:]...)
		return validateOpenFolderPath(filepath.Join(full...))
	}

	return "", fmt.Errorf("a pasta %q nao pode ser resolvida nesta maquina", raw)
}

// validateOpenFolderPath recusa UNC/rede, caminho relativo, inexistente e
// arquivo; devolve o caminho limpo.
func validateOpenFolderPath(p string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(p))
	if strings.HasPrefix(clean, "\\\\") || strings.HasPrefix(clean, "//") {
		return "", fmt.Errorf("caminhos de rede (UNC) nao sao permitidos: %s", p)
	}
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("informe um caminho absoluto (ex.: C:\\Users\\voce\\Downloads) ou um apelido conhecido")
	}
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("a raiz do disco nao e uma pasta valida para abrir")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("pasta inacessivel: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("o caminho existe mas nao e uma pasta: %s", clean)
	}
	return clean, nil
}

// isSensitiveFolderTarget destaca apelidos que expõem dados técnicos/sistema no
// pedido de autorização (consentimento informado).
func isSensitiveFolderTarget(folder string) bool {
	switch strings.ToLower(strings.TrimSpace(folder)) {
	case "appdata", "localappdata", "programas", "program files":
		return true
	}
	return false
}

// startMenuProgramDirs devolve as pastas de atalhos do Menu Iniciar (todos os
// usuários e do usuário atual).
func startMenuProgramDirs(lookupEnv func(string) string) []string {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	var dirs []string
	for _, base := range []string{lookupEnv("ProgramData"), lookupEnv("APPDATA")} {
		if strings.TrimSpace(base) == "" {
			continue
		}
		dirs = append(dirs, filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	return dirs
}

// installedAppNames lista os nomes únicos de apps com atalho no Menu Iniciar,
// opcionalmente filtrados por parte do nome, limitados a limit.
func installedAppNames(query string, limit int, lookupEnv func(string) string) []string {
	if limit <= 0 || limit > installedAppsHardLimit {
		limit = installedAppsHardLimit
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	seen := map[string]bool{}
	var out []string
	for _, dir := range startMenuProgramDirs(lookupEnv) {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() || len(out) >= limit {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(p), ".lnk") {
				return nil
			}
			label := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			lower := strings.ToLower(label)
			if needle != "" && !strings.Contains(lower, needle) && !containsAnyWord(lower, needle) {
				return nil
			}
			if seen[lower] {
				return nil
			}
			seen[lower] = true
			out = append(out, label)
			return nil
		})
	}
	sort.Strings(out)
	return out
}

type launchAppCandidate struct {
	path  string
	label string
	score int
}

// resolveLaunchAppTarget resolve o NOME de um app instalado para um alvo
// executável (.lnk do Menu Iniciar ou exe do App Paths do Registro).
func resolveLaunchAppTarget(name string, lookupEnv func(string) string, lookupAppPath func(string) (string, bool)) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", fmt.Errorf("informe o nome do aplicativo (ex.: Google Chrome)")
	}
	if strings.ContainsAny(raw, "\\/:*?\"<>|") {
		return "", fmt.Errorf("informe apenas o NOME do aplicativo (sem caminho, sem argumentos): %q", raw)
	}

	// 1) App Paths do Registro (nome exato do executável).
	if lookupAppPath != nil {
		if p, ok := lookupAppPath(raw); ok && strings.TrimSpace(p) != "" {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, nil
			}
		}
	}

	// 2) Atalhos do Menu Iniciar (case-insensitive; exato > prefixo > contém).
	target := strings.ToLower(raw)
	var candidates []launchAppCandidate
	for _, dir := range startMenuProgramDirs(lookupEnv) {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(p), ".lnk") {
				return nil
			}
			label := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			lower := strings.ToLower(label)
			score := 0
			switch {
			case lower == target:
				score = 3
			case strings.HasPrefix(lower, target):
				score = 2
			case strings.Contains(lower, target):
				score = 1
			default:
				return nil
			}
			candidates = append(candidates, launchAppCandidate{path: p, label: label, score: score})
			return nil
		})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("nao encontrei o aplicativo %q entre os atalhos instalados; confirme o nome", raw)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].label < candidates[j].label
	})
	return candidates[0].path, nil
}

// appCandidatesForMessage lista nomes parecidos para a mensagem de erro.
func appCandidatesForMessage(name string, lookupEnv func(string) string) []string {
	return installedAppNames(name, openAppMaxCandidates, lookupEnv)
}

// containsAnyWord compara também pela palavra do nome ("chrome" acha "Google Chrome").
func containsAnyWord(label, target string) bool {
	for _, w := range strings.Fields(label) {
		if strings.Contains(w, target) {
			return true
		}
	}
	return false
}

// OpenFolder implementa AppBridge: abre a pasta no Explorer.
func (a *App) OpenFolder(folder, reason string) (json.RawMessage, error) {
	resolved, err := resolveOpenFolderPath(folder, os.Getenv)
	if err != nil {
		a.logOpenExternal("open_folder", folder, "erro: "+err.Error())
		return nil, err
	}
	if err := launchExternalTarget(resolved); err != nil {
		a.logOpenExternal("open_folder", resolved, "erro: "+err.Error())
		return nil, err
	}
	a.logOpenExternal("open_folder", resolved, reason)
	payload, _ := json.Marshal(map[string]any{
		"opened":  true,
		"kind":    "folder",
		"path":    resolved,
		"message": "Pasta aberta no Explorer. Nao abra de novo sem o usuario pedir.",
	})
	return payload, nil
}

// OpenApp implementa AppBridge: abre um aplicativo instalado.
func (a *App) OpenApp(name, reason string) (json.RawMessage, error) {
	target, err := resolveLaunchAppTarget(name, os.Getenv, lookupRegisteredAppPath)
	if err != nil {
		candidates := appCandidatesForMessage(name, os.Getenv)
		a.logOpenExternal("open_app", name, "erro: "+err.Error())
		payload, _ := json.Marshal(map[string]any{
			"opened":     false,
			"kind":       "app",
			"message":    err.Error(),
			"candidates": candidates,
		})
		return payload, nil
	}
	if err := launchExternalTarget(target); err != nil {
		a.logOpenExternal("open_app", target, "erro: "+err.Error())
		return nil, err
	}
	a.logOpenExternal("open_app", target, reason)
	payload, _ := json.Marshal(map[string]any{
		"opened":  true,
		"kind":    "app",
		"target":  target,
		"message": "Aplicativo aberto. Nao abra de novo sem o usuario pedir.",
	})
	return payload, nil
}

// ListInstalledApps implementa AppBridge: nomes de apps instalados (atalhos do
// Menu Iniciar) para o LLM descobrir o nome exato antes de chamar open_app.
// É leitura pura — não exige autorização.
func (a *App) ListInstalledApps(query string, limit int) (json.RawMessage, error) {
	if limit <= 0 {
		limit = 120
	}
	names := installedAppNames(query, limit, os.Getenv)
	payload, _ := json.Marshal(map[string]any{
		"count": len(names),
		"apps":  names,
		"hint":  "Use o nome exato em open_app (sem caminho e sem argumentos).",
	})
	return payload, nil
}

// logOpenExternal registra a abertura no log do agent (auditoria).
func (a *App) logOpenExternal(action, target, reason string) {
	if a == nil {
		return
	}
	line := "[open] " + action + " target=" + target
	if strings.TrimSpace(reason) != "" {
		line += " motivo=" + strings.TrimSpace(reason)
	}
	a.Logs.Append("[chat] " + line)
}
