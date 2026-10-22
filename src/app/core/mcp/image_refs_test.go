package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ── Payloads reais mínimos (magic bytes válidos) ──────────────────────────

var (
	testPNGData  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	testJPEGData = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
	testWebPData = []byte{'R', 'I', 'F', 'F', 0x20, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 0x56, 0x50}
	testGIFData  = []byte("GIF89a" + "\x00\x00")
)

func imageSignature(contentType string) []byte {
	switch strings.ToLower(contentType) {
	case "image/png":
		return testPNGData
	case "image/jpeg", "image/jpg":
		return testJPEGData
	case "image/webp":
		return testWebPData
	case "image/gif":
		return testGIFData
	default:
		return nil
	}
}

// makeImagePayload devolve bytes de tamanho n com a assinatura real do tipo no
// início (o restante é filler) — permite testar tetos sem payload inválido.
func makeImagePayload(contentType string, n int) []byte {
	sig := imageSignature(contentType)
	if n < len(sig) {
		n = len(sig)
	}
	out := bytes.Repeat([]byte{0}, n)
	copy(out, sig)
	return out
}

func makeImageDataURL(contentType string, decodedBytes int) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(makeImagePayload(contentType, decodedBytes))
}

func validImageDataURL(contentType string) string {
	return makeImageDataURL(contentType, len(imageSignature(contentType)))
}

// ── Fake AppBridge para exercitar as tools de anexo ────────────────────────

type imageTestBridge struct {
	AppBridge

	lastTurn []string

	order []string

	attachedTicketImages  []TicketImageUpload
	attachedCommentImages []TicketImageUpload
	attachTicketErr       error
	attachCommentErr      error

	commentJSON string
	commentErr  error
	ticketJSON  string
	ticketErr   error

	attachmentsJSON string
	attachmentsErr  error

	screenshotJSON string
	screenshotErr  error

	downloadJSON  string
	downloadErr   error
	downloadCalls []string
}

func (f *imageTestBridge) GetLastTurnImage(index int) (string, error) {
	if index <= 0 || index > len(f.lastTurn) {
		return "", fmt.Errorf("imagem turn:%d indisponivel", index)
	}
	return f.lastTurn[index-1], nil
}

func (f *imageTestBridge) AttachImagesToTicket(_ string, images []TicketImageUpload) ([]string, error) {
	f.order = append(f.order, "ticket_attach")
	f.attachedTicketImages = images
	ids := make([]string, 0, len(images))
	for i := range images {
		ids = append(ids, fmt.Sprintf("ticket-att-%d", i+1))
	}
	if f.attachTicketErr != nil {
		// Simula upload parcial: o primeiro anexo foi criado antes da falha.
		return ids[:1], f.attachTicketErr
	}
	return ids, nil
}

func (f *imageTestBridge) AttachImagesToComment(_, _ string, images []TicketImageUpload) ([]string, error) {
	f.order = append(f.order, "comment_attach")
	f.attachedCommentImages = images
	ids := make([]string, 0, len(images))
	for i := range images {
		ids = append(ids, fmt.Sprintf("comment-att-%d", i+1))
	}
	if f.attachCommentErr != nil {
		return ids[:1], f.attachCommentErr
	}
	return ids, nil
}

func (f *imageTestBridge) ListAgentTicketAttachments(ticketID string) (json.RawMessage, error) {
	if f.attachmentsErr != nil {
		return nil, f.attachmentsErr
	}
	if f.attachmentsJSON != "" {
		return json.RawMessage(f.attachmentsJSON), nil
	}
	return json.Marshal(map[string]any{"ticketId": ticketID, "attachments": []any{}})
}

