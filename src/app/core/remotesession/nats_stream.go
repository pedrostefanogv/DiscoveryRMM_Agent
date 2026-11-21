//go:build windows

package remotesession

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/remotesession/rdcleanpath"
	"discovery/app/core/screen"
)

// stripHyphens remove hifens de UUIDs para garantir compatibilidade com o
// formato de subject usado pelo servidor e viewer (UUIDs sem hifens).
func stripHyphens(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// validSessionIDCharset informa se o sessionId só contém os caracteres
// permitidos (hex/hífen — formato UUID). O sessionId vira SEGMENTO de subject
// NATS e o separador de tokens é '.': um sessionId com '.' (ou espaço) criaria
// subjects fora do escopo pretendido (injeção de token no subject, escapando
// do sufixo literal). Nada além de ^[0-9a-fA-F-]+$ é aceito; a validação é
// feita no sessionId BRUTO (antes do stripHyphens — hífens são permitidos).
func validSessionIDCharset(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F', c == '-':
		default:
			return false
		}
	}
	return true
}

// NatsStreamHandler manages NATS-based bidirectional communication for remote sessions.
type NatsStreamHandler struct {
	nc       *nats.Conn
	agentID  string
	tenantID string
	siteID   string

	// maxPayloadBytes é o limite de payload do servidor NATS. Frames maiores
	// são fragmentados (JUMBO) para não exceder o limite.
	maxPayloadBytes int
}

// DefaultMaxPayloadBytes é o padrão (2MB), alinhado ao servidor NATS.
const DefaultMaxPayloadBytes = 2 * 1024 * 1024

// NewNatsStreamHandler creates a new NATS stream handler.
// tenantID, siteID, agentID are required to construct literal subject patterns for publish.
// UUIDs are normalized (hyphens stripped) to match server subject format.
func NewNatsStreamHandler(nc *nats.Conn, tenantID, siteID, agentID string) *NatsStreamHandler {
	maxPayload := DefaultMaxPayloadBytes
	if nc != nil {
		if mp := int(nc.MaxPayload()); mp > 0 && mp < maxPayload {
			maxPayload = mp
		}
	}
	return &NatsStreamHandler{
		nc:              nc,
		tenantID:        stripHyphens(tenantID),
		siteID:          stripHyphens(siteID),
		agentID:         stripHyphens(agentID),
		maxPayloadBytes: maxPayload,
	}
}

// SetMaxPayloadBytes define o limite de payload (para alinhar ao servidor).
func (h *NatsStreamHandler) SetMaxPayloadBytes(max int) {
	if max > 0 {
		h.maxPayloadBytes = max
	}
}

// MaxPayloadBytes retorna o limite de payload configurado.
func (h *NatsStreamHandler) MaxPayloadBytes() int {
	return h.maxPayloadBytes
}

// EffectiveMaxPayloadBytes retorna o menor limite entre o configurado e nc.MaxPayload().
func (h *NatsStreamHandler) EffectiveMaxPayloadBytes() int {
	limit := h.maxPayloadBytes
	if limit <= 0 {
		limit = DefaultMaxPayloadBytes
	}
	if h != nil && h.nc != nil {
		if mp := int(h.nc.MaxPayload()); mp > 0 && mp < limit {
			limit = mp
		}
	}
	return limit
}

// BufferedBytes retorna o total de bytes em buffer na conexao NATS (para backpressure).
func (h *NatsStreamHandler) BufferedBytes() (int, error) {
	if h == nil || h.nc == nil {
		return 0, fmt.Errorf("nats connection unavailable")
	}
	return h.nc.Buffered()
}

// FlushTimeout aguarda a drenagem do buffer NATS ate o timeout informado.
func (h *NatsStreamHandler) FlushTimeout(timeout time.Duration) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return h.nc.FlushTimeout(timeout)
}

// subjectBase returns the literal base subject for this handler's scope (for publish).
// Format: tenant.{tenantID}.site.{siteID}.agent.{agentID}.remote.session
func (h *NatsStreamHandler) subjectBase() string {
	return fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session", h.tenantID, h.siteID, h.agentID)
}

