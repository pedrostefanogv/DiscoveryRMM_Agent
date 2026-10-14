package app

import "strings"

// ── Análise da linha de comando do startup ──────────────────────────────────
//
// O binário da UI aceita as mesmas flags com três prefixos (--, / e -). A origem
// da execução (--startup-source) é o que distingue uma abertura AUTOMÁTICA
// (task de logon, atalho de startup, restart pós-update, watchdog) de um clique
// do usuário no ícone.

// automaticStartupSources lista as origens que NUNCA devem abrir a janela
// cheia: são relançamentos sem o usuário pedindo, então a UI sobe no tray.
// Espelha a lista usada por enforceStartupMinimized em main.go (que é quem
// força o start minimizado quando a flag --startup-minimized se perde).
var automaticStartupSources = []string{
	"autostart",
	"task-scheduler",
	"startup-link",
	"update-restart",
	StartupSourceWatchdog,
}

// IsAutomaticStartupArgs informa se a linha de comando pede abertura automática
// (minimizada no tray), aceitando os prefixos --, / e -.
//
// Usado no single-instance: quando o relançamento vem da task de logon ou do
// watchdog e já existe uma UI viva, a segunda instância precisa SAIR em
// silêncio — antes ela chamava ShowMainWindow() e a janela do usuário saltava na
// frente do que ele estava fazendo.
func IsAutomaticStartupArgs(args []string) bool {
	for _, raw := range args {
		arg := strings.ToLower(strings.TrimSpace(raw))
		if arg == "" {
			continue
		}
		// Aceita --flag, /flag e -flag.
		flag := strings.TrimLeft(arg, "-/")
		if flag == "" {
			continue
		}
		if flag == "startup-minimized" {
			return true
		}
		if source, ok := strings.CutPrefix(flag, "startup-source="); ok {
			source = strings.TrimSpace(source)
			for _, automatic := range automaticStartupSources {
				if source == automatic {
					return true
				}
			}
		}
	}
	return false
}