func (f *imageTestBridge) DownloadAgentTicketAttachment(ticketID, attachmentID, commentID string) (json.RawMessage, error) {
	f.downloadCalls = append(f.downloadCalls, ticketID+"/"+attachmentID+"/"+commentID)
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if f.downloadJSON != "" {
		return json.RawMessage(f.downloadJSON), nil
	}
	return json.Marshal(map[string]any{
		"image_base64": base64.StdEncoding.EncodeToString(testPNGData),
		"mime":         "image/png",
		"fileName":     "print.png",
		"bytes":        len(testPNGData),
	})
}

func (f *imageTestBridge) AddAgentTicketComment(_, _ string) (json.RawMessage, error) {
	f.order = append(f.order, "comment")
	if f.commentErr != nil {
		return nil, f.commentErr
	}
	if f.commentJSON != "" {
		return json.RawMessage(f.commentJSON), nil
	}
	return json.RawMessage(`{"id":"comment-1","content":"ok"}`), nil
}

func (f *imageTestBridge) CreateAgentTicket(_, _ string, _ int, _, _, _, _, _ string) (json.RawMessage, error) {
	f.order = append(f.order, "ticket")
	if f.ticketErr != nil {
		return nil, f.ticketErr
	}
	if f.ticketJSON != "" {
		return json.RawMessage(f.ticketJSON), nil
	}
	return json.RawMessage(`{"id":"ticket-1","title":"teste"}`), nil
}

func (f *imageTestBridge) CaptureScreenshotForTool(context.Context, map[string]any) (json.RawMessage, error) {
	if f.screenshotErr != nil {
		return nil, f.screenshotErr
	}
	if f.screenshotJSON != "" {
		return json.RawMessage(f.screenshotJSON), nil
	}
	raw, err := json.Marshal(map[string]any{
		"ok":           true,
		"type":         "screenshot",
		"mime":         "image/png",
		"image_base64": base64.StdEncoding.EncodeToString(testPNGData),
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func newImageTestRegistry(fake *imageTestBridge) *Registry {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, fake)
	return reg
}

func callImageTool(t *testing.T, reg *Registry, tool string, args map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return reg.Call(context.Background(), tool, raw)
}

// ── Parsing / validação puros ─────────────────────────────────────────────

func TestParseImageRefsArg(t *testing.T) {
	refs, err := parseImageRefsArg(" turn:1, shot:last ,turn:1,, ")
	if err != nil {
		t.Fatalf("parseImageRefsArg: %v", err)
	}
	if want := []string{"turn:1", "shot:last"}; fmt.Sprint(refs) != fmt.Sprint(want) {
		t.Fatalf("refs = %v, esperado %v", refs, want)
	}

	refs, err = parseImageRefsArg(`["turn:2","shot:last"]`)
	if err != nil || len(refs) != 2 || refs[0] != "turn:2" {
		t.Fatalf("array JSON: refs=%v err=%v", refs, err)
	}

	for _, invalid := range []string{"", "   ", "[]", `[1,2]`, "[turn:1"} {
		if _, err := parseImageRefsArg(invalid); err == nil {
			t.Errorf("parseImageRefsArg(%q) deveria falhar", invalid)
		}
	}
}

func TestValidateImageDataURL(t *testing.T) {
	valid := map[string]string{
		validImageDataURL("image/png"):  "image/png",
		validImageDataURL("image/jpeg"): "image/jpeg",
		// image/jpg é normalizado para o MIME padrão aceito pela policy.
		"data:image/jpg;base64," + base64.StdEncoding.EncodeToString(testJPEGData): "image/jpeg",
		validImageDataURL("image/webp"):                                            "image/webp",
		validImageDataURL("image/gif"):                                             "image/gif",
	}
	for dataURL, wantType := range valid {
		contentType, data, err := validateImageDataURL(dataURL)
		if err != nil {
			t.Errorf("%.40q: erro inesperado: %v", dataURL, err)
			continue
		}
		if contentType != wantType || len(data) == 0 {
			t.Errorf("%.40q: contentType=%q data=%d", dataURL, contentType, len(data))
		}
	}

	invalid := []string{
		"",
		"http://exemplo/imagem.png",
		"data:text/plain;base64,AQID",
		"data:image/svg+xml;base64,AQID",
		"data:image/png,AQID",                 // sem base64
		"data:image/png;base64,@@@",           // base64 inválido
		"data:image/png;base64,",              // vazia
		makeImageDataURL("image/png", 12<<20), // 12 MiB decodificados > 14 MiB em data URL
		// MIME declarado não bate com a assinatura real dos bytes.
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(testJPEGData),
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(testPNGData),
		// Bytes que não são imagem reconhecida.
		"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<html>nao e imagem</html>")),
	}
	for _, dataURL := range invalid {
		if _, _, err := validateImageDataURL(dataURL); err == nil {
			t.Errorf("data URL deveria ser rejeitada: %.60q", dataURL)
		}
	}
}

