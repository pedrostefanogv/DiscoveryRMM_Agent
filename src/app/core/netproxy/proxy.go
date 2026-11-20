package netproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxResponseBytes    int64 = 10 * 1024 * 1024 // 10 MiB (alinhado com RemoteAccessOptions.cs)
	DefaultNatsMaxPayloadBytes int   = 2 * 1024 * 1024  // 2 MiB (alinhado com nats_stream.go)
	DefaultRequestTimeout            = 30 * time.Second
	MaxRequestTimeout                = 120 * time.Second
	MinRequestTimeout                = 1 * time.Second
	MaxRedirects                     = 5
	MaxConcurrentWebSockets          = 32
	wsReadLimitBytes           int64 = 1 * 1024 * 1024 // 1 MiB raw -> ~1.33 MiB base64 < 1.5 MiB
	minChunkRawBytes                 = 64 * 1024
	maxChunkRawBytesCap              = 768 * 1024 // 768 KiB raw -> ~1 MiB base64 (< 1.5 MiB JSON)
)

// ProxyConfig define politicas de tamanho, timeout, TLS e concorrencia WebSocket.
type ProxyConfig struct {
	MaxResponseBytes      int64
	NatsMaxPayloadBytes   int
	RequestTimeout        time.Duration
	InsecureTLSSkipVerify bool
	MaxConcurrentWS       int

	// Audit, quando definido, e chamado a cada tentativa de acesso a um destino
	// (permitida ou barrada pela allowlist). Existe porque o trafego do proxy
	// NAO passa pela API (viewer -> NATS -> agent): sem este callback o servidor
	// nao tem como auditar "quem acessou o que, quando e se foi barrado".
	//
	// Roda FORA do caminho critico de erro: uma falha de auditoria nunca pode
	// impedir a navegacao do operador (o chamador so loga).
	Audit func(host string, port int, allowed bool, reason, mode string)
}

// auditNotifica dispara o callback de auditoria de forma tolerante a falhas.
// De proposito NAO propaga erro: auditoria e observabilidade, nao controle.
func (p *Proxy) auditNotifica(host string, port int, allowed bool, reason, mode string) {
	if p == nil || p.cfg.Audit == nil {
		return
	}
	defer func() {
		// Um panic no callback de auditoria jamais pode derrubar a sessao.
		_ = recover()
	}()
	p.cfg.Audit(host, port, allowed, reason, mode)
}

// Proxy gerencia requisicoes HTTP reversas e WebSockets para dispositivos na rede local.
type Proxy struct {
	allowlist *Allowlist
	client    *http.Client
	cfg       ProxyConfig
	maxBytes  int64

	wsMu    sync.Mutex
	wsConns map[string]*wsSession
	wg      sync.WaitGroup
	closed  atomic.Bool
}

// ProxyRequest representa uma requisicao HTTP ou evento WebSocket do viewer.
type ProxyRequest struct {
	Type         string            `json:"type,omitempty"` // ""|"http" (default), "ws_open", "ws_data", "ws_close"
	RequestID    string            `json:"requestId"`
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyBase64   string            `json:"bodyBase64,omitempty"`
	Body         string            `json:"body,omitempty"` // fallback legado quando BodyBase64 == ""
	TimeoutMs    int               `json:"timeoutMs,omitempty"`
	Subprotocols []string          `json:"subprotocols,omitempty"`
	MessageType  string            `json:"messageType,omitempty"` // "text" | "binary"
	DataBase64   string            `json:"dataBase64,omitempty"`
	CloseCode    int               `json:"closeCode,omitempty"`
	CloseReason  string            `json:"closeReason,omitempty"`
}

// ProxyResponse representa a resposta HTTP (unica ou chunked) ou frame WebSocket ao viewer.
type ProxyResponse struct {
	Type        string            `json:"type,omitempty"` // ""|"http", "ws_opened", "ws_data", "ws_closed", "ws_error"
	RequestID   string            `json:"requestId,omitempty"`
	Success     bool              `json:"success"`
	Error       string            `json:"error,omitempty"`
	StatusCode  int               `json:"statusCode,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyBase64  string            `json:"bodyBase64,omitempty"`
	ContentLen  int64             `json:"contentLen,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
	ChunkIndex  int               `json:"chunkIndex,omitempty"`
	TotalChunks int               `json:"totalChunks,omitempty"`
	EOF         bool              `json:"eof,omitempty"`
	Subprotocol string            `json:"subprotocol,omitempty"`
	MessageType string            `json:"messageType,omitempty"`
	DataBase64  string            `json:"dataBase64,omitempty"`
	CloseCode   int               `json:"closeCode,omitempty"`
	CloseReason string            `json:"closeReason,omitempty"`
}

