package netproxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type wsSession struct {
	id         string
	dialing    bool
	dialCancel context.CancelFunc
	aborted    atomic.Bool
	conn       *websocket.Conn
	writeMu    sync.Mutex
	cancel     context.CancelFunc
	closed     atomic.Bool
}

func (p *Proxy) handleWSOpen(ctx context.Context, req ProxyRequest, emit func([]byte) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.closed.Load() {
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: req.RequestID,
			Success:   false,
			Error:     "proxy encerrado",
		})
	}
	reqID := strings.TrimSpace(req.RequestID)
	if reqID == "" {
		return p.emitResponse(emit, ProxyResponse{
			Type:    "ws_error",
			Success: false,
			Error:   "requestId obrigatorio para ws_open",
		})
	}

	u, _, _, err := parseAndValidateURL(req.URL, "ws", "wss")
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "URL WebSocket invalida: " + err.Error(),
		})
	}

	dialTimeout := p.resolveRequestTimeout(req.TimeoutMs)
	if dialTimeout > 15*time.Second {
		dialTimeout = 15 * time.Second
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	defer dialCancel()

	slot := &wsSession{
		id:         reqID,
		dialing:    true,
		dialCancel: dialCancel,
	}

	p.wsMu.Lock()
	if p.closed.Load() {
		p.wsMu.Unlock()
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "proxy encerrado",
		})
	}
	if _, exists := p.wsConns[reqID]; exists {
		p.wsMu.Unlock()
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "sessao WebSocket duplicada para requestId",
		})
	}
	if len(p.wsConns) >= p.cfg.MaxConcurrentWS {
		p.wsMu.Unlock()
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     fmt.Sprintf("limite de WebSockets simultaneos atingido (%d)", p.cfg.MaxConcurrentWS),
		})
	}
	// Reserva slot provisoriamente com dialCancel para suportar cancelamento por ws_close/Close.
	p.wsConns[reqID] = slot
	p.wsMu.Unlock()

	releaseProvisional := func() bool {
		p.wsMu.Lock()
		aborted := slot.aborted.Load()
		if cur, ok := p.wsConns[reqID]; ok && cur == slot {
			delete(p.wsConns, reqID)
		}
		p.wsMu.Unlock()
		return aborted
	}

	dialer := websocket.Dialer{
		NetDialContext:   p.allowlist.DialAllowed,
		HandshakeTimeout: dialTimeout,
		Subprotocols:     req.Subprotocols,
	}
	if p.cfg.InsecureTLSSkipVerify {
		dialer.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
	}

	fwdHeaders := filterWSHopByHopHeaders(req.Headers)
	conn, resp, dialErr := dialer.DialContext(dialCtx, u.String(), fwdHeaders)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if dialErr != nil {
		wasAborted := releaseProvisional()
		if wasAborted && !p.closed.Load() {
			return nil
		}
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "dial WebSocket: " + dialErr.Error(),
		})
	}

	conn.SetReadLimit(wsReadLimitBytes)
	wsCtx, wsCancel := context.WithCancel(ctx)

	p.wsMu.Lock()
	cur, stillPresent := p.wsConns[reqID]
	if p.closed.Load() || slot.aborted.Load() || !stillPresent || cur != slot {
		if stillPresent && cur == slot {
			delete(p.wsConns, reqID)
		}
		wasAborted := slot.aborted.Load()
		p.wsMu.Unlock()
		wsCancel()
		_ = conn.Close()
		if wasAborted && !p.closed.Load() {
			return nil
		}
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "proxy encerrado",
		})
	}
	slot.dialing = false
	slot.dialCancel = nil
	slot.conn = conn
	slot.cancel = wsCancel
	p.wg.Add(1)
	p.wsMu.Unlock()

	if err := p.emitResponse(emit, ProxyResponse{
		Type:        "ws_opened",
		RequestID:   reqID,
		Success:     true,
		Subprotocol: conn.Subprotocol(),
	}); err != nil {
		p.removeWSSession(reqID, slot)
		wsCancel()
		_ = conn.Close()
		p.wg.Done()
		return err
	}

	go func() {
		defer p.wg.Done()
		defer wsCancel()
		stopAfter := context.AfterFunc(wsCtx, func() {
			_ = conn.Close()
		})
		defer stopAfter()
		defer func() {
			p.removeWSSession(reqID, slot)
			_ = conn.Close()
		}()

		for {
			select {
			case <-wsCtx.Done():
				return
			default:
			}

			msgType, payload, rerr := conn.ReadMessage()
			if rerr != nil {
				closeCode := websocket.CloseNormalClosure
				closeReason := ""
				var ce *websocket.CloseError
				if errors.As(rerr, &ce) {
					closeCode = ce.Code
					closeReason = ce.Text
				} else if !slot.closed.Load() && !p.closed.Load() {
					closeCode = websocket.CloseAbnormalClosure
					closeReason = rerr.Error()
				}
				_ = p.emitResponse(emit, ProxyResponse{
					Type:        "ws_closed",
					RequestID:   reqID,
					Success:     true,
					CloseCode:   closeCode,
					CloseReason: closeReason,
				})
				return
			}

			msgKind := "text"
			if msgType == websocket.BinaryMessage {
				msgKind = "binary"
			}
			_ = p.emitResponse(emit, ProxyResponse{
				Type:        "ws_data",
				RequestID:   reqID,
				Success:     true,
				MessageType: msgKind,
				DataBase64:  base64.StdEncoding.EncodeToString(payload),
			})
		}
	}()

	return nil
}