// ── Resolução turn:N / shot:last / shot:<hash> ────────────────────────────

func TestResolveImageRefsTurnAndShot(t *testing.T) {
	resetScreenshotCacheForTests()

	fake := &imageTestBridge{lastTurn: []string{
		validImageDataURL("image/png"),
		validImageDataURL("image/jpeg"),
	}}
	uploads, err := resolveImageRefs("turn:1,turn:2", fake)
	if err != nil {
		t.Fatalf("resolve turn: %v", err)
	}
	if len(uploads) != 2 {
		t.Fatalf("uploads = %d, esperado 2", len(uploads))
	}
	if uploads[0].FileName != "chat-image-1.png" || uploads[0].ContentType != "image/png" || uploads[0].DataBase64 != base64.StdEncoding.EncodeToString(testPNGData) {
		t.Errorf("turn:1 inesperado: %+v", uploads[0])
	}
	if uploads[1].FileName != "chat-image-2.jpg" || uploads[1].ContentType != "image/jpeg" {
		t.Errorf("turn:2 inesperado: %+v", uploads[1])
	}

	ref := cacheScreenshotImage(validImageDataURL("image/webp"))
	if !strings.HasPrefix(ref, "shot:") || len(ref) != len("shot:")+64 {
		t.Fatalf("ref de captura inesperada: %q", ref)
	}
	uploads, err = resolveImageRefs("shot:last", fake)
	if err != nil {
		t.Fatalf("resolve shot:last: %v", err)
	}
	if len(uploads) != 1 || uploads[0].ContentType != "image/webp" || uploads[0].FileName != "chat-image-1.webp" {
		t.Fatalf("shot:last inesperado: %+v", uploads)
	}
	uploads, err = resolveImageRefs(ref, fake)
	if err != nil || len(uploads) != 1 {
		t.Fatalf("resolve %s: uploads=%v err=%v", ref, uploads, err)
	}
}

func TestResolveImageRefsErrors(t *testing.T) {
	resetScreenshotCacheForTests()
	fake := &imageTestBridge{lastTurn: []string{validImageDataURL("image/png")}}

	cases := map[string]string{
		"turn:0":                          "turn",
		"turn:x":                          "turn",
		"turn:9":                          "indisponivel",
		"shot:last":                       "indisponivel",
		"shot:abc":                        "shot",
		"shot:" + strings.Repeat("z", 64): "shot",
		"desconhecida":                    "nao suportada",
	}
	for ref, want := range cases {
		if _, err := resolveImageRefs(ref, fake); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("resolve %q: erro %v deveria conter %q", ref, err, want)
		}
	}
}

func TestResolveImageRefsTotalCap(t *testing.T) {
	resetScreenshotCacheForTests()
	// Cada imagem decodifica 4 MB (< 6 MiB em data URL). Nove passam do teto
	// total de 32 MiB.
	basePayload := makeImagePayload("image/png", 4<<20)
	fake := &imageTestBridge{lastTurn: make([]string, 9)}
	for i := range fake.lastTurn {
		// Imagens distintas (o dedupe por hash não pode colapsar as 9).
		payload := append([]byte(nil), basePayload...)
		payload[len(payload)-1] = byte(i + 1)
		fake.lastTurn[i] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
	}
	refs := make([]string, 0, 9)
	for i := 1; i <= 9; i++ {
		refs = append(refs, fmt.Sprintf("turn:%d", i))
	}
	_, err := resolveImageRefs(strings.Join(refs, ","), fake)
	if err == nil || !strings.Contains(err.Error(), "teto total") {
		t.Fatalf("esperado erro de teto total, got=%v", err)
	}
}

