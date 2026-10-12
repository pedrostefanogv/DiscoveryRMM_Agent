package support

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guarda de regressao da conversa do chamado (aba Suporte).
//
// BUG CORRIGIDO: o anexo de imagem de um comentario aparecia apenas como um
// chip com o NOME do arquivo — nao havia miniatura. O composer tambem mostrava
// so o nome do arquivo pendente, sem previa nem botao de remover ANTES de
// enviar, ao contrario do chat com a IA (classe chat-attachment).
func TestSupportView_ComentariosComMiniaturaEAnexoPendenteComPreview(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("nao foi possivel ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	// 1) Anexo de imagem da conversa vira miniatura (mesmo gesto do chat).
	for _, needle := range []string{
		"ticket-msg-attachment--image",
		"ticket-msg-attachment-thumb",
		"hydrateCommentAttachmentThumbnails",
		"DownloadTicketCommentAttachment",
		"screenshotOpenLightbox",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support.js deve conter %q para exibir a miniatura dos anexos de comentario", needle)
		}
	}

	// 2) Anexo pendente no composer: tile do chat + botao de remover.
	for _, needle := range []string{
		"function buildPendingAttachmentTile(",
		"chat-attachment-remove",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support.js deve conter %q (previa/remocao do anexo antes de enviar)", needle)
		}
	}

	// 3) O HTML usa a barra do chat para a fila de anexos.
	htmlPath := filepath.Join("..", "..", "frontend", "partials", "views", "supportView.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("nao foi possivel ler %s: %v", htmlPath, err)
	}
	htmlText := string(html)
	for _, id := range []string{"commentAttachments", "newTicketAttachments"} {
		needle := "id=\"" + id + "\" class=\"chat-attachments hidden\""
		if !strings.Contains(htmlText, needle) {
			t.Fatalf("o container %q deve usar a barra do chat (%s)", id, needle)
		}
	}

	// 4) CSS: a miniatura precisa respeitar o atributo hidden (senao o
	//    placeholder aparecia como imagem quebrada antes do download).
	cssPath := filepath.Join("..", "..", "frontend", "styles", "responsive-part-a.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("nao foi possivel ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".ticket-msg-attachment-thumb[hidden]") {
		t.Fatal("responsive-part-a.css precisa esconder a miniatura enquanto o anexo nao foi baixado")
	}
}
