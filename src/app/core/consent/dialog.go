// Package consent monta os diálogos de autorização exibidos no chat quando a
// IA pede uma ação com efeito no computador do usuário (gravar arquivo,
// instalar/desinstalar programa, parar serviço, ...).
//
// O texto é localizado (pt/en/es, mesmo mapa de idioma da captura de tela) e a
// aprovação é SEMPRE por ação: não existe "permitir sempre". O token Action é
// estável e independente do idioma (a política das tools usa o token; o texto
// exibido vem daqui).
package consent

import (
	"fmt"
	"strings"
)

// Kind separa a natureza da ação (usada apenas no texto de abertura).
type Kind string

const (
	// KindWrite é gravação de arquivo em disco.
	KindWrite Kind = "write"
	// KindDestructive é qualquer ação que altera o estado do computador.
	KindDestructive Kind = "destructive"
)

// Request descreve o pedido de autorização. Action é um token ESTÁVEL (não
// localizado) chave da tabela de textos; Target é o alvo legível (programa,
// serviço, arquivo, PID) e Extra são linhas adicionais de contexto.
type Request struct {
	Kind   Kind
	Action string
	Target string
	Extra  []string
}

// Dialog é o resultado localizado: pergunta + rótulos das opções.
type Dialog struct {
	Question string
	Approve  string
	Deny     string
}

// Language mapeia locale ("pt-BR", "en-US", "es-ES") para o idioma do diálogo.
// Locale desconhecido → inglês; vazio (sem informação) mantém pt-BR, o
// comportamento histórico do app.
func Language(locale string) string {
	v := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case v == "":
		return "pt"
	case strings.HasPrefix(v, "pt"):
		return "pt"
	case strings.HasPrefix(v, "es"):
		return "es"
	default:
		return "en"
	}
}

// actionPhrases traduz cada token de ação para o verbo exibido ao usuário.
var actionPhrases = map[string]map[string]string{
	"write_file":              {"pt": "GRAVAR um arquivo no disco", "en": "WRITE a file to disk", "es": "ESCRIBIR un archivo en el disco"},
	"install_package":         {"pt": "INSTALAR um programa", "en": "INSTALL a program", "es": "INSTALAR un programa"},
	"uninstall_package":       {"pt": "DESINSTALAR um programa", "en": "UNINSTALL a program", "es": "DESINSTALAR un programa"},
	"upgrade_package":         {"pt": "ATUALIZAR um programa", "en": "UPDATE a program", "es": "ACTUALIZAR un programa"},
	"upgrade_all_packages":    {"pt": "ATUALIZAR todos os programas com atualização disponível", "en": "UPDATE all programs with available updates", "es": "ACTUALIZAR todos los programas con actualización disponible"},
	"service_start":           {"pt": "INICIAR um serviço do Windows", "en": "START a Windows service", "es": "INICIAR un servicio de Windows"},
	"service_stop":            {"pt": "PARAR um serviço do Windows", "en": "STOP a Windows service", "es": "DETENER un servicio de Windows"},
	"service_restart":         {"pt": "REINICIAR um serviço do Windows", "en": "RESTART a Windows service", "es": "REINICIAR un servicio de Windows"},
	"run_scheduled_task":      {"pt": "EXECUTAR uma tarefa agendada", "en": "RUN a scheduled task", "es": "EJECUTAR una tarea programada"},
	"kill_process":            {"pt": "ENCERRAR um processo", "en": "KILL a process", "es": "TERMINAR un proceso"},
	"printer_install":         {"pt": "INSTALAR uma impressora", "en": "INSTALL a printer", "es": "INSTALAR una impresora"},
	"printer_install_shared":  {"pt": "CONECTAR uma impressora compartilhada", "en": "CONNECT a shared printer", "es": "CONECTAR una impresora compartida"},
	"printer_remove":          {"pt": "REMOVER uma impressora", "en": "REMOVE a printer", "es": "QUITAR una impresora"},
	"printer_cancel_job":      {"pt": "CANCELAR um trabalho de impressão", "en": "CANCEL a print job", "es": "CANCELAR un trabajo de impresión"},
	"printer_restart_spooler": {"pt": "REINICIAR o spooler de impressão", "en": "RESTART the print spooler", "es": "REINICIAR el spooler de impresión"},
	"printer_clear_queue":     {"pt": "LIMPAR toda a fila de impressão", "en": "CLEAR the entire print queue", "es": "LIMPIAR toda la cola de impresión"},
	"power_restart":           {"pt": "REINICIAR o computador", "en": "RESTART the computer", "es": "REINICIAR el equipo"},
	"power_shutdown":          {"pt": "DESLIGAR o computador", "en": "SHUT DOWN the computer", "es": "APAGAR el equipo"},
	"power_lock":              {"pt": "BLOQUEAR a sessão", "en": "LOCK the session", "es": "BLOQUEAR la sesión"},
	"open_folder":             {"pt": "ABRIR uma pasta no Explorer", "en": "OPEN a folder in Explorer", "es": "ABRIR una carpeta en el Explorador"},
	"open_app":                {"pt": "ABRIR um aplicativo instalado", "en": "OPEN an installed application", "es": "ABRIR una aplicación instalada"},
}

var fallbackPhrase = map[string]string{
	"pt": "EXECUTAR uma ação no computador",
	"en": "EXECUTE an action on the computer",
	"es": "EJECUTAR una acción en el equipo",
}

var intros = map[Kind]map[string]string{
	KindWrite: {
		"pt": "A IA pediu para %s.",
		"en": "The AI assistant requested to %s.",
		"es": "La IA solicitó %s.",
	},
	KindDestructive: {
		"pt": "A IA pediu para %s. Esta ação altera o computador.",
		"en": "The AI assistant requested to %s. This changes the computer.",
		"es": "La IA solicitó %s. Esta acción modifica el equipo.",
	},
}

var approveLabels = map[string]string{"pt": "Autorizar", "en": "Allow", "es": "Permitir"}
var denyLabels = map[string]string{"pt": "Negar", "en": "Deny", "es": "Negar"}
var targetLabels = map[string]string{"pt": "Alvo: %s", "en": "Target: %s", "es": "Destino: %s"}

var questionLabels = map[string]string{
	"pt": "Autorizar esta ação? A autorização vale somente desta vez.",
	"en": "Allow this action? The authorization applies only this time.",
	"es": "¿Permitir esta acción? La autorización vale solo por esta vez.",
}

// ApproveLabel devolve o rótulo EXATO do botão de aprovação no idioma.
func ApproveLabel(locale string) string { return approveLabels[Language(locale)] }

// DenyLabel devolve o rótulo EXATO do botão de negação no idioma.
func DenyLabel(locale string) string { return denyLabels[Language(locale)] }

// Build monta o diálogo localizado do pedido de autorização.
func Build(req Request, locale string) Dialog {
	lang := Language(locale)

	phrase := fallbackPhrase[lang]
	if p := actionPhrases[req.Action][lang]; p != "" {
		phrase = p
	}

	intro := intros[req.Kind][lang]
	if intro == "" {
		intro = intros[KindDestructive][lang]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(intro, phrase))
	if t := strings.TrimSpace(req.Target); t != "" {
		b.WriteString("\n- " + fmt.Sprintf(targetLabels[lang], t))
	}
	for _, extra := range req.Extra {
		if e := strings.TrimSpace(extra); e != "" {
			b.WriteString("\n- " + e)
		}
	}
	b.WriteString("\n\n" + questionLabels[lang])

	return Dialog{Question: b.String(), Approve: ApproveLabel(locale), Deny: DenyLabel(locale)}
}