// ── Tools MCP ─────────────────────────────────────────────────────────────

func TestAttachImagesToTicketTool(t *testing.T) {
	resetScreenshotCacheForTests()
	fake := &imageTestBridge{lastTurn: []string{validImageDataURL("image/png")}}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "attach_images_to_ticket", map[string]any{
		"ticketId":  "ticket-1",
		"imageRefs": "turn:1",
		"confirm":   true,
	})
	if err != nil {
		t.Fatalf("attach_images_to_ticket: %v", err)
	}
	if len(fake.attachedTicketImages) != 1 || fake.attachedTicketImages[0].FileName != "chat-image-1.png" {
		t.Fatalf("bridge recebeu imagens inesperadas: %+v", fake.attachedTicketImages)
	}
	payload, _ := result.(map[string]any)
	attachments, _ := payload["attachments"].([]map[string]any)
	if payload["ok"] != true || payload["ticketId"] != "ticket-1" || len(attachments) != 1 {
		t.Fatalf("resultado inesperado: %#v", result)
	}
	if attachments[0]["attachmentId"] != "ticket-att-1" || attachments[0]["fileName"] != "chat-image-1.png" {
		t.Fatalf("metadados inesperados: %#v", attachments[0])
	}
	// Regressão: o metadata precisa refletir o tamanho real decodificado
	// (a implementação anterior usava DecodedLen e devolvia 0 em alguns casos).
	if size, _ := attachments[0]["sizeBytes"].(int64); size != int64(len(testPNGData)) {
		t.Fatalf("sizeBytes inesperado: %#v", attachments[0]["sizeBytes"])
	}
	if _, hasBytes := attachments[0]["dataBase64"]; hasBytes {
		t.Fatal("metadados nao devem expor bytes")
	}

	// Sem confirm=true (uso direto do registry, fora do gate): recusa.
	if _, err := callImageTool(t, reg, "attach_images_to_ticket", map[string]any{
		"ticketId":  "ticket-1",
		"imageRefs": "turn:1",
	}); err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("sem confirm deveria falhar, got=%v", err)
	}
}

func TestAttachImagesToCommentTool(t *testing.T) {
	resetScreenshotCacheForTests()
	cacheScreenshotImage(validImageDataURL("image/gif"))
	fake := &imageTestBridge{}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "attach_images_to_ticket_comment", map[string]any{
		"ticketId":  "ticket-1",
		"commentId": "comment-9",
		"imageRefs": "shot:last",
		"confirm":   true,
	})
	if err != nil {
		t.Fatalf("attach_images_to_ticket_comment: %v", err)
	}
	payload, _ := result.(map[string]any)
	if payload["commentId"] != "comment-9" || len(fake.attachedCommentImages) != 1 {
		t.Fatalf("resultado inesperado: %#v", result)
	}
}

func TestListTicketAttachmentsTool(t *testing.T) {
	fake := &imageTestBridge{attachmentsJSON: `{"ticketId":"ticket-1","attachments":[{"id":"a1","fileName":"print.png","contentType":"image/png","sizeBytes":3,"origin":"repository"}]}`}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "list_ticket_attachments", map[string]any{"ticketId": "ticket-1"})
	if err != nil {
		t.Fatalf("list_ticket_attachments: %v", err)
	}
	raw, ok := result.(json.RawMessage)
	if !ok || !json.Valid(raw) || !strings.Contains(string(raw), `"fileName":"print.png"`) {
		t.Fatalf("resultado inesperado: %#v", result)
	}
	if strings.Contains(string(raw), "base64") {
		t.Fatal("listagem nao deve conter bytes/base64")
	}
}

