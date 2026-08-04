//go:build windows

package remotesession

import (
	"context"
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
	return &SessionFiles{
		sessionID:  sessionID,
		natsStream: natsStream,
		server:     fileserver.NewServer(rootPath),
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

func (sf *SessionFiles) handleRequest(reqData []byte) []byte {
	var req fileserver.FileSessionRequest
	if err := json.Unmarshal(reqData, &req); err != nil {
		return sf.errorResponse("", "payload json inválido: "+err.Error())
	}

	if req.Version != 1 {
		return sf.errorResponse(req.RequestID, "versão não suportada")
	}

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
