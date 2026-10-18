package supportchat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guarda de regressão da UI da janela do chat.
//
// BUG CORRIGIDO: `.sc-modal-overlay { display: flex }` vence o atributo `hidden`
// do navegador (o UA só aplica `display: none` enquanto nenhum CSS do autor
// sobrescrever). Sem a regra `.sc-modal-overlay[hidden]`, os modais de convite e
// de encerramento ficavam AMBOS visíveis — o de encerramento cobria o do convite
// e `el.hidden = true` não escondia nada ("Continuar conversando" não fazia
// nada). Este teste falha se a invariante for quebrada.
func TestChatWindow_ModaisRespeitamAtributoHidden(t *testing.T) {
	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}

	if !strings.Contains(string(css), ".sc-modal-overlay[hidden]") {
		t.Fatal("support-chat.css precisa conter a regra '.sc-modal-overlay[hidden] { display: none; }' — sem ela o atributo hidden não esconde os modais")
	}

	htmlPath := filepath.Join("..", "..", "frontend", "chat-window.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}

	htmlText := string(html)
	for _, id := range []string{"scInvite", "scConfirm"} {
		needle := "id=\"" + id + "\" class=\"sc-modal-overlay\" hidden"
		if !strings.Contains(htmlText, needle) {
			t.Fatalf("o modal %q deve começar oculto (%s)", id, needle)
		}
	}

	// A confirmação também precisa ser context-aware (recusar convite x encerrar).
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}

	jsText := string(js)
	if !strings.Contains(jsText, "confirmMode") {
		t.Fatal("app-support-chat.js deve tratar o contexto do modal de confirmação (convite x encerramento)")
	}
	if !strings.Contains(jsText, "function on(") {
		t.Fatal("app-support-chat.js deve ter wiring resiliente (helper on()) para um elemento ausente não derrubar o resto dos listeners")
	}
}

// Guarda de regressão: mensagem de SISTEMA (quem encerrou a conversa) precisa
// ser renderizada como NOTA centralizada, não como fala de um dos lados.
//
// O servidor grava a mensagem de sistema no encerramento ("Chat de suporte
// encerrado pelo usuário/suporte") e a envia ao agent. Sem tratamento dedicado
// ela caía no ramo "não é minha" e aparecia como se o SUPORTE tivesse escrito
// aquilo — e, pior, a janela podia ser fechada automaticamente, tirando do
// usuário o que já havia sido conversado.
func TestChatWindow_MensagemDeSistemaViraNota(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, "isSystemMessage") {
		t.Fatal("app-support-chat.js deve reconhecer mensagens de sistema (isSystemMessage)")
	}
	if !strings.Contains(jsText, "sc-system") {
		t.Fatal("mensagens de sistema devem ser renderizadas como nota (classe sc-system), não como bolha de conversa")
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".sc-system") {
		t.Fatal("support-chat.css precisa estilizar a nota de sistema (.sc-system)")
	}
}

// Guarda de regressão: a janela do chat também identifica o TÉCNICO que abriu a
// conversa e mostra os COMENTÁRIOS do chamado (conversa do ticket), separados do
// chat ao vivo. Notas internas do suporte NÃO podem aparecer para o usuário
// final, e o painel (display: flex) precisa respeitar o atributo hidden — mesmo
// caso dos modais.
func TestChatWindow_TecnicoEComentariosDoChamado(t *testing.T) {
	htmlPath := filepath.Join("..", "..", "frontend", "chat-window.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	htmlText := string(html)
	for _, needle := range []string{`id="scTechnician"`, `id="scComments"`, `id="scCommentsToggle"`, `id="scCommentsList"`} {
		if !strings.Contains(htmlText, needle) {
			t.Fatalf("chat-window.html deve conter %s", needle)
		}
	}

	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)
	if !strings.Contains(jsText, `call("GetTicketComments", ticketId)`) {
		t.Fatal("app-support-chat.js deve carregar os comentários do chamado (GetTicketComments)")
	}
	if !strings.Contains(jsText, "isInternal") {
		t.Fatal("app-support-chat.js deve filtrar as notas internas (isInternal) do suporte")
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	for _, rule := range []string{".sc-comments[hidden]", ".sc-comments-list[hidden]"} {
		if !strings.Contains(string(css), rule) {
			t.Fatalf("support-chat.css precisa conter %q — display:flex do autor vence o hidden do UA", rule)
		}
	}
}

// REQUISITO: depois que a conversa TERMINA (inclusive quando quem encerrou foi
// o suporte) a janela não pode mais anunciar "sem conexão em tempo real": não há
// nada a reenviar. O estado do serviço é limpo no fim e a janela fica aberta em
// leitura, então o frontend precisa derivar "conversa encerrada" das mensagens
// que já estão na tela — era aí que o aviso de reconexão reaparecia.
//
// REQUISITO: com o X removido da janela, o botão de encerramento é o ÚNICO
// caminho de fechamento — ele não pode ficar desabilitado e, numa conversa já
// terminada, passa a fechar a janela.
func TestChatWindow_ConversaEncerradaNaoAnunciaReconexao(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, "function isConversationOver(") {
		t.Fatal("app-support-chat.js deve derivar o estado 'conversa encerrada' (isConversationOver)")
	}

	// A ordem importa: o ramo do fim precisa vencer o aviso de conexão.
	overIdx := strings.Index(jsText, "if (over) {")
	offlineIdx := strings.Index(jsText, "} else if (!state.connected) {")
	if overIdx < 0 || offlineIdx < 0 || overIdx > offlineIdx {
		t.Fatal("render() deve avaliar a conversa encerrada ANTES do aviso de conexão em tempo real")
	}

	if !strings.Contains(jsText, `over ? "Fechar" : "Encerrar"`) {
		t.Fatal("o botão deve virar 'Fechar' quando a conversa já terminou")
	}
	if !strings.Contains(jsText, "els.end.disabled = false;") {
		t.Fatal("o botão de encerramento é o único caminho de fechamento e não pode ficar desabilitado")
	}
}

