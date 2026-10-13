package automation

import "testing"

// reportTriggerSkip não pode explodir quando o serviço ainda não tem client ou
// banco (bootstrap) — o gatilho chama isso em goroutine para toda tarefa
// dispensada.
func TestReportTriggerSkip_NoOpWithoutClientOrDB(t *testing.T) {
	svc := &Service{}
	svc.reportTriggerSkip("agent-1", AutomationTask{TaskID: "task-1"}, ExecutionSourceForceSync, TriggerTypeImmediate, "pacote ja instalado")
	svc.reportTriggerSkip("", AutomationTask{TaskID: "task-1"}, ExecutionSourceForceSync, TriggerTypeImmediate, "motivo")
}