func TestAddTicketCommentCreatesFirstAndPreservesOnAttachFailure(t *testing.T) {
	resetScreenshotCacheForTests()
	cacheScreenshotImage(validImageDataURL("image/png"))
	fake := &imageTestBridge{
		commentJSON:      `{"id":"comment-42","content":"detalhes"}`,
		attachCommentErr: fmt.Errorf("servidor recusou o upload"),
	}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "add_ticket_comment", map[string]any{
		"ticketId":        "ticket-1",
		"content":         "detalhes",
		"attachImageRefs": "shot:last",
		"confirm":         true,
	})
	if err != nil {
		t.Fatalf("falha de upload nao deve virar erro da tool: %v", err)
	}
	if got := strings.Join(fake.order, ","); got != "comment,comment_attach" {
		t.Fatalf("ordem = %q, esperado comment,comment_attach", got)
	}
	payload, _ := result.(map[string]any)
	if payload["ok"] != false || payload["commentId"] != "comment-42" {
		t.Fatalf("resultado deveria preservar commentId + erro: %#v", payload)
	}
	if msg, _ := payload["error"].(string); !strings.Contains(msg, "comentario criado") || !strings.Contains(msg, "servidor recusou") {
		t.Fatalf("erro pouco claro: %q", msg)
	}
	attachments, _ := payload["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["attachmentId"] != "comment-att-1" {
		t.Fatalf("IDs parciais do comentario deveriam ser devolvidos: %#v", payload["attachments"])
	}
}

func TestAddTicketCommentWithoutRefsKeepsLegacyBehavior(t *testing.T) {
	reg := newImageTestRegistry(&imageTestBridge{commentJSON: `{"id":"comment-1"}`})
	result, err := callImageTool(t, reg, "add_ticket_comment", map[string]any{"ticketId": "ticket-1", "content": "oi"})
	if err != nil {
		t.Fatalf("add_ticket_comment sem refs: %v", err)
	}
	raw, ok := result.(json.RawMessage)
	if !ok || string(raw) != `{"id":"comment-1"}` {
		t.Fatalf("sem refs deve devolver o payload original: %#v", result)
	}
}

func TestCreateTicketAttachFailurePreservesTicket(t *testing.T) {
	resetScreenshotCacheForTests()
	cacheScreenshotImage(validImageDataURL("image/png"))
	fake := &imageTestBridge{
		ticketJSON:      `{"id":"ticket-77","title":"novo"}`,
		attachTicketErr: fmt.Errorf("sem espaco"),
	}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "create_ticket", map[string]any{
		"title":           "novo",
		"description":     "desc",
		"departmentId":    "dep-1",
		"attachImageRefs": "shot:last",
		"confirm":         true,
	})
	if err != nil {
		t.Fatalf("falha de upload nao deve virar erro da tool: %v", err)
	}
	if got := strings.Join(fake.order, ","); got != "ticket,ticket_attach" {
		t.Fatalf("ordem = %q", got)
	}
	payload, _ := result.(map[string]any)
	if payload["ok"] != false || payload["ticketId"] != "ticket-77" {
		t.Fatalf("resultado deveria preservar ticketId: %#v", payload)
	}
	attachments, _ := payload["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["attachmentId"] != "ticket-att-1" {
		t.Fatalf("IDs parciais do chamado deveriam ser devolvidos: %#v", payload["attachments"])
	}
}

