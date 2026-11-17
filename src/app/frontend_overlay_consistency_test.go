package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGlobals(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "frontend", "js", "app-core-globals.js"))
	if err != nil {
		t.Fatalf("ler app-core-globals.js: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// INCONSISTÊNCIA (revisão): o overlay calcula `mode` e as telas de conteúdo
// usam o estado publicado por applyServerAuthorizationState. Quando o
// GetOnboardingStatus FALHA, o overlay mostra a espera pelo fallback local mas
// o estado compartilhado não é atualizado — as telas continuariam com o modo
// antigo (ou 'normal'), reabrindo o caminho do erro cru que este trabalho veio
// eliminar. O overlay precisa publicar o modo QUE ELE VAI EXIBIR.
func TestOverlayPublishesTheModeItActuallyShows(t *testing.T) {
	js := readGlobals(t)

	body, ok := jsFunctionBody(js, "function syncProvisioningOverlayFromConfig(debugCfg, onboardingStatus)")
	if !ok {
		t.Fatal("syncProvisioningOverlayFromConfig não encontrada")
	}

	// Deve publicar SEMPRE (não só quando onboardingStatus existe).
	if strings.Contains(body, "typeof applyServerAuthorizationState === 'function' && onboardingStatus)") {
		t.Fatal("o overlay precisa publicar o modo mesmo sem onboardingStatus (fallback local), " +
			"senão as telas de conteúdo divergem do que o usuário vê")
	}
	if !strings.Contains(body, "publishServerAuthorizationMode(") {
		t.Fatal("o overlay precisa publicar o estado para as telas de conteúdo")
	}
	// E precisa publicar o MODO RESOLVIDO, não o status bruto do GO.
	if !strings.Contains(body, "publishServerAuthorizationMode(mode, onboardingStatus)") {
		t.Fatal("o overlay precisa publicar o modo que ele realmente exibe")
	}
}
