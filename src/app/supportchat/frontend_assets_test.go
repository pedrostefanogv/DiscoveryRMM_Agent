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
// ── Abertura da janela: nada de "sem conexão" numa conversa recém-aberta ────
//
// RELATO: "mando abrir o chat, às vezes demora e aparece a mensagem de sem
// conexão". O banner depende de state.link, com quatro valores:
//
//	opening  janela recém-aberta sem conversa carregada (NUNCA mostra o aviso);
//	live     conversa carregada / canal saudável;
//	offline  SÓ com o último snapshot falhando e sem conversa viva;
//	over     conversa encerrada.
//
// A ordem dos ramos do render() e a fonte do link (o BACKEND, não o processo de
// UI) são o contrato deste teste.
func TestChatWindow_AberturaNaoAnunciaSemConexao(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, `els.subtitle.textContent = "Abrindo a conversa…";`) {
		t.Fatal("a janela recém-aberta deve anunciar 'Abrindo a conversa…' em vez de 'Reconectando ao servidor…'")
	}

	// O ramo de abertura precede o de offline: o aviso só aparece depois do prazo.
	openingIdx := strings.Index(jsText, `state.link === "opening"`)
	offlineIdx := strings.Index(jsText, `state.link === "offline"`)
	if openingIdx < 0 || offlineIdx < 0 || openingIdx > offlineIdx {
		t.Fatal("render() deve tratar 'opening' ANTES de 'offline'")
	}

	// O link é calculado por recomputeLink(), e só cai para offline quando o
	// ÚLTIMO SNAPSHOT falhou — o processo de UI não tem NATS próprio.
	if !strings.Contains(jsText, "function recomputeLink()") {
		t.Fatal("o link precisa ser derivado por recomputeLink()")
	}
	// "offline" é atribuído SOMENTE quando o último snapshot falhou; um snapshot
	// bem-sucedido sem conversa cai em "idle" (diz o que é, sem prometer
	// reconexão) — nunca fica preso em "Abrindo a conversa…".
	if !strings.Contains(jsText, `state.link = "offline";`) {
		t.Fatal("'offline' só pode ser derivado de uma falha real de snapshot")
	}
	if !strings.Contains(jsText, `state.link = "idle";`) {
		t.Fatal("sem conversa e sem erro o link precisa cair em 'idle' (não pode ficar em 'opening' para sempre)")
	}
	if strings.Contains(jsText, "state.connected = !!next.connected") {
		t.Fatal("o link não pode mais depender do connected do próprio processo de UI")
	}
}

// REQUISITO: eventos e snapshots podem se cruzar (IPC x binding). Um evento
// 'waiting' emitido ANTES do aceite não pode derrubar a conversa já 'active',
// e 'ended' é terminal para a sessão.
func TestChatWindow_EstadoMonotonicoPorSessao(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	for _, needle := range []string{
		"state.sessionState[nextSession]",
		`previousStatus === "ended" && nextStatus !== "ended"`,
		`previousStatus === "active" && nextStatus === "waiting"`,
		"previousLiveSession === nextSession",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("applyState precisa do guarda monotônico %q", needle)
		}
	}
}