// NewProxy cria um novo proxy de rede com configuracao padrao.
func NewProxy(allowlist *Allowlist, maxBytes int64) *Proxy {
	return NewProxyWithConfig(allowlist, ProxyConfig{
		MaxResponseBytes: maxBytes,
	})
}

// NewProxyWithConfig cria um proxy HTTP/WS usando DialAllowed e politicas configuradas.
func NewProxyWithConfig(allowlist *Allowlist, cfg ProxyConfig) *Proxy {
	if allowlist == nil {
		allowlist = NewAllowlist()
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if cfg.NatsMaxPayloadBytes <= 0 {
		cfg.NatsMaxPayloadBytes = DefaultNatsMaxPayloadBytes
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.MaxConcurrentWS <= 0 {
		cfg.MaxConcurrentWS = MaxConcurrentWebSockets
	}

	transport := &http.Transport{
		DialContext: allowlist.DialAllowed,
	}
	if cfg.InsecureTLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
	}

	p := &Proxy{
		allowlist: allowlist,
		cfg:       cfg,
		maxBytes:  cfg.MaxResponseBytes,
		wsConns:   make(map[string]*wsSession),
	}
	p.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			_, nextHost, nextPort, err := parseAndValidateURL(req.URL.String(), "http", "https")
			if err != nil {
				return fmt.Errorf("redirect invalido: %w", err)
			}
			if _, err := allowlist.ResolveAndValidate(req.Context(), nextHost, nextPort); err != nil {
				return fmt.Errorf("redirect bloqueado pela allowlist: %w", err)
			}
			return nil
		},
	}
	return p
}

// HandleRequest executa uma requisicao de proxy sincrona retornando um unico JSON.
func (p *Proxy) HandleRequest(raw []byte) []byte {
	var frames [][]byte
	err := p.HandleRequestStream(context.Background(), raw, func(b []byte) error {
		cp := make([]byte, len(b))
		copy(cp, b)
		frames = append(frames, cp)
		return nil
	})
	if err != nil && len(frames) == 0 {
		return p.errorResponse("", err.Error())
	}
	if len(frames) == 0 {
		return p.errorResponse("", "nenhuma resposta produzida")
	}
	if len(frames) == 1 {
		return frames[0]
	}

	// Reassemble multi-chunk HTTP response for single-frame callers.
	var first ProxyResponse
	if err := json.Unmarshal(frames[0], &first); err != nil {
		return frames[0]
	}
	var assembled bytes.Buffer
	for _, f := range frames {
		var chunk ProxyResponse
		if uerr := json.Unmarshal(f, &chunk); uerr != nil {
			return p.errorResponse(first.RequestID, "erro ao remontar chunk: "+uerr.Error())
		}
		if !chunk.Success {
			return f
		}
		if chunk.BodyBase64 != "" {
			decoded, derr := base64.StdEncoding.DecodeString(chunk.BodyBase64)
			if derr != nil {
				return p.errorResponse(first.RequestID, "chunk base64 invalido: "+derr.Error())
			}
			assembled.Write(decoded)
		}
	}
	first.Stream = false
	first.ChunkIndex = 0
	first.TotalChunks = 0
	first.EOF = false
	first.BodyBase64 = base64.StdEncoding.EncodeToString(assembled.Bytes())
	first.ContentLen = int64(assembled.Len())
	b, _ := json.Marshal(first)
	return b
}

// HandleRequestStream processa uma requisicao HTTP ou WebSocket e emite um ou mais
// frames JSON correlacionados por requestId.
func (p *Proxy) HandleRequestStream(ctx context.Context, raw []byte, emit func([]byte) error) error {
	if p == nil {
		return fmt.Errorf("proxy nil")
	}
	if emit == nil {
		return fmt.Errorf("emit callback nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var req ProxyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return p.emitResponse(emit, ProxyResponse{
			Success: false,
			Error:   "payload invalido: " + err.Error(),
		})
	}

	reqType := strings.ToLower(strings.TrimSpace(req.Type))
	switch reqType {
	case "ws_open":
		return p.handleWSOpen(ctx, req, emit)
	case "ws_data":
		return p.handleWSData(req, emit)
	case "ws_close":
		return p.handleWSClose(req, emit)
	case "", "http":
		return p.handleHTTPStream(ctx, req, emit)
	default:
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     fmt.Sprintf("tipo de requisicao nao suportado: %q", req.Type),
		})
	}
}

