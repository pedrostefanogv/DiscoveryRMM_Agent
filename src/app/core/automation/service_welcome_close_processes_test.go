package automation

import (
	"reflect"
	"testing"
)

// Tarefa com CloseProcesses e adiamento habilitado: as opções do Welcome
// nativo precisam carregar a lista normalizada (trim/dup/.exe) e o adiamento
// padrão, para o prompt nativo encerrar os processos ao prosseguir.
func TestResolvePSADTWelcomeOptionsCloseProcessesAndDefer(t *testing.T) {
	allowDefer := true
	task := AutomationTask{
		TaskID:         "t1",
		AllowDefer:     &allowDefer,
		CloseProcesses: []string{" FIREFOX.EXE ", "firefox", "WINWORD", "", "firefox.exe"},
	}

	options := resolvePSADTWelcomeOptions(task)

	if !options.AllowDefer {
		t.Fatalf("AllowDefer = false, want true")
	}
	// A normalização deduplica por minúsculas exatas (inclui .exe); a colapsagem
	// de "firefox.exe" e "firefox" acontece no fechamento, via normalizeProcessName.
	want := []string{"FIREFOX.EXE", "firefox", "WINWORD"}
	if !reflect.DeepEqual(options.CloseProcesses, want) {
		t.Fatalf("CloseProcesses = %v, want %v", options.CloseProcesses, want)
	}
	if options.CloseProcessesCountdownSeconds != resolveUserPromptTimeoutSeconds(task) {
		t.Fatalf("CloseProcessesCountdownSeconds = %d, want %d",
			options.CloseProcessesCountdownSeconds, resolveUserPromptTimeoutSeconds(task))
	}
}

// allowDefer = false: o prompt nativo força a continuação (Esc/X = instalar).
func TestResolvePSADTWelcomeOptionsNoDefer(t *testing.T) {
	allowDefer := false
	task := AutomationTask{TaskID: "t2", AllowDefer: &allowDefer}

	options := resolvePSADTWelcomeOptions(task)

	if options.AllowDefer {
		t.Fatalf("AllowDefer = true, want false")
	}
	if options.CloseProcesses != nil {
		t.Fatalf("CloseProcesses = %v, want nil", options.CloseProcesses)
	}
}
