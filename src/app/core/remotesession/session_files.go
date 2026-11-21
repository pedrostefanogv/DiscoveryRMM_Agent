//go:build windows

package remotesession

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"discovery/app/core/fileserver"
)

// SessionFiles gerencia uma sessão de transferência de arquivos remota.
type SessionFiles struct {
	sessionID  string
	natsStream *NatsStreamHandler
	server     *fileserver.Server
	webrtcSend func([]byte) bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// SetWebRtcSend registra a função de envio pelo data channel WebRTC ("files").
// Quando retorna false (canal fechado ou acima do teto SCTP), o fluxo cai para NATS.
func (sf *SessionFiles) SetWebRtcSend(fn func([]byte) bool) {
	if sf == nil {
		return
	}
	sf.webrtcSend = fn
}

// HandleWebRtcRequest processa uma requisição recebida pelo data channel "files"
// e devolve a resposta pelo mesmo canal, caindo para NATS se o envio falhar.
func (sf *SessionFiles) HandleWebRtcRequest(reqData []byte) {
	if sf == nil {
		return
	}
	resp := sf.handleRequest(reqData)
	if resp == nil {
		return
	}
	if sf.webrtcSend != nil && sf.webrtcSend(resp) {
		return
	}
	if sf.natsStream != nil {
		_ = sf.natsStream.PublishFilesResp(sf.sessionID, resp)
	}
}

// NewSessionFiles cria um gerenciador de sessão de arquivos.
func NewSessionFiles(sessionID string, natsStream *NatsStreamHandler, rootPath string) *SessionFiles {
	if rootPath == "" {
		rootPath = "C:\\"
	}
	srv := fileserver.NewServer(rootPath)
	// M29: auditoria das operações mutantes de arquivo — log local + evento ao
	// servidor via subject de eventos da sessão (quando habilitado via
	// DISCOVERY_FILES_AUDIT, default on).
	srv.SetAuditHook(func(action, path, newPath string, ok bool, errMsg string) {
		log.Printf("[session-files][audit] action=%s path=%q newPath=%q ok=%t err=%q",
			action, path, newPath, ok, errMsg)
		if natsStream != nil {
			natsStream.PublishEvent(sessionID, "files_audit", map[string]any{
				"action":   action,
				"path":     path,
				"newPath":  newPath,
				"ok":       ok,
				"error":    errMsg,
				"auditUtc": time.Now().UTC().Format(time.RFC3339),
			})
		}
	})
	return &SessionFiles{
		sessionID:  sessionID,
		natsStream: natsStream,
		server:     srv,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start subscreve .files.req e processa requisições.
func (sf *SessionFiles) Start(ctx context.Context) error {
	defer close(sf.doneCh)
	defer func() { _ = sf.server.Close() }()

	sub, err := sf.natsStream.SubscribeToFilesReq(sf.sessionID, func(reqData []byte) []byte {
		return sf.handleRequest(reqData)
	})
	if err != nil {
		return fmt.Errorf("subscribe files.req: %w", err)
	}
	defer sub.Unsubscribe()

	log.Printf("[session-files] sessão iniciada para %s", sf.sessionID)

	// Notifica o viewer que a sessão está pronta (subscribe ativo). O viewer
	// aguarda files.ready antes de enviar o primeiro list — evita a race em
	// que o list chega antes do subscribe e é perdido (NATS core sem replay).
	// "drives" permite ao viewer oferecer a troca de disco (C:, D:, ...).
	// Fica null/ausente quando a sessão tem rootPath em subpasta (sandbox
	// restrito, sem troca de volume).
	_ = sf.natsStream.PublishFilesReady(sf.sessionID, map[string]any{
		"rootPath":       sf.server.RootPath(),
		"status":         "ready",
		"drives":         sf.server.AllowedDrives(),
		"supportsBinary": true,
		"binaryFrames":   true,
		"dataSubject":    true,
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sf.stopCh:
		return nil
	}
}

// Stop encerra a sessão de arquivos.
func (sf *SessionFiles) Stop() {
	select {
	case <-sf.stopCh:
	default:
		close(sf.stopCh)
	}
	<-sf.doneCh
	_ = sf.server.Close()
}

// maxBinaryFileHeaderBytes espelha o teto do codec do viewer (fileFrameCodec.ts).
const maxBinaryFileHeaderBytes = 64 * 1024

// decodeBinaryFileRequest reconhece o frame binário 'DF' do contrato de
// transferência de arquivos (fileFrameCodec.ts no viewer / encodeBinaryFileFrame
// no e2e): [0..1] magic 'D','F' · [2..5] headerLen uint32 BigEndian ·
// [6..6+headerLen) JSON do FileSessionRequest (sem data) · resto = payload cru
// do chunk. Retorna ok=false para payloads JSON legados.
func decodeBinaryFileRequest(reqData []byte) (fileserver.FileSessionRequest, []byte, bool) {
	if len(reqData) < 6 || reqData[0] != 'D' || reqData[1] != 'F' {
		return fileserver.FileSessionRequest{}, nil, false
	}
	headerLen := int(binary.BigEndian.Uint32(reqData[2:6]))
	if headerLen <= 0 || headerLen > maxBinaryFileHeaderBytes || 6+headerLen > len(reqData) {
		return fileserver.FileSessionRequest{}, nil, false
	}
	var req fileserver.FileSessionRequest
	if err := json.Unmarshal(reqData[6:6+headerLen], &req); err != nil {
		return fileserver.FileSessionRequest{}, nil, false
	}
	return req, reqData[6+headerLen:], true
}

// encodeBinaryFileFrame empacota uma FileSessionResponse no formato binário 'DF'
// compatível com fileFrameCodec.ts: [0..1]='D','F' · [2..5]=headerLen BE ·
// [6..6+headerLen)=JSON sem `data` · resto=bytes crus do chunk.
func encodeBinaryFileFrame(header fileserver.FileSessionResponse, data []byte) ([]byte, error) {
	header.Data = nil
	hdrBytes, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	if len(hdrBytes) == 0 || len(hdrBytes) > maxBinaryFileHeaderBytes {
		return nil, fmt.Errorf("header 'DF' fora do limite (%d bytes)", len(hdrBytes))
	}
	frame := make([]byte, 6+len(hdrBytes)+len(data))
	frame[0] = 'D'
	frame[1] = 'F'
	binary.BigEndian.PutUint32(frame[2:6], uint32(len(hdrBytes)))
	copy(frame[6:6+len(hdrBytes)], hdrBytes)
	if len(data) > 0 {
		copy(frame[6+len(hdrBytes):], data)
	}
	return frame, nil
}

func (sf *SessionFiles) handleRequest(reqData []byte) []byte {
	var (
		req  fileserver.FileSessionRequest
		isDF bool
	)

	// Frame binário 'DF' (upload de chunks 256 KB ou download 'get' binário):
	// evita re-serializar o payload em JSON+base64 passando diretamente a
	// FileSessionRequest estruturada para fileserver.Server.HandleSessionRequest.
	if len(reqData) >= 2 && reqData[0] == 'D' && reqData[1] == 'F' {
		binReq, payload, ok := decodeBinaryFileRequest(reqData)
		if !ok {
			return sf.errorResponse("", "frame binário 'DF' inválido")
		}
		binReq.Data = payload
		req = binReq
		isDF = true
	} else {
		if err := json.Unmarshal(reqData, &req); err != nil {
			return sf.errorResponse("", "payload json inválido: "+err.Error())
		}
	}

	if req.Version != 1 {
		return sf.errorResponse(req.RequestID, "versão não suportada")
	}

	// Conecta o callback de progresso do server ao NATS (files.progress).
	// Só para operações longas (copy/move/zip/unzip) — list/get/put são rápidas.
	reqID := req.RequestID
	sf.server.SetProgressCallback(func(loaded, total int64) {
		_ = sf.natsStream.PublishFilesProgress(sf.sessionID, map[string]any{
			"requestId": reqID,
			"loaded":    loaded,
			"total":     total,
		})
	})
	defer sf.server.SetProgressCallback(nil)

	start := time.Now()
	resp := sf.server.HandleSessionRequest(req)
	elapsed := time.Since(start)

	log.Printf("[session-files] req=%s path=%s elapsed=%v", req.Action, req.Path, elapsed)

	if resp.RequestID == "" {
		resp.RequestID = req.RequestID
	}

	// Download binário 'DF': quando o viewer pediu binário (frame 'DF' ou flag
	// `binary: true`), publica os bytes crus sem base64 (+33%) — preferindo o
	// data channel WebRTC quando conectado e caindo para .files.data no NATS.
	if req.Action == "get" && resp.Success && (isDF || req.Binary) {
		if frame, err := encodeBinaryFileFrame(resp, resp.Data); err == nil {
			if sf.webrtcSend != nil && sf.webrtcSend(frame) {
				return nil
			}
			if pubErr := sf.natsStream.PublishFilesData(sf.sessionID, frame); pubErr == nil {
				return nil
			} else {
				log.Printf("[session-files] falha ao publicar .files.data (fallback JSON .files.resp): %v", pubErr)
			}
		}
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return sf.errorResponse(req.RequestID, "serializar resposta: "+err.Error())
	}
	return out
}

func (sf *SessionFiles) errorResponse(requestID, msg string) []byte {
	resp := fileserver.FileSessionResponse{
		Version:   1,
		RequestID: requestID,
		Success:   false,
		Error:     msg,
	}
	data, _ := json.Marshal(resp)
	return data
}
