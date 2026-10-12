package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// scriptTask monta uma task RunScript com referência de script.
func scriptTask(taskID, scriptID, hash, content string) AutomationTask {
	task := AutomationTask{TaskID: taskID, ActionType: ActionRunScript, ScriptID: scriptID}
	task.Script = &AutomationScript{
		ScriptID:          scriptID,
		ScriptType:        ScriptPowerShell,
		ContentHashSHA256: hash,
		Content:           content,
	}
	return task
}

// Sync automático entrega só a referência: o conteúdo já baixado precisa
// sobreviver quando o hash declarado confere.
func TestPreserveScriptContent_ReusaQuandoHashConfere(t *testing.T) {
	prev := []AutomationTask{scriptTask("t1", "s1", "hash-a", "Write-Output 1")}
	next := []AutomationTask{scriptTask("t1", "s1", "hash-a", "")}

	preserveScriptContent(prev, next)

	if got := strings.TrimSpace(next[0].Script.Content); got != "Write-Output 1" {
		t.Fatalf("conteudo deveria ter sido preservado, obtido %q", got)
	}
}

// Hash diferente = script alterado no servidor; reaproveitar o conteúdo antigo
// executaria o script errado.
func TestPreserveScriptContent_NaoReusaQuandoHashMuda(t *testing.T) {
	prev := []AutomationTask{scriptTask("t1", "s1", "hash-a", "antigo")}
	next := []AutomationTask{scriptTask("t1", "s1", "hash-b", "")}

	preserveScriptContent(prev, next)

	if next[0].Script.Content != "" {
		t.Fatalf("hash diferente nao pode reaproveitar conteudo antigo, obtido %q", next[0].Script.Content)
	}
}

// O conteúdo que veio no próprio sync nunca é sobrescrito pelo cache local.
func TestPreserveScriptContent_NaoSobrescreveConteudoNovo(t *testing.T) {
	prev := []AutomationTask{scriptTask("t1", "s1", "hash-a", "antigo")}
	next := []AutomationTask{scriptTask("t1", "s1", "hash-a", "novo")}

	preserveScriptContent(prev, next)

	if next[0].Script.Content != "novo" {
		t.Fatalf("conteudo novo nao pode ser sobrescrito, obtido %q", next[0].Script.Content)
	}
}

func TestTasksMissingScriptContent(t *testing.T) {
	if !tasksMissingScriptContent([]AutomationTask{scriptTask("t1", "s1", "h", "")}) {
		t.Fatal("RunScript sem conteudo deveria sinalizar ausencia")
	}
	if tasksMissingScriptContent([]AutomationTask{scriptTask("t1", "s1", "h", "x")}) {
		t.Fatal("RunScript com conteudo nao pode sinalizar ausencia")
	}
	pkg := AutomationTask{TaskID: "t2", ActionType: ActionInstallPackage, PackageID: "x"}
	if tasksMissingScriptContent([]AutomationTask{pkg}) {
		t.Fatal("task de pacote nao entra na checagem de conteudo de script")
	}
}

// Conteúdo ausente é falha de SINCRONIZAÇÃO: não pode pausar a task por 24h,
// senão o problema real fica escondido e a recuperação nunca ocorre.
func TestCircuitBreakerNaoAbreParaConteudoAusente(t *testing.T) {
	task := braveTask()
	svc, _ := newWatchdogService(t, []AutomationTask{task}, "fp-conteudo")

	for i := 0; i < failureThreshold+2; i++ {
		svc.updateAntiLoopState("agent-1", task, ExecutionResult{
			Success:      false,
			ExitCode:     2,
			ExitCodeSet:  true,
			ErrorMessage: errMsgScriptContentMissing,
		})
	}

	if svc.circuitBreakerPaused("agent-1", task.TaskID) {
		t.Fatal("falha de conteudo ausente nao pode abrir o circuit breaker")
	}
}

// Falhas reais continuam abrindo o circuit breaker normalmente.
func TestCircuitBreakerAbreParaFalhaReal(t *testing.T) {
	task := braveTask()
	svc, _ := newWatchdogService(t, []AutomationTask{task}, "fp-real")

	for i := 0; i < failureThreshold; i++ {
		svc.updateAntiLoopState("agent-1", task, ExecutionResult{Success: false, ErrorMessage: "winget falhou"})
	}

	if !svc.circuitBreakerPaused("agent-1", task.TaskID) {
		t.Fatal("falhas reais consecutivas deveriam abrir o circuit breaker")
	}
}

// Regressão do bug relatado: o primeiro sync (IncludeScriptContent=false) entrega
// a task sem o corpo; o agent precisa refazer o sync pedindo o conteúdo e SEM
// enviar o fingerprint conhecido (senão o servidor responde up-to-date com
// Tasks=[] e o conteúdo nunca chega).
func TestRefreshPolicyAutoRecuperaConteudoDeScript(t *testing.T) {
	const scriptContent = "Write-Output 'anydesk-id'"
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		var got struct {
			Request PolicySyncRequest `json:"request"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		includeContent := got.Request.IncludeScriptContent != nil && *got.Request.IncludeScriptContent

		task := AutomationTask{
			TaskID:     "01a1310e",
			Name:       "COletarAnydesk",
			ActionType: ActionRunScript,
			ScriptID:   "01a130fa",
			Script: &AutomationScript{
				ScriptID:          "01a130fa",
				ScriptType:        ScriptPowerShell,
				ContentHashSHA256: "hash-a",
			},
		}
		if includeContent {
			if got.Request.KnownPolicyFingerprint != nil {
				t.Error("sync com conteudo nao deve enviar KnownPolicyFingerprint")
			}
			task.Script.Content = scriptContent
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PolicySyncResponse{
			PolicyFingerprint: "fp-a",
			GeneratedAt:       "2026-10-12T17:00:00Z",
			TaskCount:         1,
			Tasks:             []AutomationTask{task},
		})
	}))
	defer srv.Close()

	svc, _ := newWatchdogService(t, nil, "")
	svc.getConfig = func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}
	}

	if _, err := svc.refreshPolicy(context.Background(), false); err != nil {
		t.Fatalf("refreshPolicy: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("esperava 2 syncs (normal + recuperacao de conteudo), obtido %d", got)
	}
	state := svc.GetState()
	if len(state.Tasks) != 1 {
		t.Fatalf("esperava 1 task, obtido %d", len(state.Tasks))
	}
	if got := strings.TrimSpace(state.Tasks[0].Script.Content); got != scriptContent {
		t.Fatalf("conteudo do script nao foi sincronizado: %q", got)
	}
}