// REQUISITO: a janela REUSADA no aceite precisa buscar a conversa nova (o Go
// emite supportchat:refresh) e precisa reconhecer a sessão trocada para não
// mostrar o transcript antigo.
func TestChatWindow_ReusoDaJanelaRecarregaConversa(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	for _, needle := range []string{
		"supportchat:refresh",
		"function beginOpening()",
		"function openingParams()",
		"SupportChatWindowPainted",
		"state.chatSessionId",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("janela do chat precisa de %q para o reuso no aceite", needle)
		}
	}
}

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

	// A ordem importa: o ramo do fim precisa vencer os outros. O aviso de
	// conexão agora é um VALOR de link ("offline"), decidido por
	// recomputeLink() — que também coloca "over" em primeiro lugar.
	overIdx := strings.Index(jsText, "if (over) {")
	offlineIdx := strings.Index(jsText, "} else if (state.link === \"idle\") {")
	if overIdx < 0 || offlineIdx < 0 || overIdx > offlineIdx {
		t.Fatal("render() deve avaliar a conversa encerrada ANTES do aviso de conexão em tempo real")
	}
	linkOverIdx := strings.Index(jsText, "function recomputeLink() {")
	linkOfflineIdx := strings.Index(jsText, `state.link = "idle";`)
	overInsideLinkIdx := strings.Index(jsText, `state.link = "over";`)
	if linkOverIdx < 0 || linkOfflineIdx < 0 || overInsideLinkIdx < 0 || overInsideLinkIdx > linkOfflineIdx {
		t.Fatal("recomputeLink() deve derivar o fim da conversa antes de calcular offline")
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
	if strings.Contains(jsText, "triggerDownload(") || strings.Contains(jsText, "link.download") {
		t.Fatal("o anexo não pode ser baixado pelo webview (painel de downloads do Edge); use a gravação nativa")
	}
	if !strings.Contains(jsText, "SupportChatSaveAttachment") {
		t.Fatal("os tipos não renderizáveis precisam ser gravados em disco pelo backend (SupportChatSaveAttachment)")
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
	for _, needle := range []string{`id="scAttachmentViewer"`, `id="scViewerImage"`, `id="scViewerClose"`, `id="scViewerSave"`} {
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

// REQUISITO (definido pelo usuário): a imagem aberta no visor precisa ter uma
// opção explícita de baixar/salvar — antes o visor só mostrava o print.
// O salvamento usa o backend (sem painel de downloads do Edge) e o Shift+clique
// abre o "Salvar como" para escolher outro local.
func TestChatWindow_VisorDeImagemTemOpcaoDeSalvar(t *testing.T) {
	htmlPath := filepath.Join("..", "..", "frontend", "chat-window.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	if !strings.Contains(string(html), `id="scViewerSave"`) {
		t.Fatal("chat-window.html deve conter o botão de salvar imagem (scViewerSave)")
	}

	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)
	for _, needle := range []string{
		"els.viewerSave = byId(\"scViewerSave\");",
		"state.viewerSave = typeof onSave === \"function\" ? onSave : null;",
		"state.viewerSave(!!(event && event.shiftKey))",
		"Salvo em: ",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js deve conter %q (salvar imagem pelo visor)", needle)
		}
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".sc-viewer-save") {
		t.Fatal("support-chat.css precisa estilizar o botão de salvar do visor (.sc-viewer-save)")
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

// REQUISITO (definido pelo usuário em produção): o download de um anexo NÃO
// pode abrir o painel de downloads do Edge nem baixar uma cópia nova a cada
// clique. A janela salva o arquivo pelo backend (Go) numa pasta do usuário,
// guarda o caminho por anexo e, num segundo clique, apenas informa onde está.
// Shift+clique oferece o "Salvar como".
func TestChatWindow_AnexosSalvamEmDiscoSemPainelDeDownload(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	// Nada de Blob + <a download> (era o que abria o painel do Edge e baixava
	// de novo a cada clique).
	for _, forbidden := range []string{"triggerDownload(", "link.download", `createElement("a")`} {
		if strings.Contains(jsText, forbidden) {
			t.Fatalf("o download não pode passar pelo webview (encontrado %q); use a gravação nativa", forbidden)
		}
	}

	// Gravação nativa dos DOIS níveis (chat ao vivo e comentário) e diálogo
	// "Salvar como" para o Shift+clique.
	for _, needle := range []string{
		"function saveSupportChatAttachment(",
		"function saveCommentAttachment(",
		"SupportChatSaveAttachment",
		"SupportChatSaveAttachmentAs",
		"ChooseSupportChatAttachmentSavePath",
		"SaveTicketCommentAttachment",
		"SaveTicketCommentAttachmentAs",
		"ChooseTicketAttachmentSavePath",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js deve conter %q (gravação nativa do anexo)", needle)
		}
	}

	// Guarda contra download repetido: caminho salvo + promessa em andamento.
	for _, needle := range []string{
		"var savedAttachmentPaths = {};",
		"var pendingAttachmentSaves = {};",
		"Anexo já salvo em: ",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js deve conter %q (deduplicação de downloads)", needle)
		}
	}

	// As bindings geradas pelo Wails precisam conter os métodos Go: build antigo
	// sem regenerar faria o clique cair em "Binding indisponível".
	bindingsPath := filepath.Join("..", "..", "frontend", "bindings", "discovery", "app", "app.js")
	bindings, err := os.ReadFile(bindingsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", bindingsPath, err)
	}
	bindingsText := string(bindings)
	for _, needle := range []string{
		"export function SupportChatSaveAttachment(",
		"export function SupportChatSaveAttachmentAs(",
		"export function ChooseSupportChatAttachmentSavePath(",
	} {
		if !strings.Contains(bindingsText, needle) {
			t.Fatalf("bindings do Wails desatualizadas: falta %q; rode wails3 generate bindings", needle)
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

// REQUISITO: o seletor de arquivos da janela do chat precisa refletir a política
// do SERVIDOR (allowlist/tamanho), e não um espelho fixo no agent — antes o
// usuário escolhia o arquivo para só então descobrir que o tipo era proibido.
// A consulta também NÃO pode bloquear a abertura da janela (rede degradada).
func TestChatWindow_SeletorUsaPoliticaDoServidor(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if !strings.Contains(jsText, "function applyFileInputAccept(") {
		t.Fatal("app-support-chat.js deve aplicar a allowlist do servidor no seletor (applyFileInputAccept)")
	}
	if !strings.Contains(jsText, `setAttribute("accept"`) {
		t.Fatal("o filtro do seletor precisa ser aplicado no input de arquivo (accept)")
	}
	if !strings.Contains(jsText, "SupportChatAttachmentPolicyJSON") {
		t.Fatal("app-support-chat.js deve consultar a política de anexos (SupportChatAttachmentPolicyJSON)")
	}

	// A consulta da política NÃO pode bloquear a abertura da janela: ela termina
	// no próprio .catch (sem encadear applyState/refresh depois). Se alguém voltar
	// a encadear, uma rede degradada atrasaria o carregamento da conversa.
	policyIdx := strings.Index(jsText, "SupportChatAttachmentPolicyJSON")
	if policyIdx < 0 {
		t.Fatal("chamada da política não encontrada")
	}
	bootstrap := jsText[policyIdx:]

	applyIdx := strings.Index(bootstrap, "applyState({ status: \"none\"")
	if applyIdx < 0 {
		t.Fatal("applyState inicial não encontrado no bootstrap")
	}
	catchIdx := strings.Index(bootstrap, "/* mantém o default */")
	if catchIdx < 0 || catchIdx > applyIdx {
		t.Fatal("a consulta da política precisa terminar em .catch e não pode ser encadeada antes de carregar a conversa")
	}
	if strings.Contains(bootstrap[catchIdx:applyIdx], ".then(") {
		t.Fatal("o bootstrap não pode encadear a política antes de carregar a conversa (consulta não pode bloquear a janela)")
	}
}

// REQUISITO (relato 21/10): a conversa aparecia como texto puro, sem bolhas/
// destaque, porque support-chat.css só era linkado em chat-window.html — a aba
// EMBUTIDA da janela principal usa o mesmo CSS e ficava sem TODAS as classes
// .sc-*. Este teste falha se o link sumir do index.html.
func TestAbaEmbutida_CarregaCssDoChatDeSuporte(t *testing.T) {
	htmlPath := filepath.Join("..", "..", "frontend", "index.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	if !strings.Contains(string(html), "href=\"support-chat.css\"") {
		t.Fatal("index.html precisa linkar support-chat.css — sem isso a aba embutida perde bolhas, statusbar e composer do chat")
	}
}

// REQUISITO (relato 21/10): o item do menu só pode aparecer com sessão de
// suporte (waiting/active) e PRECISA sumir no encerramento. O atributo hidden
// era ignorado porque .sidebar-link define display:flex; a regra global
// [hidden] do base.css é o que faz o item realmente desaparecer. O ícone também
// não pode ser o mesmo balão do Chat IA.
func TestMenuAtendimentoAoVivo_SomeSemSessaoEIconeProprio(t *testing.T) {
	sidebarPath := filepath.Join("..", "..", "frontend", "partials", "sidebar.html")
	sidebar, err := os.ReadFile(sidebarPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", sidebarPath, err)
	}
	sidebarText := string(sidebar)

	for _, needle := range []string{"id=\"tabSupportChat\"", "sidebar-link--support"} {
		if !strings.Contains(sidebarText, needle) {
			t.Fatalf("sidebar.html precisa conter %q", needle)
		}
	}
	// O balão do Chat IA continua existindo no item tabChat; a checagem precisa
	// olhar SÓ o bloco do item de atendimento ao vivo.
	start := strings.Index(sidebarText, "id=\"tabSupportChat\"")
	if start < 0 {
		t.Fatal("item tabSupportChat não encontrado no sidebar.html")
	}
	endRel := strings.Index(sidebarText[start:], "</button>")
	if endRel < 0 {
		t.Fatal("não foi possível delimitar o item tabSupportChat no sidebar.html")
	}
	button := sidebarText[start : start+endRel]
	if strings.Contains(button, "M21 15a2 2 0 0 1-2 2H7l-4 4V5") {
		t.Fatal("o item do atendimento ao vivo não pode reusar o ícone/balão do Chat IA")
	}
	if !strings.Contains(button, "M4 14v-2a8 8 0 0 1 16 0v2") {
		t.Fatal("o item do atendimento ao vivo precisa do ícone de headset (distinto do Chat IA)")
	}

	baseCssPath := filepath.Join("..", "..", "frontend", "styles", "base.css")
	baseCss, err := os.ReadFile(baseCssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", baseCssPath, err)
	}
	if !strings.Contains(string(baseCss), "[hidden] { display: none !important; }") {
		t.Fatal("base.css precisa da regra global [hidden] — sem ela display:flex do .sidebar-link vence o hidden e o item não some no encerramento")
	}

	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	if !strings.Contains(string(js), "btn.hidden = !visible") {
		t.Fatal("updateSupportChatTabSurface precisa esconder o item quando não há sessão (waiting/active)")
	}
}

// REQUISITO (relato 21/10): o nome do técnico aparecia DUAS vezes na statusbar
// (subtitle com sufixo + elemento #scTechnician) e o chip piscava entre
// "encerrado" e "sem conversa". O técnico fica só no elemento próprio e o chip
// deriva do estado "conversa encerrada".
func TestStatusbar_NaoDuplicaTecnicoNemPiscaNoEncerramento(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	if strings.Contains(jsText, "techSuffix") {
		t.Fatal("render() não pode concatenar o técnico no subtitle: o nome já aparece em #scTechnician")
	}
	if !strings.Contains(jsText, "var label = over ? statusLabel(\"ended\") : statusLabel(state.status);") {
		t.Fatal("o chip precisa derivar de over (encerrado) para não piscar entre encerrado e sem conversa")
	}
	if !strings.Contains(jsText, "Conversa encerrada") {
		t.Fatal("o subtitle no fim deve ser estável (Conversa encerrada)")
	}
}

// REQUISITO (relato 21/10): "não consigo digitar nada" — o campo era
// desabilitado por um estado none aplicado por eventos com payload em ARRAY
// (Wails v3 entrega Event.Data como slice quando há mais de um argumento) e o
// composer continuava na tela parecendo disponível. O payload precisa ser
// normalizado e o composer precisa sair de cena no encerramento.
func TestEstado_NormalizaPayloadArrayEComposerSomeAoEncerrar(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	for _, needle := range []string{
		"function normalizeStatePayload(",
		"Array.isArray(payload)",
		"typeof next.hasSession !== \"boolean\"",
		"els.composer.hidden = over",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js precisa de %q para não travar o campo no meio da conversa", needle)
		}
	}

	htmlPath := filepath.Join("..", "..", "frontend", "partials", "views", "supportChatView.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", htmlPath, err)
	}
	for _, id := range []string{"scComposer", "scEndedNotice", "scEndedClose"} {
		if !strings.Contains(string(html), "id=\""+id+"\"") {
			t.Fatalf("supportChatView.html precisa conter id=%s", id)
		}
	}

	cssPath := filepath.Join("..", "..", "frontend", "support-chat.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), ".sc-ended") {
		t.Fatal("support-chat.css precisa estilizar a faixa de conversa encerrada (.sc-ended)")
	}
}

// REQUISITO (revisão por bugs 21/10): colar/arrastar arquivo só pode anexar ao
// chat de suporte quando a aba está em tela e o alvo está dentro dela. O
// listener global antigo enviava o print para o suporte mesmo colando no Chat
// IA e bloqueava o drag&drop nativo das outras telas. Também: o erro de envio
// precisa sobreviver ao render() (antes o banner era escondido no mesmo tick).
func TestAnexosPorPasteDropPertencemAoChatDeSuporte(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	// O working tree desta máquina usa CRLF; normaliza antes de comparar
	// trechos multilinha.
	jsText := strings.ReplaceAll(string(js), "\r\n", "\n")

	for _, needle := range []string{
		"function supportChatOwnsAttachmentTarget(",
		"if (!supportChatOwnsAttachmentTarget(event.target)) return;",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js precisa de %q para não capturar paste/drop de outras abas", needle)
		}
	}

	if !strings.Contains(jsText, "render();\n        showBanner(\"Falha ao enviar: \" + describe(err));") {
		t.Fatal("o banner de falha ao enviar precisa ser mostrado DEPOIS do render() — senão é escondido no mesmo tick")
	}
}

// REQUISITO (revisão por bugs 21/10): o handler de supportchat:state não pode
// pedir um snapshot a cada evento. refresh() emite supportchat:state; pedir
// refresh de novo no handler vira loop (GET /active contínuo + re-render).
// O snapshot inicial de mensagens é pedido UMA vez por sessão (aceite com
// transcript vazio).
func TestEstado_NaoDisparaRefreshEmLoop(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)
	for _, needle := range []string{
		"snapshotRequestedSession",
		"state.status === \"active\" && state.messages.length === 0",
		"snapshotRequestedSession !== state.chatSessionId",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-support-chat.js precisa de %q para não entrar em loop de refresh", needle)
		}
	}
}

