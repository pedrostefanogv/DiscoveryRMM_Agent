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

	stopCh chan struct{}
	doneCh chan struct{}
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
		"rootPath": sf.server.RootPath(),
		"status":   "ready",
		"drives":   sf.server.AllowedDrives(),
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

func (sf *SessionFiles) handleRequest(reqData []byte) []byte {
	// Frame binário 'DF' (upload de chunks 256 KB — evita base64 +33% e JUMBO
	// frames): converte para o FileSessionRequest com Data preenchida e segue
	// o MESMO caminho de validação/sandbox/auditoria do JSON legado.
	if binReq, payload, ok := decodeBinaryFileRequest(reqData); ok {
		binReq.Data = payload
		if b, err := json.Marshal(binReq); err == nil {
			reqData = b
		}
	}

	var req fileserver.FileSessionRequest
	if err := json.Unmarshal(reqData, &req); err != nil {
		return sf.errorResponse("", "payload json inválido: "+err.Error())
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
	result := sf.server.HandleRequest(reqData)
	elapsed := time.Since(start)

	log.Printf("[session-files] req=%s path=%s elapsed=%v", req.Action, req.Path, elapsed)

	// Propaga o requestId na resposta para o viewer correlacionar.
	// O server.HandleRequest retorna FileSessionResponse v1 sem requestId
	// (ele não conhece a requisição) — preenche aqui.
	var resp fileserver.FileSessionResponse
	if err := json.Unmarshal(result, &resp); err == nil && resp.RequestID == "" {
		resp.RequestID = req.RequestID
		if data, err := json.Marshal(resp); err == nil {
			return data
		}
	}

	return result
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
