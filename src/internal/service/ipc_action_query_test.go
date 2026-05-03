package service

import (
	"testing"
	"time"

	"discovery/internal/automation"
	"discovery/internal/database"
)

func TestDispatchCommand_GetActionStatus(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-dispatch",
		UserSID:     "S-1-5-21-444",
		UserName:    "DESKTOP\\ipc",
		Command:     "install_package",
		PayloadJSON: `{"action":"install_package","package":"7zip"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	server := &IPCServer{manager: &ServiceManager{db: db}}

	response := server.dispatchCommand(&ClientRequest{
		ID:      "req-status",
		Command: "getActionStatus",
		Payload: map[string]interface{}{"action_id": "action-dispatch"},
	})
	if response.Code != 200 || response.Status != "success" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Data["status"] != "queued" {
		t.Fatalf("expected queued status, got %v", response.Data["status"])
	}
}

func TestDispatchCommand_GetActionHistoryValidation(t *testing.T) {
	server := &IPCServer{manager: &ServiceManager{}}

	missingAction := server.dispatchCommand(&ClientRequest{
		ID:      "req-history-missing",
		Command: "getActionHistory",
		Payload: map[string]interface{}{},
	})
	if missingAction.Code != 400 {
		t.Fatalf("expected 400 for missing action_id, got %+v", missingAction)
	}

	invalidLimit := server.dispatchCommand(&ClientRequest{
		ID:      "req-history-invalid-limit",
		Command: "getActionHistory",
		Payload: map[string]interface{}{"action_id": "action-1", "limit": -1},
	})
	if invalidLimit.Code != 400 {
		t.Fatalf("expected 400 for invalid limit, got %+v", invalidLimit)
	}
}

// TestMultiUserActionQueue verifica que ações de dois usuários distintos são
// enfileiradas e consultáveis de forma independente pela mesma instância de service.
func TestMultiUserActionQueue(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	actions := []database.ActionQueueEntry{
		{
			ActionID:    "action-user1",
			UserSID:     "S-1-5-21-111",
			UserName:    "DESKTOP\\user1",
			Command:     "refresh_inventory",
			PayloadJSON: `{"action":"refresh_inventory"}`,
			QueuedAt:    time.Now(),
		},
		{
			ActionID:    "action-user2",
			UserSID:     "S-1-5-21-222",
			UserName:    "DESKTOP\\user2",
			Command:     "install_package",
			PayloadJSON: `{"action":"install_package","package":"notepadplusplus"}`,
			QueuedAt:    time.Now(),
		},
	}
	for _, a := range actions {
		if err := db.EnqueueAction(a); err != nil {
			t.Fatalf("enqueue %s: %v", a.ActionID, err)
		}
	}

	server := &IPCServer{manager: &ServiceManager{db: db}}

	for _, tc := range []struct {
		actionID string
		userSID  string
		command  string
	}{
		{"action-user1", "S-1-5-21-111", "refresh_inventory"},
		{"action-user2", "S-1-5-21-222", "install_package"},
	} {
		resp := server.dispatchCommand(&ClientRequest{
			ID:       "req-" + tc.actionID,
			Command:  "getActionStatus",
			UserSID:  tc.userSID,
			UserName: "DESKTOP\\user",
			Payload:  map[string]interface{}{"action_id": tc.actionID},
		})
		if resp.Code != 200 {
			t.Fatalf("action %s: expected 200, got %d – %s", tc.actionID, resp.Code, resp.Message)
		}
		if resp.Data["command"] != tc.command {
			t.Fatalf("action %s: expected command %q, got %v", tc.actionID, tc.command, resp.Data["command"])
		}
		if resp.Data["user_sid"] != tc.userSID {
			t.Fatalf("action %s: expected user_sid %q, got %v", tc.actionID, tc.userSID, resp.Data["user_sid"])
		}
	}
}

// TestDispatchCommand_GetPolicies_InMemory verifica que getPolicies retorna
// as tarefas do serviço em memória quando disponíveis (estado online).
func TestDispatchCommand_GetPolicies_InMemory(t *testing.T) {
	fakeSvc := &fakeAutomationService{
		currentTasks: []automation.AutomationTask{
			{TaskID: "task-mem-1", Name: "Instalar Chrome", ActionType: "install"},
			{TaskID: "task-mem-2", Name: "Atualizar Edge", ActionType: "upgrade"},
		},
	}

	server := &IPCServer{manager: &ServiceManager{
		automationSvc: fakeSvc,
		config:        &SharedConfig{AgentID: "agent-test"},
	}}

	resp := server.dispatchCommand(&ClientRequest{
		ID:      "req-policies-mem",
		Command: "getPolicies",
	})
	if resp.Code != 200 || resp.Status != "success" {
		t.Fatalf("expected 200 success, got %d %s: %s", resp.Code, resp.Status, resp.Message)
	}

	policies, ok := resp.Data["policies"].([]map[string]interface{})
	if !ok {
		t.Fatalf("policies not []map[string]interface{}: %T", resp.Data["policies"])
	}
	if len(policies) != 2 {
		t.Fatalf("expected 2 policies from memory, got %d", len(policies))
	}
	if policies[0]["task_id"] != "task-mem-1" {
		t.Fatalf("unexpected task_id: %v", policies[0]["task_id"])
	}
}

// TestDispatchCommand_GetPolicies_FallbackPersisted verifica que getPolicies
// cai no snapshot persistido quando o serviço em memória não tem tarefas.
func TestDispatchCommand_GetPolicies_FallbackPersisted(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Gravar política persistida no banco.
	policyJSON := `{"policy":{"Tasks":[{"TaskId":"task-db-1","Name":"Install 7zip","ActionType":"install"}]},"savedAt":"2026-01-01T00:00:00Z"}`
	if err := db.SaveAutomationPolicy("agent-test", "fp-001", []byte(policyJSON)); err != nil {
		t.Fatalf("save policy: %v", err)
	}

	// Serviço em memória sem tarefas → deve cair no fallback.
	fakeSvc := &fakeAutomationService{currentTasks: nil}

	server := &IPCServer{manager: &ServiceManager{
		db:            db,
		automationSvc: fakeSvc,
		config:        &SharedConfig{AgentID: "agent-test"},
	}}

	resp := server.dispatchCommand(&ClientRequest{
		ID:      "req-policies-db",
		Command: "getPolicies",
	})
	if resp.Code != 200 || resp.Status != "success" {
		t.Fatalf("expected 200 success, got %d %s: %s", resp.Code, resp.Status, resp.Message)
	}

	policies, ok := resp.Data["policies"].([]map[string]interface{})
	if !ok {
		t.Fatalf("policies not []map[string]interface{}: %T", resp.Data["policies"])
	}
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy from persisted db, got %d", len(policies))
	}
	if policies[0]["task_id"] != "task-db-1" {
		t.Fatalf("unexpected task_id: %v", policies[0]["task_id"])
	}
}

// TestDispatchCommand_UnknownCommand verifica que comandos não reconhecidos
// retornam 404 independentemente do usuário.
func TestDispatchCommand_UnknownCommand(t *testing.T) {
	server := &IPCServer{manager: &ServiceManager{}}

	resp := server.dispatchCommand(&ClientRequest{
		ID:       "req-unknown",
		Command:  "doSomethingWeird",
		UserSID:  "S-1-5-21-999",
		UserName: "DESKTOP\\attacker",
	})
	if resp.Code != 404 || resp.Status != "error" {
		t.Fatalf("expected 404 error for unknown command, got %d %s", resp.Code, resp.Status)
	}
}

func TestDispatchCommand_TriggerUpdateCheck(t *testing.T) {
	manager := &ServiceManager{updateTrigger: make(chan bool, 1)}
	server := &IPCServer{manager: manager}

	resp := server.dispatchCommand(&ClientRequest{
		ID:      "req-update-check",
		Command: "triggerUpdateCheck",
		Payload: map[string]interface{}{"source": "command:check-update"},
	})
	if resp.Code != 202 || resp.Status != "success" {
		t.Fatalf("expected 202 success, got %+v", resp)
	}
	if queued, _ := resp.Data["queued"].(bool); !queued {
		t.Fatalf("expected queued=true, got %+v", resp.Data)
	}
	select {
	case force := <-manager.updateTrigger:
		if !force {
			t.Fatalf("expected forced update signal=true for command source")
		}
	default:
		t.Fatalf("expected update trigger signal to be enqueued")
	}
}