// REQUISITO (revisão por bugs 21/10): falha do refresh depois de um envio bem
// sucedido não pode ser reportada como "Falha ao enviar" (o usuário reenviaria
// a mensagem que já foi). O send e o refresh precisam ser promessas separadas.
func TestEnvio_FalhaDeRefreshNaoViraFalhaDeEnvio(t *testing.T) {
	jsPath := filepath.Join("..", "..", "frontend", "js", "app-support-chat.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := strings.ReplaceAll(string(js), "\r\n", "\n")
	if !strings.Contains(jsText, "refresh().catch(function () { /* refresh() já avisa */ });") {
		t.Fatal("o refresh pós-envio precisa ser desacoplado do catch de envio")
	}
}


// REQUISITO (revisão por bugs 21/10): o mount da aba embutida PRECISA ser
// display:contents como os outros. Sem isso o #supportChatViewMount era um
// div block de altura automática dentro do .main-wrap (flex column): o
// .page-content não esticava, o painel do chat não preenchia a altura, o
// composer ficava grudado no topo das mensagens e sobrava um vazio enorme
// embaixo (o layout "estranho" da captura do usuário).
func TestAbaEmbutida_MountParticipaDoLayoutFlex(t *testing.T) {
	cssPath := filepath.Join("..", "..", "frontend", "styles", "base.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", cssPath, err)
	}
	if !strings.Contains(string(css), "#supportChatViewMount") {
		t.Fatal("base.css precisa incluir #supportChatViewMount na regra display:contents — sem isso o painel do chat não preenche a altura da janela")
	}

	shellPath := filepath.Join("..", "..", "frontend", "partials", "main-shell.html")
	shell, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", shellPath, err)
	}
	if !strings.Contains(string(shell), "supportChatViewMount") {
		t.Fatal("main-shell.html precisa montar a view do chat de suporte")
	}
}
