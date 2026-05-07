package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"discovery/internal/automation"
	"discovery/internal/database"
)

type fakeAutomationService struct {
	result       interface{}
	err          error
	calls        int
	currentTasks []automation.AutomationTask
}

func (f *fakeAutomationService) RefreshPolicy(ctx context.Context, includeScriptContent bool) (interface{}, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeAutomationService) GetCurrentTasks() []automation.AutomationTask {
	return f.currentTasks
}

type fakeInventoryService struct {
	result interface{}
	err    error
	calls  int
}

func (f *fakeInventoryService) Collect(ctx context.Context) (interface{}, error) {
	f.calls++
	return f.result, f.err
}

type fakeAppsService struct {
	installOutput string
	installErr    error
	installCalls  int
	lastPackageID string
}

type fakeAgentRuntime struct {
	forceHeartbeatResult bool
	forceHeartbeatCalls  int
}

func (f *fakeAgentRuntime) Run(ctx context.Context) {}

func (f *fakeAgentRuntime) Reload() {}

func (f *fakeAgentRuntime) ForceHeartbeat() bool {
	f.forceHeartbeatCalls++
	return f.forceHeartbeatResult
}

func (f *fakeAgentRuntime) GetStatus() AgentConnectionStatus {
	return AgentConnectionStatus{}
}

func (f *fakeAgentRuntime) IngestRemoteDebugLog(line string) {}

func (f *fakeAppsService) Install(ctx context.Context, id string) (string, error) {
	f.installCalls++
	f.lastPackageID = id
	return f.installOutput, f.installErr
}