func (p *Proxy) handleWSData(req ProxyRequest, emit func([]byte) error) error {
	reqID := strings.TrimSpace(req.RequestID)
	sess := p.getWSSession(reqID)
	if sess == nil {
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "sessao WebSocket nao encontrada",
		})
	}

	payload, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "dataBase64 invalido: " + err.Error(),
		})
	}

	msgType := websocket.TextMessage
	if strings.EqualFold(strings.TrimSpace(req.MessageType), "binary") {
		msgType = websocket.BinaryMessage
	}

	sess.writeMu.Lock()
	_ = sess.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	werr := sess.conn.WriteMessage(msgType, payload)
	sess.writeMu.Unlock()
	if werr != nil {
		return p.emitResponse(emit, ProxyResponse{
			Type:      "ws_error",
			RequestID: reqID,
			Success:   false,
			Error:     "erro ao enviar frame WebSocket: " + werr.Error(),
		})
	}
	return nil
}

func (p *Proxy) handleWSClose(req ProxyRequest, emit func([]byte) error) error {
	reqID := strings.TrimSpace(req.RequestID)
	p.wsMu.Lock()
	sess, ok := p.wsConns[reqID]
	if ok {
		delete(p.wsConns, reqID)
	}
	if ok && sess != nil && sess.dialing {
		sess.aborted.Store(true)
		dc := sess.dialCancel
		p.wsMu.Unlock()
		if dc != nil {
			dc()
		}
		code := req.CloseCode
		if code == 0 {
			code = websocket.CloseNormalClosure
		}
		return p.emitResponse(emit, ProxyResponse{
			Type:        "ws_closed",
			RequestID:   reqID,
			Success:     true,
			CloseCode:   code,
			CloseReason: req.CloseReason,
		})
	}
	p.wsMu.Unlock()

	if !ok || sess == nil {
		return p.emitResponse(emit, ProxyResponse{
			Type:        "ws_closed",
			RequestID:   reqID,
			Success:     true,
			CloseCode:   websocket.CloseNormalClosure,
			CloseReason: req.CloseReason,
		})
	}
	sess.closed.Store(true)
	code := req.CloseCode
	if code == 0 {
		code = websocket.CloseNormalClosure
	}
	sess.writeMu.Lock()
	_ = sess.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, req.CloseReason),
		time.Now().Add(2*time.Second),
	)
	sess.writeMu.Unlock()
	if sess.cancel != nil {
		sess.cancel()
	}
	_ = sess.conn.Close()
	return nil
}

// Close encerra todas as sessoes WebSocket ativas e aguarda suas goroutines.
func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	p.closed.Store(true)
	p.wsMu.Lock()
	sessions := make([]*wsSession, 0, len(p.wsConns))
	var dialCancels []context.CancelFunc
	for id, sess := range p.wsConns {
		if sess != nil {
			if sess.dialing {
				sess.aborted.Store(true)
				if sess.dialCancel != nil {
					dialCancels = append(dialCancels, sess.dialCancel)
				}
			} else {
				sessions = append(sessions, sess)
			}
		}
		delete(p.wsConns, id)
	}
	p.wsMu.Unlock()

	for _, dc := range dialCancels {
		dc()
	}
	for _, sess := range sessions {
		sess.closed.Store(true)
		if sess.cancel != nil {
			sess.cancel()
		}
		if sess.conn != nil {
			_ = sess.conn.Close()
		}
	}
	p.wg.Wait()
	return nil
}

func (p *Proxy) getWSSession(id string) *wsSession {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	sess := p.wsConns[id]
	if sess == nil || sess.dialing {
		return nil
	}
	return sess
}

func (p *Proxy) removeWSSession(id string, expected *wsSession) *wsSession {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	cur, ok := p.wsConns[id]
	if !ok {
		return nil
	}
	if expected != nil && cur != expected {
		return nil
	}
	delete(p.wsConns, id)
	return cur
}

func filterWSHopByHopHeaders(raw map[string]string) http.Header {
	if len(raw) == 0 {
		return nil
	}
	out := make(http.Header)
	for k, v := range raw {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "sec-websocket-") ||
			lower == "connection" ||
			lower == "upgrade" ||
			lower == "host" {
			continue
		}
		out.Set(key, v)
	}
	return out
}