// subscribePattern returns the literal subject for subscribing to a session's
// interactive subjects (input, term.in, files.req, proxy.req, signal, control).
// Format: tenant.{tenantID}.site.{siteID}.agent.{agentID}.remote.session.{sessionID}.{suffix}
//
// IMPORTANTE (segurança): o subject é LITERAL e amarrado à identidade do próprio
// agente (tenantID/siteID/agentID injetados via SetNatsConn). NÃO usar wildcards
// (tenant.*.site.*.agent.*) aqui: além de não serem cobertos pela ACL literal do
// backend (Permissions Violation), wildcards permitiriam receber mensagens
// destinadas a outros agentes/sites/tenants. O literal é um subconjunto exato da
// permissão `tenant.{c}.site.{s}.agent.{a}.remote.session.>` emitida pelo backend.
func (h *NatsStreamHandler) subscribePattern(sessionID, suffix string) string {
	if !validSessionIDCharset(sessionID) {
		log.Printf("[nats-stream] sessionId com caracteres invalidos (subscribe) — subject suprimido")
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", h.subjectBase(), stripHyphens(sessionID), suffix)
}

// publishSubject builds the literal publish subject for a given session and suffix.
// Format: tenant.{tenantID}.site.{siteID}.agent.{agentID}.remote.session.{sessionID}.{suffix}
// All UUIDs are normalized (hyphens stripped) to match server subject format.
func (h *NatsStreamHandler) publishSubject(sessionID, suffix string) string {
	if !validSessionIDCharset(sessionID) {
		log.Printf("[nats-stream] sessionId com caracteres invalidos (publish) — subject suprimido")
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", h.subjectBase(), stripHyphens(sessionID), suffix)
}

// SubscribeToControl subscreve ao subject de controle da sessao. O handler
// recebe os BYTES crus: o canal unico carrega o envelope tipado de liveness
// (ping/pong/keyframe/closed) e o decodificador do dominio decide o que fazer.
func (h *NatsStreamHandler) SubscribeToControl(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	subject := h.subscribePattern(sessionID, "control")
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// PublishControl publica um frame de controle ja codificado no subject da
// sessao (.control). Usado pela liveness (ping/pong) e pelo encerramento.
func (h *NatsStreamHandler) PublishControl(sessionID string, payload []byte) error {
	return h.nc.Publish(h.publishSubject(sessionID, "control"), payload)
}

// PublishFrame envia um frame de tela para o viewer.
// Se o frame exceder maxPayloadBytes, é fragmentado (JUMBO) em múltiplos
// publishes .frame.frag com reassembly no viewer.
func (h *NatsStreamHandler) PublishFrame(sessionID string, frameData []byte) error {
	if len(frameData) <= h.maxPayloadBytes {
		subject := h.publishSubject(sessionID, "frame")
		return h.nc.Publish(subject, frameData)
	}

	// ── Fragmentação JUMBO ──
	// Formato de cada fragmento:
	//   [4B totalLen][4B offset][2B fragIndex][2B fragCount][payload]
	// O offset explícito torna o reassembly determinístico (o tamanho de
	// fragmento no agent pode não dividir o total uniformemente).
	const fragHeaderLen = 12
	maxFrag := h.maxPayloadBytes - 32 // reserva margem p/ header NATS
	if maxFrag <= fragHeaderLen {
		maxFrag = fragHeaderLen + 64
	}

	total := len(frameData)
	count := (total + maxFrag - 1) / maxFrag
	if count > 65535 {
		count = 65535 // limita nº de fragmentos
	}

	base := h.publishSubject(sessionID, "frame.frag")
	for i := 0; i < count; i++ {
		start := i * maxFrag
		end := start + maxFrag
		if end > total {
			end = total
		}
		part := frameData[start:end]

		buf := make([]byte, fragHeaderLen+len(part))
		binary.BigEndian.PutUint32(buf[0:4], uint32(total))
		binary.BigEndian.PutUint32(buf[4:8], uint32(start))
		binary.BigEndian.PutUint16(buf[8:10], uint16(i))
		binary.BigEndian.PutUint16(buf[10:12], uint16(count))
		copy(buf[fragHeaderLen:], part)

		if err := h.nc.Publish(base, buf); err != nil {
			return err
		}
	}
	return nil
}

// PublishTermOut envia saida do terminal para o viewer.
func (h *NatsStreamHandler) PublishTermOut(sessionID string, data string) error {
	subject := h.publishSubject(sessionID, "term.out")
	err := h.nc.Publish(subject, []byte(data))
	if err != nil {
		log.Printf("[remote-session-nats] PublishTermOut erro: subject=%s err=%v\n", subject, err)
	}
	return err
}

// PublishTermReady notifica o viewer sobre shells disponiveis e console pronto.
// Erros de marshal/publish sao logados e devolvidos: o SessionTerminal os
// contabiliza em publishErrors (telemetria .stats).
func (h *NatsStreamHandler) PublishTermReady(sessionID string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[remote-session-nats] PublishTermReady marshal erro: %v\n", err)
		return err
	}
	subject := h.publishSubject(sessionID, "term.ready")
	if err := h.nc.Publish(subject, payload); err != nil {
		log.Printf("[remote-session-nats] PublishTermReady erro: subject=%s err=%v\n", subject, err)
		return err
	}
	return nil
}

// PublishTermStats publica a telemetria periodica da sessao de terminal
// (subject .stats, agent->viewer — contrato realtime v3, aditivo). Erros sao
// logados e devolvidos para o SessionTerminal contabilizar em publishErrors.
func (h *NatsStreamHandler) PublishTermStats(sessionID string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[remote-session-nats] PublishTermStats marshal erro: %v\n", err)
		return err
	}
	subject := h.publishSubject(sessionID, "stats")
	if err := h.nc.Publish(subject, payload); err != nil {
		log.Printf("[remote-session-nats] PublishTermStats erro: subject=%s err=%v\n", subject, err)
		return err
	}
	return nil
}

// PublishTermCwd publica o diretório corrente REPORTADO pelo shell da sessão
// (subject .term.cwd, agent->viewer — contrato aditivo). Payload:
// {"cwd":..,"shellKind":..,"atMs":..}. Erros são devolvidos para o
// SessionTerminal contabilizar em publishErrors.
//
// A linha do subject precisa ser adicionada à ACL de publicação do viewer em
// DiscoveryRMM_API/docs/NATS_SUBJECTS_ACL.md (fora do escopo desta mudança).
func (h *NatsStreamHandler) PublishTermCwd(sessionID string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[remote-session-nats] PublishTermCwd marshal erro: %v\n", err)
		return err
	}
	subject := h.publishSubject(sessionID, "term.cwd")
	if err := h.nc.Publish(subject, payload); err != nil {
		log.Printf("[remote-session-nats] PublishTermCwd erro: subject=%s err=%v\n", subject, err)
		return err
	}
	return nil
}

