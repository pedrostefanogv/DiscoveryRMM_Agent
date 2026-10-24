package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"discovery/app/core/notifyui"
)

// ── Worker de UI das notificações nativas ──
//
// O SERVIÇO roda em sessão 0 (SYSTEM): uma janela criada no próprio processo
// é invisível para o usuário. O caminho PSADT antigo funcionava porque
// lançava powershell.exe COM O TOKEN DO USUÁRIO INTERATIVO
// (CreateProcessAsUser). O substituto nativo replica isso: o serviço grava um
// job JSON em disco e spawna o discovery-agent.exe (irmão no diretório de
// instalação) com --notify-worker=<job> e o token da sessão interativa; o
// worker desenha o cartão/toast/progresso na sessão do usuário e devolve o
// resultado em JSON no stdout.

// notifyWorkerJob é o job gravado em disco para o worker.
type notifyWorkerJob struct {
	Kind    string          `json:"kind"` // "card" | "toast" | "progress" | "visual"
	Request json.RawMessage `json:"request"`
}

// notifyToastResult é o resultado do job toast.
type notifyToastResult struct {
	Error string `json:"error"`
}

// HandleNotifyWorkerCLI processa --notify-worker=<job.json> e devolve true
// quando o processo é um worker (o chamador deve sair sem iniciar a GUI).
func HandleNotifyWorkerCLI(args []string) bool {
	path := ""
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		for _, prefix := range []string{"--notify-worker=", "/notify-worker=", "-notify-worker="} {
			if strings.HasPrefix(arg, prefix) {
				path = strings.TrimSpace(arg[len(prefix):])
			}
		}
	}
	if path == "" {
		return false
	}
	runNotifyWorkerFromFile(path)
	return true
}

func runNotifyWorkerFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		writeWorkerResult(notifyToastResult{Error: "falha ao ler job: " + err.Error()})
		return
	}
	var job notifyWorkerJob
	if err := json.Unmarshal(data, &job); err != nil {
		fmt.Println(`{"error":"job invalido"}`)
		return
	}
	ctx := context.Background()
	switch job.Kind {
	case "card":
		var req notifyui.CardRequest
		if err := json.Unmarshal(job.Request, &req); err != nil {
			fmt.Println(`{"error":"request invalido"}`)
			return
		}
		res, ok := notifyui.ShowCard(ctx, req)
		if !ok {
			res = notifyui.CardResult{Outcome: notifyui.OutcomeError, Reason: res.Reason}
		}
		writeWorkerResult(res)
	case "toast":
		var req notifyui.ToastRequest
		if err := json.Unmarshal(job.Request, &req); err != nil {
			fmt.Println(`{"error":"request invalido"}`)
			return
		}
		err := notifyui.ShowToast(req)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		writeWorkerResult(notifyToastResult{Error: msg})
	case "progress":
		var req notifyui.ProgressRequest
		if err := json.Unmarshal(job.Request, &req); err != nil {
			fmt.Println(`{"error":"request invalido"}`)
			return
		}
		res, ok := notifyui.ShowProgress(ctx, req)
		if !ok {
			res = notifyui.ProgressResult{Outcome: notifyui.ProgressError, Reason: res.Reason}
		}
		writeWorkerResult(res)
	case "visual":
		var req NativeVisualNotificationRequest
		if err := json.Unmarshal(job.Request, &req); err != nil {
			fmt.Println(`{"error":"request invalido"}`)
			return
		}
		result := NativeToolResult{}
		writeWorkerResult(executeNativeVisualNotification(req, &result))
	default:
		fmt.Println(`{"error":"kind desconhecido"}`)
	}
}

func writeWorkerResult(result any) {
	data, err := json.Marshal(result)
	if err != nil {
		fmt.Println(`{"error":"falha ao serializar resultado"}`)
		return
	}
	fmt.Println(string(data))
}