func (p *Proxy) handleHTTPStream(ctx context.Context, req ProxyRequest, emit func([]byte) error) error {
	if strings.TrimSpace(req.URL) == "" {
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     "URL nao informada",
		})
	}

	parsedURL, targetHost, targetPort, err := parseAndValidateURL(req.URL, "http", "https")
	if err != nil {
		// URL malformada: nao ha destino valido para auditar, mas registramos a
		// tentativa para nao perder o rastro de um viewer adulterado.
		p.auditNotifica(req.URL, 0, false, "URL invalida: "+err.Error(), "invalid")
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     "URL invalida: " + err.Error(),
		})
	}

	method, err := validateHTTPMethod(req.Method)
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     err.Error(),
		})
	}

	var bodyReader io.Reader
	if req.BodyBase64 != "" {
		decoded, derr := base64.StdEncoding.DecodeString(req.BodyBase64)
		if derr != nil {
			return p.emitResponse(emit, ProxyResponse{
				RequestID: req.RequestID,
				Success:   false,
				Error:     "bodyBase64 invalido: " + derr.Error(),
			})
		}
		bodyReader = bytes.NewReader(decoded)
	} else if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	timeout := p.resolveRequestTimeout(req.TimeoutMs)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, method, parsedURL.String(), bodyReader)
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     "criar request: " + err.Error(),
		})
	}

	for k, v := range req.Headers {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		httpReq.Header.Set(key, v)
	}
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", "DiscoveryRMM-NetProxy/1.0")
	}

	// Decisao da allowlist ANTES de discar: registra o destino tentado.
	// O erro do DialAllowed e exatamente a mensagem que o operador ve na tela
	// ("allowlist vazia: acesso bloqueado para ...", "IP fora da allowlist",
	// "porta N nao permitida", "SSRF bloqueado"): aqui ele vira auditoria.
	if _, verr := p.allowlist.ResolveAndValidate(reqCtx, targetHost, targetPort); verr != nil {
		p.auditNotifica(targetHost, targetPort, false, verr.Error(), "allowlist")
	} else {
		p.auditNotifica(targetHost, targetPort, true, "", "allowlist")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			RequestID: req.RequestID,
			Success:   false,
			Error:     "executar request: " + err.Error(),
		})
	}
	defer resp.Body.Close()

	if resp.ContentLength > p.maxBytes {
		return p.emitResponse(emit, ProxyResponse{
			RequestID:  req.RequestID,
			Success:    false,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("response exceeds maxResponseBytes (%d)", p.maxBytes),
		})
	}

	headers := flattenHeaders(resp.Header)
	contentType := resp.Header.Get("Content-Type")
	finalURL := parsedURL.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	chunkBudget := p.rawChunkBudget()
	limited := io.LimitReader(resp.Body, p.maxBytes+1)

	firstBuf, hitEOF, err := readChunk(limited, chunkBudget+1)
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			RequestID:  req.RequestID,
			Success:    false,
			StatusCode: resp.StatusCode,
			Error:      "ler resposta: " + err.Error(),
		})
	}

	if int64(len(firstBuf)) > p.maxBytes {
		return p.emitResponse(emit, ProxyResponse{
			RequestID:  req.RequestID,
			Success:    false,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("response exceeds maxResponseBytes (%d)", p.maxBytes),
		})
	}

	if hitEOF && len(firstBuf) <= chunkBudget {
		single := ProxyResponse{
			RequestID:   req.RequestID,
			Success:     true,
			StatusCode:  resp.StatusCode,
			Headers:     headers,
			BodyBase64:  base64.StdEncoding.EncodeToString(firstBuf),
			ContentLen:  int64(len(firstBuf)),
			ContentType: contentType,
			FinalURL:    finalURL,
		}
		b, merr := json.Marshal(single)
		if merr != nil {
			return p.emitResponse(emit, ProxyResponse{
				RequestID: req.RequestID,
				Success:   false,
				Error:     "serializar resposta: " + merr.Error(),
			})
		}
		if len(b) <= p.cfg.NatsMaxPayloadBytes-4096 {
			return emit(b)
		}
	}

	// Streaming chunked path (> chunkBudget or headers pushed single JSON over NATS limit).
	var totalBytes int64
	var pending []byte
	chunkIndex := 0

	emitFirst := func(chunk []byte, isEOF bool) error {
		totalBytes += int64(len(chunk))
		respFrame := ProxyResponse{
			RequestID:   req.RequestID,
			Success:     true,
			StatusCode:  resp.StatusCode,
			Headers:     headers,
			BodyBase64:  base64.StdEncoding.EncodeToString(chunk),
			ContentType: contentType,
			FinalURL:    finalURL,
			Stream:      true,
			ChunkIndex:  chunkIndex,
			EOF:         isEOF,
		}
		if isEOF {
			respFrame.TotalChunks = chunkIndex + 1
			respFrame.ContentLen = totalBytes
		}
		chunkIndex++
		return p.emitResponse(emit, respFrame)
	}

	if len(firstBuf) > chunkBudget {
		if err := emitFirst(firstBuf[:chunkBudget], false); err != nil {
			return err
		}
		pending = append(pending, firstBuf[chunkBudget:]...)
	} else {
		if err := emitFirst(firstBuf, hitEOF); err != nil {
			return err
		}
		if hitEOF {
			return nil
		}
	}

	for {
		nextBuf, eof, rerr := readChunk(limited, chunkBudget-len(pending))
		if rerr != nil {
			return p.emitResponse(emit, ProxyResponse{
				RequestID:  req.RequestID,
				Success:    false,
				StatusCode: resp.StatusCode,
				Stream:     true,
				ChunkIndex: chunkIndex,
				EOF:        true,
				Error:      "ler resposta: " + rerr.Error(),
			})
		}

		combined := nextBuf
		if len(pending) > 0 {
			combined = make([]byte, len(pending)+len(nextBuf))
			copy(combined, pending)
			copy(combined[len(pending):], nextBuf)
			pending = nil
		}

		totalBytes += int64(len(combined))
		if totalBytes > p.maxBytes {
			return p.emitResponse(emit, ProxyResponse{
				RequestID:  req.RequestID,
				Success:    false,
				StatusCode: resp.StatusCode,
				Stream:     true,
				ChunkIndex: chunkIndex,
				EOF:        true,
				Error:      fmt.Sprintf("response exceeds maxResponseBytes (%d)", p.maxBytes),
			})
		}

		frame := ProxyResponse{
			RequestID:  req.RequestID,
			Success:    true,
			Stream:     true,
			ChunkIndex: chunkIndex,
			BodyBase64: base64.StdEncoding.EncodeToString(combined),
			EOF:        eof,
		}
		if eof {
			frame.TotalChunks = chunkIndex + 1
			frame.ContentLen = totalBytes
		}
		if err := p.emitResponse(emit, frame); err != nil {
			return err
		}
		chunkIndex++

		if eof {
			return nil
		}
	}
}