// PublishRecordingTerm envia frames de terminal para gravacao.
func (h *NatsStreamHandler) PublishRecordingTerm(sessionID string, data []byte) error {
	subject := h.publishSubject(sessionID, "recording.term")
	return h.nc.Publish(subject, data)
}

// PublishCursor envia a posição/estado do cursor separadamente do frame.
// Formato: 6 bytes (flags | x int16 | y int16) — pequeno e frequente.
func (h *NatsStreamHandler) PublishCursor(sessionID string, cursorData []byte) error {
	subject := h.publishSubject(sessionID, "cursor")
	return h.nc.Publish(subject, cursorData)
}

// PublishCursorImage envia o bitmap do cursor (PNG) quando a forma muda.
// Formato binário: [4B width][4B height][4B hotX][4B hotY][PNG bytes].
func (h *NatsStreamHandler) PublishCursorImage(sessionID string, img *screen.CursorImage) error {
	if img == nil || len(img.PNG) == 0 {
		return nil
	}
	buf := make([]byte, 16+len(img.PNG))
	binary.BigEndian.PutUint32(buf[0:4], uint32(img.Width))
	binary.BigEndian.PutUint32(buf[4:8], uint32(img.Height))
	binary.BigEndian.PutUint32(buf[8:12], uint32(img.HotX))
	binary.BigEndian.PutUint32(buf[12:16], uint32(img.HotY))
	copy(buf[16:], img.PNG)
	subject := h.publishSubject(sessionID, "cursor.img")
	return h.nc.Publish(subject, buf)
}