func TestResolveImageRefsDeduplicatesByDecodedHash(t *testing.T) {
	resetScreenshotCacheForTests()
	// Duas refs diferentes apontando para os MESMOS bytes: só um upload.
	fake := &imageTestBridge{lastTurn: []string{
		validImageDataURL("image/png"),
		validImageDataURL("image/png"),
	}}
	uploads, err := resolveImageRefs("turn:1,turn:2", fake)
	if err != nil {
		t.Fatalf("resolve com refs repetidas: %v", err)
	}
	if len(uploads) != 1 {
		t.Fatalf("dedupe por hash falhou: %d uploads", len(uploads))
	}
	if uploads[0].FileName != "chat-image-1.png" {
		t.Fatalf("nome pos-dedupe inesperado: %+v", uploads[0])
	}
}

func TestViewTicketAttachmentTool(t *testing.T) {
	fake := &imageTestBridge{}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "view_ticket_attachment", map[string]any{
		"ticketId":     "ticket-1",
		"attachmentId": "att-1",
		"confirm":      true,
	})
	if err != nil {
		t.Fatalf("view_ticket_attachment: %v", err)
	}
	raw, ok := result.(json.RawMessage)
	if !ok || !json.Valid(raw) {
		t.Fatalf("resultado inesperado: %#v", result)
	}
	body := string(raw)
	for _, want := range []string{"\"fileName\":\"print.png\"", "\"mime\":\"image/png\"", "\"image_base64\"", "\"bytes\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("resultado sem %s: %s", want, body)
		}
	}
	if len(fake.downloadCalls) != 1 || fake.downloadCalls[0] != "ticket-1/att-1/" {
		t.Fatalf("download do repositorio inesperado: %v", fake.downloadCalls)
	}

	// Anexo de comentário: commentId é repassado ao bridge.
	if _, err := callImageTool(t, reg, "view_ticket_attachment", map[string]any{
		"ticketId":     "ticket-1",
		"attachmentId": "att-2",
		"commentId":    "comment-1",
		"confirm":      true,
	}); err != nil {
		t.Fatalf("view anexo de comentario: %v", err)
	}
	if got := fake.downloadCalls[len(fake.downloadCalls)-1]; got != "ticket-1/att-2/comment-1" {
		t.Fatalf("commentId nao repassado: %q", got)
	}

	// Sem confirm (fora do gate) recusa antes de baixar.
	if _, err := callImageTool(t, reg, "view_ticket_attachment", map[string]any{
		"ticketId": "ticket-1", "attachmentId": "att-1",
	}); err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("sem confirm deveria falhar, got=%v", err)
	}
}

func TestViewTicketAttachmentErrorIsClear(t *testing.T) {
	fake := &imageTestBridge{downloadErr: fmt.Errorf("anexo nao e uma imagem suportada (png, jpeg, webp ou gif)")}
	reg := newImageTestRegistry(fake)
	_, err := callImageTool(t, reg, "view_ticket_attachment", map[string]any{
		"ticketId": "ticket-1", "attachmentId": "att-1", "confirm": true,
	})
	if err == nil || !strings.Contains(err.Error(), "imagem suportada") {
		t.Fatalf("erro do bridge deveria ser propagado, got=%v", err)
	}
}

func TestCaptureScreenshotAddsImageRef(t *testing.T) {
	resetScreenshotCacheForTests()
	fake := &imageTestBridge{}
	reg := newImageTestRegistry(fake)

	result, err := callImageTool(t, reg, "capture_screenshot", map[string]any{"mode": "full"})
	if err != nil {
		t.Fatalf("capture_screenshot: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("resultado inesperado: %#v", result)
	}
	ref, _ := payload["image_ref"].(string)
	if !strings.HasPrefix(ref, "shot:") || len(ref) != len("shot:")+64 {
		t.Fatalf("image_ref inesperada: %q", ref)
	}
	if payload["image_base64"] != base64.StdEncoding.EncodeToString(testPNGData) {
		t.Fatal("image_base64 deveria continuar no payload")
	}
	uploads, err := resolveImageRefs(ref, fake)
	if err != nil || len(uploads) != 1 || uploads[0].ContentType != "image/png" {
		t.Fatalf("ref da captura nao resolve: uploads=%v err=%v", uploads, err)
	}
}
