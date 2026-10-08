package ai

import "testing"

// Regressão: a autorização de gravação dos exports é uma PERGUNTA no chat.
// Se a tool ficar sujeita ao timeout de 60s do loop, a pergunta morre antes do
// clique do usuário e a exportação nunca é confirmada (ou é negada em silêncio).
func TestInteractiveToolRequiresUser(t *testing.T) {
	wantInteractive := []string{
		"ask_user", "capture_screenshot", "read_file",
		"export_inventory_markdown", "export_inventory_pdf",
	}
	for _, name := range wantInteractive {
		if !interactiveToolRequiresUser(name) {
			t.Errorf("%q deveria aguardar o usuário (sem timeout)", name)
		}
	}

	wantTimed := []string{"get_inventory", "search_packages", "install_package", "get_logs"}
	for _, name := range wantTimed {
		if interactiveToolRequiresUser(name) {
			t.Errorf("%q NÃO deveria ser isenta de timeout", name)
		}
	}
}
