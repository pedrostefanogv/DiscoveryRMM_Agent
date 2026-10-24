//go:build windows

package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/notifyui"
	"discovery/app/core/platform"
	"discovery/app/core/processutil"
)

// ── Spawner do worker de UI (lado do serviço) ──

// sessionIsInteractive informa se o processo atual já roda na sessão
// interativa (UI/desktop). Na sessão 0 (serviço) janelas são invisíveis.
// Reusa currentProcessSessionID (session_windows.go).
func sessionIsInteractive() bool {
	return currentProcessSessionID() != 0
}

// spawnNotifyWorker grava o job em disco e executa o discovery-agent.exe
// (irmão no diretório de instalação) NA SESSÃO DO USUÁRIO INTERATIVO
// (WTSQueryUserToken → CreateProcessAsUser). Devolve o stdout do worker.
func spawnNotifyWorker(ctx context.Context, job notifyWorkerJob, timeout time.Duration) ([]byte, error) {
	exe, err := uiAgentExecutablePath()
	if err != nil {
		return nil, err
	}
	tmpBase, err := platform.EnsureTempDir()
	if err != nil {
		return nil, err
	}
	jobFile, err := os.CreateTemp(tmpBase, "notify-job-*.json")
	if err != nil {
		return nil, err
	}
	jobPath := jobFile.Name()
	defer os.Remove(jobPath)
	data, err := json.Marshal(job)
	if err != nil {
		jobFile.Close()
		return nil, err
	}
	slog.Info("notify-worker: spawn na sessão do usuário", "exe", exe, "kind", job.Kind, "job", jobPath)
	if _, err := jobFile.Write(data); err != nil {
		jobFile.Close()
		return nil, err
	}
	if err := jobFile.Close(); err != nil {
		return nil, err
	}
	// O worker roda como USUÁRIO (CreateProcessAsUser): o job precisa ser
	// legível por ele (mesma política dos .ps1 do staging PSADT — o diretório
	// já tem ACL Everyone RX, mas o chmod deixa a intenção explícita).
	if err := os.Chmod(jobPath, 0o644); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, exe, "--notify-worker="+jobPath)
	cmd.Dir = filepath.Dir(exe)
	processutil.HideWindow(cmd)
	userCtx, cleanup, ok := withActiveUserToken(runCtx)
	if ok {
		defer cleanup()
		processutil.ApplyUserContext(userCtx, cmd)
	}
	// Sem token de usuário ativo o spawn roda como SYSTEM na sessão 0 — o
	// worker não vai conseguir criar janela; falha rápido para o caller tratar.
	if !ok {
		return nil, errors.New("sem sessao de usuario interativa ativa")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	return out, nil
}

// parseWorkerOutput extrai a última linha JSON do stdout do worker.
func parseWorkerOutput(out []byte, target any) error {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return errors.New("worker sem saida")
	}
	lines := strings.Split(text, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	return json.Unmarshal([]byte(last), target)
}

// runCardAsUser exibe o cartão nativo: na sessão interativa quando o processo
// já é da sessão do usuário, ou via worker quando o chamador é o serviço.
func runCardAsUser(ctx context.Context, req notifyui.CardRequest, timeout time.Duration) (notifyui.CardResult, bool) {
	if sessionIsInteractive() {
		return notifyui.ShowCard(ctx, req)
	}
	jobReq, err := json.Marshal(req)
	if err != nil {
		return notifyui.CardResult{Outcome: notifyui.OutcomeError, Reason: err.Error()}, false
	}
	out, err := spawnNotifyWorker(ctx, notifyWorkerJob{Kind: "card", Request: jobReq}, timeout)
	if err != nil {
		reason := err.Error()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return notifyui.CardResult{Outcome: notifyui.OutcomeCanceled, Reason: reason}, true
		}
		return notifyui.CardResult{Outcome: notifyui.OutcomeError, Reason: reason}, false
	}
	var res notifyui.CardResult
	if perr := parseWorkerOutput(out, &res); perr != nil {
		return notifyui.CardResult{Outcome: notifyui.OutcomeError, Reason: perr.Error()}, false
	}
	// Resultado vazio/desconhecido do worker = falha silenciosa de UI — nunca
	// tratar como desfecho válido (o caller acharia que o usuário respondeu).
	if res.Outcome == "" || res.Outcome == notifyui.OutcomeError {
		if res.Reason == "" {
			res.Reason = "worker sem desfecho válido"
		}
		return res, false
	}
	return res, true
}

// runToastAsUser exibe o toast (mesma política do cartão). nil = exibido.
func runToastAsUser(ctx context.Context, req notifyui.ToastRequest) error {
	if sessionIsInteractive() {
		return notifyui.ShowToast(req)
	}
	jobReq, err := json.Marshal(req)
	if err != nil {
		return err
	}
	out, err := spawnNotifyWorker(ctx, notifyWorkerJob{Kind: "toast", Request: jobReq}, 60*time.Second)
	if err != nil {
		return err
	}
	var res notifyToastResult
	if perr := parseWorkerOutput(out, &res); perr != nil {
		return perr
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// runProgressAsUser exibe a janela de progresso (mesma política do cartão).
func runProgressAsUser(ctx context.Context, req notifyui.ProgressRequest) (notifyui.ProgressResult, bool) {
	if sessionIsInteractive() {
		return notifyui.ShowProgress(ctx, req)
	}
	jobReq, err := json.Marshal(req)
	if err != nil {
		return notifyui.ProgressResult{Outcome: notifyui.ProgressError, Reason: err.Error()}, false
	}
	timeout := time.Duration(req.DurationSeconds+30) * time.Second
	out, err := spawnNotifyWorker(ctx, notifyWorkerJob{Kind: "progress", Request: jobReq}, timeout)
	if err != nil {
		return notifyui.ProgressResult{Outcome: notifyui.ProgressError, Reason: err.Error()}, false
	}
	var res notifyui.ProgressResult
	if perr := parseWorkerOutput(out, &res); perr != nil {
		return notifyui.ProgressResult{Outcome: notifyui.ProgressError, Reason: perr.Error()}, false
	}
	if res.Outcome == "" || res.Outcome == notifyui.ProgressError {
		if res.Reason == "" {
			res.Reason = "worker sem desfecho válido"
		}
		return res, false
	}
	return res, true
}