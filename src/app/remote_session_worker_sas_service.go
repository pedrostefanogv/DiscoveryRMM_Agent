//go:build windows

package app

import (
	"log"
	"strings"

	"discovery/app/core/screen"
)

// Lado do SERVIÇO da delegação de SAS (ver remote_session_worker_sas.go).
// O serviço é LocalSystem na sessão 0: é o chamador suportado do SendSAS
// (sas.dll, AsUser=FALSE) — o caminho que os outros RMMs (RustDesk,
// MeshCentral) usam para gerar Ctrl+Alt+Del em sessões bloqueadas/logon.

const sasRequestPrefix = sasRequestStderrMarker + " id="


// parseSASRequestLine reconhece a linha de pedido de SAS do worker no stderr
// e devolve o id de correlação.
func parseSASRequestLine(line string) (string, bool) {
	if !strings.HasPrefix(line, sasRequestPrefix) {
		return "", false
	}
	id := strings.TrimSpace(strings.TrimPrefix(line, sasRequestPrefix))
	if id == "" {
		return "", false
	}
	return id, true
}

// handleWorkerSASRequest executa o SendSAS no serviço (sessão 0) e responde
// ao worker pelo stdin framed. Em goroutine: o dreno do stderr é serial e o
// SendSAS não pode bloquear as demais linhas do worker.
func handleWorkerSASRequest(w *remoteSessionWorkerProc, id string) {
	go func() {
		err := screen.SendSAS()
		if err != nil {
			log.Printf("[remote-session] SendSAS via serviço (sessão 0) FALHOU id=%s: %v", id, err)
		} else {
			log.Printf("[remote-session] SendSAS executado no serviço (LocalSystem/sessão 0) id=%s", id)
		}
		payload := map[string]any{"action": "sas_result", "id": id, "ok": err == nil}
		if err != nil {
			payload["error"] = err.Error()
		}
		if werr := writeWorkerPayload(w, payload); werr != nil {
			log.Printf("[remote-session] sas-request id=%s: falha ao responder ao worker: %v", id, werr)
		}
	}()
}