func (p *Proxy) resolveRequestTimeout(timeoutMs int) time.Duration {
	if timeoutMs <= 0 {
		return p.cfg.RequestTimeout
	}
	d := time.Duration(timeoutMs) * time.Millisecond
	if d < MinRequestTimeout {
		return MinRequestTimeout
	}
	if d > MaxRequestTimeout {
		return MaxRequestTimeout
	}
	return d
}

func (p *Proxy) rawChunkBudget() int {
	usable := p.cfg.NatsMaxPayloadBytes - 65536
	if usable <= 0 {
		return minChunkRawBytes
	}
	raw := (usable * 3) / 4
	if raw < minChunkRawBytes {
		return minChunkRawBytes
	}
	if raw > maxChunkRawBytesCap {
		return maxChunkRawBytesCap
	}
	return raw
}

func (p *Proxy) emitResponse(emit func([]byte) error, resp ProxyResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		fallback, _ := json.Marshal(ProxyResponse{
			RequestID: resp.RequestID,
			Success:   false,
			Error:     "marshal erro: " + err.Error(),
		})
		return emit(fallback)
	}
	return emit(b)
}

func (p *Proxy) errorResponse(requestID, msg string) []byte {
	r := ProxyResponse{
		RequestID: requestID,
		Success:   false,
		Error:     msg,
	}
	b, _ := json.Marshal(r)
	return b
}

func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		if strings.EqualFold(k, "Set-Cookie") {
			out[k] = v[0]
		} else {
			out[k] = strings.Join(v, ", ")
		}
	}
	return out
}

func readChunk(r io.Reader, maxLen int) ([]byte, bool, error) {
	if maxLen <= 0 {
		return nil, false, nil
	}
	buf := make([]byte, maxLen)
	total := 0
	for total < maxLen {
		n, err := r.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if err == io.EOF {
			return buf[:total], true, nil
		}
		if err != nil {
			return buf[:total], false, err
		}
	}
	return buf[:total], false, nil
}