// PublishMonitors envia a lista de monitores do agent para o viewer.
// Formato JSON: [{index, x, y, width, height, name, isPrimary}].
func (h *NatsStreamHandler) PublishMonitors(sessionID string, monitors []screen.Monitor) error {
	payload, _ := json.Marshal(monitors)
	subject := h.publishSubject(sessionID, "monitors")
	return h.nc.Publish(subject, payload)
}

// PublishEvent envia um evento de sessao.
func (h *NatsStreamHandler) PublishEvent(sessionID string, eventType string, data any) error {
	payload, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"eventType": eventType,
		"data":      data,
	})
	subject := h.publishSubject(sessionID, "event")
	log.Printf("[remote-session-nats] PublishEvent: subject=%s eventType=%s\n", subject, eventType)
	return h.nc.Publish(subject, payload)
}

// PublishTransportInfo publica o estado do transporte da sessão (WebRTC vs
// NATS vs fallback) no canal .control.
//
// Por que num canal próprio em vez de só no evento "started": o console precisa
// saber de uma MUDANÇA de transporte em tempo real — principalmente quando o ICE
// falha e o agente cai para NATS no meio da sessão. Sem este aviso, o operador
// continuaria vendo "WebRTC" no selo enquanto a sessão já trafega por NATS.
//
// ADITIVO: viewers antigos ignoram a mensagem (o switch de actions tem default),
// então nenhum cliente precisa subir em sincronia com o agente.
func (h *NatsStreamHandler) PublishTransportInfo(sessionID string, info TransportInfo) error {
	payload, err := json.Marshal(map[string]any{
		"action":    "transport_info",
		"sessionId": sessionID,
		"transport": info,
	})
	if err != nil {
		return err
	}

	subject := h.publishSubject(sessionID, "control")
	log.Printf("[remote-session-nats] PublishTransportInfo: subject=%s state=%s active=%s reason=%q\n",
		subject, info.State, info.Active, info.Reason)
	return h.nc.Publish(subject, payload)
}

// ── Sinalização WebRTC (oferta/resposta/candidatos) ─────────────────────
//
// Subjects DEDICADOS (.webrtc.in / .webrtc.out), separados de .control.
//
// Por que não usar o .control: o envelope de controle tem teto de 512 bytes
// (sessioncontrol.MaxEnvelopeBytes) e uma oferta SDP típica tem 1-4 KB. Enfiar
// o SDP ali exigiria fragmentar um documento JSON no meio, o que multiplicaria
// os modos de falha (fragmento perdido, fora de ordem, renegociação no meio).
// O .control continua carregando só o que é pequeno: ping/pong, keyframe,
// inputLock e o estado do transporte.
//
// O canal é bidirecional pelo mesmo par de subjects: o viewer publica em
// .webrtc.in e o agente responde em .webrtc.out, como já se faz em .tunnel.*.
// O frame carrega JSON puro ({kind:"offer"|"answer"|"candidate"}) — o mesmo
// contrato do wire que o navegador usa, sem envelope extra.

// SubscribeToWebRtcIn subscreve o canal de entrada da sinalização WebRTC
// (viewer → agente). O handler recebe o frame JSON cru.
func (h *NatsStreamHandler) SubscribeToWebRtcIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "webrtc.in")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToWebRtcIn erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToWebRtcIn ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishWebRtcOut publica um frame de sinalização do agente para o viewer
// (resposta SDP, candidatos ICE do agente, erro de negociação).
func (h *NatsStreamHandler) PublishWebRtcOut(sessionID string, data []byte) error {
	subject := h.publishSubject(sessionID, "webrtc.out")
	return h.nc.Publish(subject, data)
}

// SubscribeToInput subscreve a eventos de input (mouse/teclado) do viewer.
func (h *NatsStreamHandler) SubscribeToInput(sessionID string, handler func(inputData []byte)) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "input")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToInput erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToInput ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishClipboard envia o conteúdo do clipboard da máquina remota para o
// viewer (subject .clipboard — agent→viewer).
func (h *NatsStreamHandler) PublishClipboard(sessionID string, text string) error {
	payload, _ := json.Marshal(map[string]any{"text": text})
	subject := h.publishSubject(sessionID, "clipboard")
	return h.nc.Publish(subject, payload)
}

