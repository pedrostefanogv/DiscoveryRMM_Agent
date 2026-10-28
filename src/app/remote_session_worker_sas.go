//go:build windows

package app

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"discovery/app/core/remotesession"
)

// Delegação da geração de SAS (Ctrl+Alt+Del) do worker para o SERVIÇO.
//
// O SendSAS (sas.dll) só tem entrega garantida ao winlogon quando chamado de
// um serviço LocalSystem na SESSÃO 0 (AsUser=FALSE). O worker roda na sessão
// interativa: mesmo SYSTEM, a chamada pode retornar TRUE sem efeito (campo,
// 27/10/2026, Windows Server 2025 em VM — "injetada" e a tela de bloqueio não
// mudava). Este arquivo implementa o round-trip pelos canais já existentes:
//
//	worker --stderr--> "[remote-session-worker] sas-request id=<corr>"
//	serviço: executa screen.SendSAS() na sessão 0
//	serviço --stdin (framed)--> {"action":"sas_result","id":"<corr>","ok":bool,"error":"..."}
//	worker: devolve nil/erro ao InputController → feedback ao viewer.

const (
	// sasRequestStderrMarker é a linha impressa no stderr pelo worker pedindo
	// que o serviço gere a SAS (drainStderr reconhece pelo prefixo).
	sasRequestStderrMarker = "[remote-session-worker] sas-request"
	// sasRequestTimeout limita a espera pela confirmação do serviço. Excedido,
	// o erro é propagado ao viewer (fail-visible) em vez de um sucesso oco.
	sasRequestTimeout = 5 * time.Second
)

// sasPending guarda os canais de resposta por correlação.
var sasPending struct {
	mu   sync.Mutex
	byID map[string]chan string
}

func init() { sasPending.byID = make(map[string]chan string) }

// installServiceSASDelegate registra o delegado de SAS no remotesession.
// Chamado uma única vez por RunRemoteSessionWorker.
func installServiceSASDelegate() {
	remotesession.SetSASRequester(requestSASViaService)
}

// requestSASViaService pede ao serviço (sessão 0) que gere a SAS e bloqueia
// até a confirmação (ou timeout). A resposta é a mensagem de erro, ou "" em
// caso de sucesso.
func requestSASViaService() error {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	ch := make(chan string, 1)
	sasPending.mu.Lock()
	sasPending.byID[id] = ch
	sasPending.mu.Unlock()
	defer func() {
		sasPending.mu.Lock()
		delete(sasPending.byID, id)
		sasPending.mu.Unlock()
	}()

	fmt.Fprintf(os.Stderr, "%s id=%s\n", sasRequestStderrMarker, id)

	select {
	case reply := <-ch:
		if strings.TrimSpace(reply) == "" {
			return nil
		}
		return fmt.Errorf("SendSAS via serviço: %s", reply)
	case <-time.After(sasRequestTimeout):
		return fmt.Errorf("serviço não confirmou o SendSAS em %s (serviço pai indisponível ou binário do serviço desatualizado)", sasRequestTimeout)
	}
}

// handleSASResultCommand entrega ao requerente pendente a resposta do serviço
// (chamado pelo monitor de stdin do worker). Retorna true se o comando era um
// sas_result (tratado aqui, não vai ao manager).
func handleSASResultCommand(cmd map[string]any) bool {
	if act, _ := cmd["action"].(string); act != "sas_result" {
		return false
	}
	id, _ := cmd["id"].(string)
	sasPending.mu.Lock()
	ch := sasPending.byID[id]
	delete(sasPending.byID, id)
	sasPending.mu.Unlock()
	if ch == nil {
		return true // resposta tardia/duplicada — descarta silenciosamente
	}
	if ok, _ := cmd["ok"].(bool); ok {
		ch <- ""
	} else {
		errMsg, _ := cmd["error"].(string)
		if strings.TrimSpace(errMsg) == "" {
			// Serviço sinalizou falha SEM mensagem (versão malformada/futura):
			// um "" aqui seria interpretado como SUCESSO pelo requerente.
			errMsg = "SendSAS recusado pelo serviço (sem detalhes do erro)"
		}
		ch <- errMsg
	}
	return true
}