// REQUISITO: "imagem" na janela do chat é o que o WebView2 RENDERIZA, não
// `image/*`. image/tiff, image/heic e afins estão na família `image/` mas não
// renderizam — cairiam no visor VAZIO (mesmo sintoma do PDF dentro de <img>).
// Todo o resto (PDF, log, csv, zip, exe, anexo sem tipo) BAIXA.
func TestChatWindow_ImagemRenderizavelNaoEhImageQualquer(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, "function isRenderableImage(") {
		t.Fatal("app-support-chat.js deve decidir imagem por tipo RENDERIZÁVEL (isRenderableImage)")
	}
	if strings.Contains(jsText, `indexOf("image/") === 0`) {
		t.Fatal("o critério antigo (`image/*`) voltou: TIFF/HEIC cairiam no visor vazio; use isRenderableImage")
	}
	if !strings.Contains(jsText, "RENDERABLE_IMAGE_TYPES") || !strings.Contains(jsText, "IMAGE_EXTENSIONS") {
		t.Fatal("a lista de tipos renderizáveis e o fallback por extensão (sem MIME) precisam existir")
	}
	if !strings.Contains(jsText, `triggerDownload(`) {
		t.Fatal("os tipos não renderizáveis precisam BAIXAR (triggerDownload)")
	}
}

// BUG CORRIGIDO: o anexo/imagem enviado pelo suporte não aparecia na janela do
// chat. Além de o envelope do NATS não trazer os anexos, o merge do frontend
// mantinha a cópia antiga (sem anexo) quando o snapshot canônico chegava — e o
// clique no chip apenas BAIXAVA o arquivo, sem forma de ver a imagem.
func TestChatWindow_AnexoDeImagemTemVisorEMergeCanonico(t *testing.T) {
	htmlPath := filepath.Join("..", "..", "frontend", "chat-window.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	for _, needle := range []string{`id="scAttachmentViewer"`, `id="scViewerImage"`, `id="scViewerClose"`} {
		if !strings.Contains(string(html), needle) {
			t.Fatalf("chat-window.html deve conter %s (visor do anexo de imagem)", needle)
		}
	}

	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)
	for _, needle := range []string{"function openAttachment(", "function payloadToBlob(", "function messageKey("} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js deve declarar %s", needle)
		}
	}

	// O snapshot CANÔNICO (incoming) precisa ser aplicado ANTES da cópia local:
	// era a ordem inversa que deixava o anexo invisível para sempre.
	incomingIdx := strings.Index(jsText, "(incoming || []).forEach(push);")
	existingIdx := strings.Index(jsText, "(state.messages || []).forEach(push);")
	if incomingIdx < 0 || existingIdx < 0 || incomingIdx > existingIdx {
		t.Fatal("mergeMessages deve aplicar o snapshot canônico (incoming) antes da cópia local, caso contrário a mensagem enriquecida com anexos é descartada")
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".sc-viewer-image") {
		t.Fatal("support-chat.css precisa estilizar o visor do anexo (.sc-viewer-image)")
	}
}

// BUG CORRIGIDO: o anexo de imagem de um COMENTÁRIO na conversa aparecia só
// como um chip com o nome do arquivo, enquanto o anexo do chat ao vivo já
// tinha miniatura e visor. Agora os dois usam a mesma linguagem visual.
func TestChatWindow_ComentarioComAnexoDeImagemTemMiniatura(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)
	for _, needle := range []string{
		"function buildCommentAttachments(",
		"function loadCommentThumbnail(",
		`call("DownloadTicketCommentAttachment", state.ticketId, comment.id, attachment.id)`,
		"sc-attachment--thumb",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js deve conter %q (miniatura do anexo de comentário)", needle)
		}
	}
}

// REQUISITO (definido pelo usuário): o convite do suporte precisa aparecer por
// cima de TODOS os aplicativos abertos. O Go garante "sempre no topo" enquanto
// o convite está pendente (supportChatShouldFloat); a janela precisa AVISAR o
// usuário disso, senão a janela flutuando sem explicação parece travamento.
//
// Também é o convite que dá o título da janela: o tooltip do botão na barra de
// tarefas mostra o título, e é o que sobra quando o usuário está num app em tela
// cheia.
func TestChatWindow_ConviteAvisaQueJanelaFicaNoTopo(t *testing.T) {
	htmlPath := filepath.Join("..", "..", "frontend", "chat-window.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	htmlText := string(html)

	if !strings.Contains(htmlText, `id="scInviteHint"`) {
		t.Fatal("chat-window.html deve conter o aviso de que a janela fica acima dos outros apps (scInviteHint)")
	}

	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, "Convite do suporte") {
		t.Fatal("app-support-chat.js deve dar um título próprio ao convite pendente (document.title)")
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".sc-modal-hint") {
		t.Fatal("support-chat.css precisa estilizar o aviso do convite (.sc-modal-hint)")
	}
}