// SubscribeToClipboardReq subscreve a pedidos de set/clipboard do viewer
// (subject .clipboard.req — viewer→agent). O handler recebe o texto e a flag
// explícita `paste`: somente com paste=true o agent injeta Ctrl+V no app
// focado — set sem paste apenas atualiza o clipboard remoto (evita Ctrl+V
// incondicional digitado no app errado).
func (h *NatsStreamHandler) SubscribeToClipboardReq(sessionID string, handler func(text string, paste bool)) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "clipboard.req")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		var req struct {
			Text  string `json:"text"`
			Paste bool   `json:"paste"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("[remote-session-nats] clipboard.req inválido: %v", err)
			return
		}
		handler(req.Text, req.Paste)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToClipboardReq erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToClipboardReq ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// SubscribeToTermIn subscreve a stdin do terminal enviado pelo viewer.
func (h *NatsStreamHandler) SubscribeToTermIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "term.in")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToTermIn erro: pattern=%s err=%v\n", pattern, err)
	} else {
		_ = h.nc.Flush()
		log.Printf("[remote-session-nats] SubscribeToTermIn ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// SubscribeToCompletionReq subscreve a requisicao de completacao (TAB) do
// viewer (subject .completion.req, viewer->agent). Assunto LITERAL, como os
// demais: nada de wildcards (ACL do backend + isolamento por sessao).
func (h *NatsStreamHandler) SubscribeToCompletionReq(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "completion.req")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToCompletionReq erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToCompletionReq ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishCompletionRes publica a resposta da completacao (subject
// .completion.res, agent->viewer).
func (h *NatsStreamHandler) PublishCompletionRes(sessionID string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[remote-session-nats] PublishCompletionRes marshal erro: %v\n", err)
		return err
	}
	subject := h.publishSubject(sessionID, "completion.res")
	if err := h.nc.Publish(subject, payload); err != nil {
		log.Printf("[remote-session-nats] PublishCompletionRes erro: subject=%s err=%v\n", subject, err)
		return err
	}
	return nil
}

// SubscribeToFilesReq subscreve a requisicoes de arquivos (list/get/put/delete).
func (h *NatsStreamHandler) SubscribeToFilesReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "files.req")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(h.publishSubject(sessionID, "files.resp"), resp)
		}
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToFilesReq erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToFilesReq ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishFilesReady notifica o viewer que a sessão de arquivos está pronta
// (subscribe em files.req ativo). O viewer aguarda este evento antes de
// enviar o primeiro list — evita a race em que o list chega antes do
// subscribe e é perdido (NATS core não tem replay).
func (h *NatsStreamHandler) PublishFilesReady(sessionID string, data any) error {
	payload, _ := json.Marshal(data)
	subject := h.publishSubject(sessionID, "files.ready")
	log.Printf("[remote-session-nats] PublishFilesReady: subject=%s\n", subject)
	return h.nc.Publish(subject, payload)
}

// PublishFilesProgress publica progresso de uma operação longa (copy/move/zip/unzip)
// para o viewer. O viewer correlaciona pelo requestId.
func (h *NatsStreamHandler) PublishFilesProgress(sessionID string, data any) error {
	payload, _ := json.Marshal(data)
	subject := h.publishSubject(sessionID, "files.progress")
	return h.nc.Publish(subject, payload)
}

// PublishFilesData publica um frame binário 'DF' no canal dedicado .files.data
// (agent -> viewer).
func (h *NatsStreamHandler) PublishFilesData(sessionID string, data []byte) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	subject := h.publishSubject(sessionID, "files.data")
	if subject == "" {
		return fmt.Errorf("sessionId invalido")
	}
	return h.nc.Publish(subject, data)
}

// SubscribeToProcReq subscreve a requisicoes de processos/servicos
// (listProcesses/killProcess/listServices/startService/stopService/restartService).
// O handler retorna o payload de resposta, publicado em .proc.resp.
func (h *NatsStreamHandler) SubscribeToProcReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	pattern := h.subscribePattern(sessionID, "proc.req")
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(h.publishSubject(sessionID, "proc.resp"), resp)
		}
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToProcReq erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToProcReq ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishProcReady notifica o viewer que a sessão de processos/serviços está
// pronta (subscribe em proc.req ativo). Mesmo padrão anti-race de files.ready.
func (h *NatsStreamHandler) PublishProcReady(sessionID string, data any) error {
	payload, _ := json.Marshal(data)
	subject := h.publishSubject(sessionID, "proc.ready")
	log.Printf("[remote-session-nats] PublishProcReady: subject=%s\n", subject)
	return h.nc.Publish(subject, payload)
}

// SubscribeToRegistryReq subscreve as requisicoes de navegacao/edicao do
// Registro do Windows.
//
// O handler devolve o payload de resposta, publicado em .registry.resp. Mesmo
// contrato de SubscribeToProcReq: o handler roda na goroutine do callback do
// NATS, entao operacoes de registro (que podem bloquear em I/O) sao aceitaveis
// aqui pelo mesmo motivo que em processos — sao operacoes curtas e limitadas,
// e o guard recusa antes de qualquer syscall pesado.
func (h *NatsStreamHandler) SubscribeToRegistryReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	if h == nil || h.nc == nil {
		return nil, fmt.Errorf("nats connection unavailable")
	}
	pattern := h.subscribePattern(sessionID, "registry.req")
	respSubject := h.publishSubject(sessionID, "registry.resp")
	if pattern == "" || respSubject == "" {
		return nil, fmt.Errorf("sessionId invalido")
	}
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(respSubject, resp)
		}
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToRegistryReq erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToRegistryReq ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishRegistryReady notifica o viewer que a sessao de registro esta pronta
// (subscribe em registry.req ativo). Mesmo padrao anti-race de proc.ready.
func (h *NatsStreamHandler) PublishRegistryReady(sessionID string, data any) error {
	payload, _ := json.Marshal(data)
	subject := h.publishSubject(sessionID, "registry.ready")
	log.Printf("[remote-session-nats] PublishRegistryReady: subject=%s\n", subject)
	return h.nc.Publish(subject, payload)
}

// SubscribeToProxyReq subscreve a requisicoes de proxy HTTP.
func (h *NatsStreamHandler) SubscribeToProxyReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	if h == nil || h.nc == nil {
		return nil, fmt.Errorf("nats connection unavailable")
	}
	pattern := h.subscribePattern(sessionID, "proxy.req")
	respSubject := h.publishSubject(sessionID, "proxy.resp")
	if pattern == "" || respSubject == "" {
		return nil, fmt.Errorf("sessionId invalido")
	}
	return h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(respSubject, resp)
		}
	})
}

// SubscribeToProxyReqStream subscreve a requisicoes de proxy HTTP/WebSocket com
// concorrencia limitada e suporte a multiplas respostas (streaming/WS) em .proxy.resp.
func (h *NatsStreamHandler) SubscribeToProxyReqStream(sessionID string, handler func(reqData []byte, emit func([]byte) error)) (*nats.Subscription, error) {
	if h == nil || h.nc == nil {
		return nil, fmt.Errorf("nats connection unavailable")
	}
	pattern := h.subscribePattern(sessionID, "proxy.req")
	respSubject := h.publishSubject(sessionID, "proxy.resp")
	if pattern == "" || respSubject == "" {
		return nil, fmt.Errorf("sessionId invalido")
	}

	const maxConcurrentProxyReqs = 16
	sem := make(chan struct{}, maxConcurrentProxyReqs)

	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		dataCopy := make([]byte, len(msg.Data))
		copy(dataCopy, msg.Data)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			handler(dataCopy, func(frame []byte) error {
				if frame == nil {
					return nil
				}
				return h.PublishProxyResp(sessionID, frame)
			})
		}()
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToProxyReqStream erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToProxyReqStream ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishProxyReady notifica o viewer que a sessao de proxy/rdp esta pronta
// (subscricoes em proxy.req e/ou tunnel.in ativas).
func (h *NatsStreamHandler) PublishProxyReady(sessionID string, data any) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	subject := h.publishSubject(sessionID, "proxy.ready")
	if subject == "" {
		return fmt.Errorf("sessionId invalido")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	log.Printf("[remote-session-nats] PublishProxyReady: subject=%s\n", subject)
	return h.nc.Publish(subject, payload)
}

// PublishProxyResp publica um frame de resposta de proxy HTTP/WS em .proxy.resp.
func (h *NatsStreamHandler) PublishProxyResp(sessionID string, data []byte) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	subject := h.publishSubject(sessionID, "proxy.resp")
	if subject == "" {
		return fmt.Errorf("sessionId invalido")
	}
	return h.nc.Publish(subject, data)
}

// SubscribeToTunnelIn subscreve ao canal de entrada do tunel TCP/RDP (.tunnel.in).
func (h *NatsStreamHandler) SubscribeToTunnelIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	if h == nil || h.nc == nil {
		return nil, fmt.Errorf("nats connection unavailable")
	}
	pattern := h.subscribePattern(sessionID, "tunnel.in")
	if pattern == "" {
		return nil, fmt.Errorf("sessionId invalido")
	}
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToTunnelIn erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToTunnelIn ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishTunnelOut publica dados binarios (0x01) ou controle JSON (0x7B) no canal .tunnel.out.
func (h *NatsStreamHandler) PublishTunnelOut(sessionID string, data []byte) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	subject := h.publishSubject(sessionID, "tunnel.out")
	if subject == "" {
		return fmt.Errorf("sessionId invalido")
	}
	return h.nc.Publish(subject, data)
}

// ── RDCleanPath (WebRDP no browser) ─────────────────────────────────────────
//
// O WebRDP usa subjects DEDICADOS, separados de .tunnel.in/.tunnel.out.
// Motivo (decisao 3b): o canal do tunel carrega frames multiplexados do proxy LAN
// (header binario 0x01) e JSON de controle (0x7B), enquanto o RDCleanPath carrega
// DER (tag 0x30) seguido de bytes RDP crus. No site, RemoteProxy e RemoteRdp
// derivam o subject do MESMO natsSubject: compartilhar o canal faria cada parser
// descartar/corromper os bytes do outro, silenciosamente.
//
// O destino continua fail-closed em 127.0.0.1:<RDPPort> (ver rdcleanpath.Server);
// o campo destination vindo do cliente e ignorado.

// SubscribeToRdpCleanpathIn assina o subject dedicado de entrada do RDCleanPath.
// O handler recebe BYTES CRUS (blocos), pois o request DER pode chegar
// fragmentado em varias mensagens.
func (h *NatsStreamHandler) SubscribeToRdpCleanpathIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	if h == nil || h.nc == nil {
		return nil, fmt.Errorf("nats connection unavailable")
	}
	pattern := h.subscribePattern(sessionID, rdcleanpath.SuffixSubjectIn)
	if pattern == "" {
		return nil, fmt.Errorf("sessionId invalido")
	}
	sub, err := h.nc.Subscribe(pattern, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		log.Printf("[remote-session-nats] SubscribeToRdpCleanpathIn erro: pattern=%s err=%v\n", pattern, err)
	} else {
		log.Printf("[remote-session-nats] SubscribeToRdpCleanpathIn ok: pattern=%s\n", pattern)
	}
	return sub, err
}

// PublishRdpCleanpathOut publica bytes no subject dedicado de saida do
// RDCleanPath (response DER do handshake e, depois, bytes RDP crus do relay).
func (h *NatsStreamHandler) PublishRdpCleanpathOut(sessionID string, data []byte) error {
	if h == nil || h.nc == nil {
		return fmt.Errorf("nats connection unavailable")
	}
	subject := h.publishSubject(sessionID, rdcleanpath.SuffixSubjectOut)
	if subject == "" {
		return fmt.Errorf("sessionId invalido")
	}
	return h.nc.Publish(subject, data)
}

// Close encerra o handler (no-op por enquanto, a conexao NATS e gerenciada externamente).
func (h *NatsStreamHandler) Close() error {
	return nil
}