func (f *fakeAppsService) Uninstall(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (f *fakeAppsService) Upgrade(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (f *fakeAppsService) UpgradeAll(ctx context.Context) (string, error) {
	return "", nil
}

func TestProcessNextQueuedAction_RefreshPolicy(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	automationSvc := &fakeAutomationService{result: map[string]interface{}{"fingerprint": "abc"}}
	sm := &ServiceManager{
		db:            db,
		automationSvc: automationSvc,
		actionTrigger: make(chan struct{}, 1),
	}

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-refresh",
		UserSID:     "S-1-5-21-111",
		UserName:    "DESKTOP\\user",
		Command:     "refresh_policy",
		PayloadJSON: `{"action":"refresh_policy"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	processed, err := sm.processNextQueuedAction(context.Background())
	if err != nil {
		t.Fatalf("process action: %v", err)
	}
	if !processed {
		t.Fatalf("expected one action to be processed")
	}
	if automationSvc.calls != 1 {
		t.Fatalf("expected RefreshPolicy to be called once, got %d", automationSvc.calls)
	}

	stored, found, err := db.GetAction("action-refresh")
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found || stored.Status != "completed" {
		t.Fatalf("expected completed action, found=%v status=%q", found, stored.Status)
	}

	history, err := db.ListActionHistory("action-refresh", 10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one history row, got %d", len(history))
	}
	if history[0].Status != "completed" {
		t.Fatalf("unexpected history status: %s", history[0].Status)
	}
}

func TestProcessNextQueuedAction_InstallPackage(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	appsSvc := &fakeAppsService{installOutput: "installed ok"}
	sm := &ServiceManager{
		db:            db,
		appsSvc:       appsSvc,
		actionTrigger: make(chan struct{}, 1),
	}

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-install",
		UserSID:     "S-1-5-21-222",
		UserName:    "DESKTOP\\user",
		Command:     "install_package",
		PayloadJSON: `{"action":"install_package","package":"7zip"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	processed, err := sm.processNextQueuedAction(context.Background())
	if err != nil {
		t.Fatalf("process action: %v", err)
	}
	if !processed {
		t.Fatalf("expected one action to be processed")
	}
	if appsSvc.installCalls != 1 {
		t.Fatalf("expected Install to be called once, got %d", appsSvc.installCalls)
	}
	if appsSvc.lastPackageID != "7zip" {
		t.Fatalf("unexpected package id: %s", appsSvc.lastPackageID)
	}

	stored, found, err := db.GetAction("action-install")
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found || stored.Status != "completed" {
		t.Fatalf("expected completed action, found=%v status=%q", found, stored.Status)
	}

	history, err := db.ListActionHistory("action-install", 10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one history row, got %d", len(history))
	}
	if history[0].Output != "installed ok" {
		t.Fatalf("unexpected history output: %s", history[0].Output)
	}
}

func TestGetActionStatusAndHistory(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	automationSvc := &fakeAutomationService{result: map[string]interface{}{"fingerprint": "xyz"}}
	sm := &ServiceManager{
		db:            db,
		automationSvc: automationSvc,
		actionTrigger: make(chan struct{}, 1),
	}

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-status",
		UserSID:     "S-1-5-21-333",
		UserName:    "DESKTOP\\user",
		Command:     "refresh_policy",
		PayloadJSON: `{"action":"refresh_policy"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	status, found, err := sm.GetActionStatus("action-status")
	if err != nil {
		t.Fatalf("get action status: %v", err)
	}
	if !found {
		t.Fatalf("expected action status to be found")
	}
	if status["status"] != "queued" {
		t.Fatalf("expected queued status, got %v", status["status"])
	}

	processed, err := sm.processNextQueuedAction(context.Background())
	if err != nil {
		t.Fatalf("process action: %v", err)
	}
	if !processed {
		t.Fatalf("expected action to be processed")
	}

	status, found, err = sm.GetActionStatus("action-status")
	if err != nil {
		t.Fatalf("get completed action status: %v", err)
	}
	if !found {
		t.Fatalf("expected completed action status to be found")
	}
	if status["status"] != "completed" {
		t.Fatalf("expected completed status, got %v", status["status"])
	}

	history, found, err := sm.GetActionHistory("action-status", 10)
	if err != nil {
		t.Fatalf("get action history: %v", err)
	}
	if !found {
		t.Fatalf("expected action history to be found")
	}
	entries, ok := history["history"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected typed history entries")
	}
	if len(entries) != 1 {
		t.Fatalf("expected one history entry, got %d", len(entries))
	}
	if entries[0]["status"] != "completed" {
		t.Fatalf("unexpected history status: %v", entries[0]["status"])
	}
	if history["count"] != 1 {
		t.Fatalf("expected history count=1, got %v", history["count"])
	}

	_, found, err = sm.GetActionStatus("missing-action")
	if err != nil {
		t.Fatalf("get missing action status: %v", err)
	}
	if found {
		t.Fatalf("expected missing action to return found=false")
	}
}

func TestProcessNextQueuedAction_ForceHeartbeat(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentRuntime := &fakeAgentRuntime{forceHeartbeatResult: true}
	sm := &ServiceManager{
		db:            db,
		agentRuntime:  agentRuntime,
		actionTrigger: make(chan struct{}, 1),
	}

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-force-heartbeat",
		UserSID:     "S-1-5-21-444",
		UserName:    "DESKTOP\\user",
		Command:     "force_heartbeat",
		PayloadJSON: `{"action":"force_heartbeat","source":"debug-manual-heartbeat"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	processed, err := sm.processNextQueuedAction(context.Background())
	if err != nil {
		t.Fatalf("process action: %v", err)
	}
	if !processed {
		t.Fatalf("expected one action to be processed")
	}
	if agentRuntime.forceHeartbeatCalls != 1 {
		t.Fatalf("expected ForceHeartbeat to be called once, got %d", agentRuntime.forceHeartbeatCalls)
	}

	stored, found, err := db.GetAction("action-force-heartbeat")
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found || stored.Status != "completed" {
		t.Fatalf("expected completed action, found=%v status=%q", found, stored.Status)
	}
}

func TestProcessNextQueuedAction_ForceHeartbeatFailure(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentRuntime := &fakeAgentRuntime{forceHeartbeatResult: false}
	sm := &ServiceManager{
		db:            db,
		agentRuntime:  agentRuntime,
		actionTrigger: make(chan struct{}, 1),
	}

	err = db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    "action-force-heartbeat-failure",
		UserSID:     "S-1-5-21-445",
		UserName:    "DESKTOP\\user",
		Command:     "force_heartbeat",
		PayloadJSON: `{"action":"force_heartbeat"}`,
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	processed, err := sm.processNextQueuedAction(context.Background())
	if err != nil {
		t.Fatalf("process action: %v", err)
	}
	if !processed {
		t.Fatalf("expected one action to be processed")
	}
	if agentRuntime.forceHeartbeatCalls != 1 {
		t.Fatalf("expected ForceHeartbeat to be called once, got %d", agentRuntime.forceHeartbeatCalls)
	}

	stored, found, err := db.GetAction("action-force-heartbeat-failure")
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found || stored.Status != "failed" {
		t.Fatalf("expected failed action, found=%v status=%q", found, stored.Status)
	}
	if !strings.Contains(strings.ToLower(stored.ErrorMessage), "timeout") {
		t.Fatalf("unexpected error message: %q", stored.ErrorMessage)
	}
}
