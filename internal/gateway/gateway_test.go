package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/hooks"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

func TestHandleInputDetailedRejectsEmptyText(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	_, err := gw.HandleInputDetailed(context.Background(), "   ", "main", "main", "test")
	if err == nil {
		t.Fatal("expected error for blank text")
	}
}

func TestFilteredExecutionLogs(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r1",
		SessionID: "s1",
		Status:    "failed",
		Source:    "http",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r2",
		SessionID: "s2",
		Status:    "completed",
		Source:    "cli",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}

	logs, err := gw.FilteredExecutionLogs("main", 10, ExecutionLogFilter{SessionID: "s1", Status: "failed"})
	if err != nil {
		t.Fatalf("filter execution logs: %v", err)
	}
	if len(logs) != 1 || logs[0].RequestID != "r1" {
		t.Fatalf("unexpected filtered execution logs: %+v", logs)
	}
}

func TestMetricsIncludesCoreCounters(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)
	gw.running = true

	metrics := gw.Metrics()
	if !strings.Contains(metrics, "nanoclaw_gateway_up 1") {
		t.Fatalf("expected gateway_up metric, got %s", metrics)
	}
	if !strings.Contains(metrics, "nanoclaw_requests_total 0") {
		t.Fatalf("expected requests_total metric, got %s", metrics)
	}
	if !strings.Contains(metrics, "nanoclaw_running_tasks 0") {
		t.Fatalf("expected running_tasks metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_agent_context_messages{agent_id="main"}`) {
		t.Fatalf("expected agent context metric, got %s", metrics)
	}
}

func TestGatewayAppliesExecutionBudgetConfig(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.MaxWallClockSec = 30
	cfg.MaxToolRounds = 3
	cfg.MaxToolCalls = 5
	cfg.MaxToolOutputBytes = 1024
	gw := NewGateway(cfg)

	execCtx := mcRuntime.NewExecution("main", "s-budget", "test")
	gw.applyExecutionBudget(execCtx)
	if execCtx.Budget.MaxWallClock != 30*time.Second || execCtx.Budget.MaxToolRounds != 3 || execCtx.Budget.MaxToolCalls != 5 || execCtx.Budget.MaxToolOutputBytes != 1024 {
		t.Fatalf("unexpected execution budget: %+v", execCtx.Budget)
	}
	if !execCtx.Deadline.Equal(execCtx.StartedAt.Add(30 * time.Second)) {
		t.Fatalf("expected deadline from configured budget, got %s started %s", execCtx.Deadline, execCtx.StartedAt)
	}
}

func TestRetiredScheduleToolNamesDoNotExposePrimarySchemas(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)
	gw.registerCronTools("main")
	a, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}

	retiredNames := []string{
		"cron" + "_" + "add",
		"cron" + "_" + "list",
	}
	for _, name := range retiredNames {
		schemas := a.Hands.GetToolSchemas([]string{name})
		if len(schemas) != 0 {
			t.Fatalf("expected retired schedule name %s to expose no schemas, got %+v", name, schemas)
		}
	}
}

func TestHealthIncludesConfigVersionAndHash(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.ConfigVersion = "release-1"
	gw := NewGateway(cfg)

	health := gw.Health()
	if health["config_version"] != "release-1" {
		t.Fatalf("expected config version in health, got %+v", health)
	}
	if health["config_hash"] == "" {
		t.Fatalf("expected config hash in health, got %+v", health)
	}

	ready := gw.Readiness()
	if ready.Checks["config_version"] != "release-1" || ready.Checks["config_hash"] == "" {
		t.Fatalf("expected config version/hash in readiness, got %+v", ready)
	}
}

func TestReadinessChecksStoreHealth(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)
	gw.running = true

	ready := gw.Readiness()
	if ready.Status != "ready" || ready.Checks["store_ready"] != true {
		t.Fatalf("expected ready store, got %+v", ready)
	}

	gw.store = failingStore{Store: gw.store}
	notReady := gw.Readiness()
	if notReady.Status != "not_ready" || notReady.Checks["store_ready"] != false || notReady.Checks["store_error"] == "" {
		t.Fatalf("expected failing store readiness, got %+v", notReady)
	}
}

func TestHandleInputPersistsTraceEvents(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		return &brain.BrainResponse{Text: "ok", StopReason: "end_turn"}, nil
	})

	result, err := gw.HandleInputDetailed(context.Background(), "hello", "main", "s-trace", "test")
	if err != nil {
		t.Fatalf("handle input: %v", err)
	}

	events, err := gw.TraceEvents("main", 20, TraceEventFilter{RequestID: result.RequestID})
	if err != nil {
		t.Fatalf("trace events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected started/completed trace events, got %+v", events)
	}
	if events[0].TraceID == "" {
		t.Fatalf("expected trace id on event, got %+v", events[0])
	}
	var completed bool
	var thinking bool
	for _, event := range events {
		if event.Event == "completed" && event.Status == "completed" {
			completed = true
		}
		if event.Event == "thinking" && event.Status == "thinking" {
			thinking = true
		}
	}
	if !completed {
		t.Fatalf("expected completed trace event, got %+v", events)
	}
	if !thinking {
		t.Fatalf("expected thinking trace event, got %+v", events)
	}
}

type failingStore struct {
	store.Store
}

func (f failingStore) HealthCheck() error {
	return fmt.Errorf("store unavailable")
}

func TestGatewayEmitsRuntimeEvents(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	seen := make(chan mcRuntime.RuntimeEvent, 1)
	gw.RegisterHook(hooks.Hook{
		Name:      "capture-runtime",
		EventType: hooks.EventRuntime,
		Handler: func(ctx context.Context, event hooks.HookEvent) error {
			ev, ok := event.Payload["event"].(mcRuntime.RuntimeEvent)
			if !ok {
				t.Fatalf("expected runtime event payload, got %+v", event.Payload)
			}
			seen <- ev
			return nil
		},
	})

	execCtx := mcRuntime.NewExecution("main", "s-runtime", "test")
	gw.persistTrace(execCtx, "gateway", "started", "started", map[string]any{"phase": "test"}, nil)

	select {
	case ev := <-seen:
		if ev.Type != mcRuntime.EventRunStarted {
			t.Fatalf("expected run started event, got %+v", ev)
		}
		if ev.RequestID != execCtx.IDs.RequestID || ev.TraceID != execCtx.IDs.TraceID || ev.SessionID != "s-runtime" {
			t.Fatalf("expected execution IDs on runtime event, got %+v", ev)
		}
		if ev.Metadata["phase"] != "test" || ev.Span != "gateway" || ev.Status != "started" {
			t.Fatalf("unexpected runtime event details: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected runtime event")
	}
}

func TestDecideApprovalExpiresStaleApproval(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.Agents["main"].ToolPolicies.ApprovalTimeoutMin = 1
	gw := NewGateway(cfg)

	requestedAt := time.Now().UTC().Add(-2 * time.Minute)
	if err := gw.Store().SaveApprovalRecord("main", store.ApprovalRecord{
		ApprovalID:  "approval-stale",
		TraceID:     "trace-stale",
		RequestID:   "req-stale",
		SessionID:   "session-stale",
		TaskID:      "task-stale",
		AgentID:     "main",
		ToolName:    "write_workspace_file",
		Arguments:   map[string]any{"path": "stale.txt", "content": "stale"},
		Status:      "pending",
		RequestedAt: requestedAt,
	}); err != nil {
		t.Fatalf("save approval record: %v", err)
	}

	_, err := gw.DecideApproval("main", "approval-stale", "approved", "tester")
	if mcRuntime.CodeOf(err) != mcRuntime.CodeApprovalRequired {
		t.Fatalf("expected expired approval error, got %v", err)
	}

	records, err := gw.ApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("approval records: %v", err)
	}
	if len(records) != 2 || records[1].Status != "expired" || records[1].DecidedBy != "tester" {
		t.Fatalf("expected appended expired approval, got %+v", records)
	}
	tasks, err := gw.TaskViews("main", 10, ExecutionLogFilter{RequestID: "req-stale"})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("expected expired approval to fail task, got %+v", tasks)
	}
	traces, err := gw.TraceEvents("main", 10, TraceEventFilter{RequestID: "req-stale", Event: "expired"})
	if err != nil {
		t.Fatalf("trace events: %v", err)
	}
	if len(traces) != 1 || traces[0].Status != "expired" {
		t.Fatalf("expected expired approval trace, got %+v", traces)
	}
}

func TestMetricsIncludesExecutionAndToolAggregates(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)
	gw.running = true

	now := time.Now().UTC()
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:      "main",
		RequestID:    "r1",
		Status:       "completed",
		Provider:     "anthropic",
		Model:        "claude-test",
		InputTokens:  120,
		OutputTokens: 30,
		BrainCalls:   2,
		ToolCalls:    1,
		StartedAt:    now,
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	if err := gw.Store().SaveToolAuditLog(store.ToolAuditLog{
		AgentID:    "main",
		ToolName:   "run_command",
		Status:     "denied",
		OccurredAt: now,
	}); err != nil {
		t.Fatalf("save tool audit log: %v", err)
	}
	if err := gw.Store().SavePlan("main", store.PlanRecord{
		AgentID:          "main",
		RequestID:        "r2",
		TaskID:           "t2",
		RetryOfRequestID: "r1",
		RetryOfTaskID:    "t1",
		Attempt:          2,
		MaxRetries:       1,
		Mode:             "plan_execute",
		Goal:             "retry task",
		Status:           "completed",
		GeneratedAt:      now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r2",
		TaskID:    "t2",
		Status:    "completed",
		StartedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("save retry execution log: %v", err)
	}

	metrics := gw.Metrics()
	if !strings.Contains(metrics, `nanoclaw_brain_calls_total{agent_id="main",provider="anthropic",model="claude-test"} 2`) {
		t.Fatalf("expected brain call metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_input_tokens_total{agent_id="main",provider="anthropic",model="claude-test"} 120`) {
		t.Fatalf("expected input token metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_tool_audit_total{agent_id="main",tool_name="run_command",status="denied"} 1`) {
		t.Fatalf("expected tool audit metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_retried_tasks_total{agent_id="main"} 1`) {
		t.Fatalf("expected retried task metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_task_attempt{agent_id="main",attempt="2"} 1`) {
		t.Fatalf("expected task attempt metric, got %s", metrics)
	}
	if !strings.Contains(metrics, `nanoclaw_auto_retryable_tasks{agent_id="main"} 1`) {
		t.Fatalf("expected auto retryable metric, got %s", metrics)
	}
}

func TestFilteredExecutionLogsByTimeRange(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	base := time.Now().UTC()
	for i, ts := range []time.Time{
		base.Add(-2 * time.Hour),
		base.Add(-1 * time.Hour),
		base,
	} {
		if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
			AgentID:   "main",
			RequestID: fmt.Sprintf("r%d", i+1),
			Status:    "completed",
			Source:    "http",
			StartedAt: ts,
		}); err != nil {
			t.Fatalf("save execution log: %v", err)
		}
	}

	logs, err := gw.FilteredExecutionLogs("main", 10, ExecutionLogFilter{
		Since: base.Add(-90 * time.Minute),
		Until: base.Add(-30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("filter execution logs by time: %v", err)
	}
	if len(logs) != 1 || logs[0].RequestID != "r2" {
		t.Fatalf("unexpected filtered time range logs: %+v", logs)
	}
}

func TestTaskViewsAggregateExecutionLogs(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	base := time.Now().UTC()
	for _, log := range []store.ExecutionLog{
		{AgentID: "main", RequestID: "r1", TaskID: "t1", SessionID: "s1", Source: "http", Status: "started", StartedAt: base},
		{AgentID: "main", RequestID: "r1", TaskID: "t1", SessionID: "s1", Source: "http", Status: "completed", StartedAt: base.Add(2 * time.Second)},
		{AgentID: "main", RequestID: "r2", TaskID: "t2", SessionID: "s2", Source: "cli", Status: "started", StartedAt: base.Add(1 * time.Second)},
		{AgentID: "main", RequestID: "r2", TaskID: "t2", SessionID: "s2", Source: "cli", Status: "failed", Error: "boom", StartedAt: base.Add(3 * time.Second)},
	} {
		if err := gw.Store().SaveExecutionLog(log); err != nil {
			t.Fatalf("save execution log: %v", err)
		}
	}

	tasks, err := gw.TaskViews("main", 10, ExecutionLogFilter{})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 task views, got %+v", tasks)
	}
	if tasks[0].RequestID != "r2" || tasks[0].Status != "failed" || tasks[0].EventCount != 2 {
		t.Fatalf("unexpected latest failed task: %+v", tasks[0])
	}
	if tasks[1].RequestID != "r1" || tasks[1].Status != "completed" || tasks[1].EventCount != 2 {
		t.Fatalf("unexpected completed task: %+v", tasks[1])
	}
}

func TestSummarizeTaskViews(t *testing.T) {
	gw := &Gateway{}
	summary := gw.SummarizeTaskViews([]TaskView{
		{Status: "completed", Source: "http", Attempt: 1},
		{Status: "failed", Source: "http", Attempt: 2, RetryOfRequestID: "r1", MaxRetries: 1},
		{Status: "failed", Source: "cli", Attempt: 3, RetryOfTaskID: "t1"},
	})

	if summary.Total != 3 {
		t.Fatalf("expected total 3, got %+v", summary)
	}
	if summary.ByStatus["failed"] != 2 || summary.BySource["http"] != 2 {
		t.Fatalf("unexpected task summary: %+v", summary)
	}
	if summary.ByAttempt["2"] != 1 || summary.MaxAttempt != 3 || summary.RetriedTotal != 2 || summary.AutoRetryable != 1 {
		t.Fatalf("unexpected retry summary: %+v", summary)
	}
}

func TestTaskViewsIncludePersistedPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	now := time.Now().UTC()
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r-plan",
		TaskID:    "t-plan",
		SessionID: "s-plan",
		Source:    "http",
		Status:    "completed",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	if err := gw.Store().SavePlan("main", store.PlanRecord{
		AgentID:     "main",
		RequestID:   "r-plan",
		TaskID:      "t-plan",
		SessionID:   "s-plan",
		Mode:        "plan_execute",
		Goal:        "Ship feature",
		Status:      "completed",
		GeneratedAt: now,
		UpdatedAt:   now,
		Steps: []store.PlanStepRecord{
			{ID: "step-1", Title: "Analyze", Status: "completed"},
		},
	}); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	tasks, err := gw.TaskViews("main", 10, ExecutionLogFilter{})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task view, got %+v", tasks)
	}
	if tasks[0].Mode != "plan_execute" || tasks[0].Plan == nil || tasks[0].Plan.RequestID != "r-plan" {
		t.Fatalf("expected task plan attached, got %+v", tasks[0])
	}
}

func TestHandleInputModeDetailedPersistsPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		return &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		}, nil
	})

	result, err := gw.HandleInputModeDetailed(context.Background(), "Analyze this. Then implement it.", "main", "s-plan", "test", mcRuntime.ModePlanExecute)
	if err != nil {
		t.Fatalf("handle input with mode: %v", err)
	}
	if result.RequestID == "" {
		t.Fatalf("expected request id in result")
	}

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("expected persisted plan")
	}
	last := plans[len(plans)-1]
	if last.RequestID != result.RequestID || last.Mode != "plan_execute" || len(last.Steps) < 2 {
		t.Fatalf("unexpected persisted plan: %+v", last)
	}
	if last.Steps[0].Checkpoint == "" {
		t.Fatalf("expected completed step checkpoint, got %+v", last.Steps[0])
	}

	events, err := gw.TraceEvents("main", 50, TraceEventFilter{RequestID: result.RequestID})
	if err != nil {
		t.Fatalf("load trace events: %v", err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Event+"/"+event.Status] = true
	}
	if !seen["planning/planning"] || !seen["step_started/running_tool"] || !seen["step_completed/completed"] {
		t.Fatalf("expected planned runtime trace events, got %+v", events)
	}
}

func TestHandleInputModeDetailedPersistsVerifiedPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		text := "ok"
		if len(messages) == 1 && strings.Contains(messages[0].Content, "Verification task:") {
			text = "PASS: verified"
		}
		return &brain.BrainResponse{
			Text:       text,
			StopReason: "end_turn",
		}, nil
	})

	result, err := gw.HandleInputModeDetailed(context.Background(), "Analyze this. Then implement it.", "main", "s-plan", "test", mcRuntime.ModePlanExecuteVerify)
	if err != nil {
		t.Fatalf("handle verified plan: %v", err)
	}

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("expected persisted plan")
	}
	last := plans[len(plans)-1]
	if last.RequestID != result.RequestID || last.Mode != "plan_execute_verify" || last.Status != "completed" {
		t.Fatalf("unexpected verified plan: %+v", last)
	}
	if len(last.Steps) == 0 || last.Steps[len(last.Steps)-1].ID != "quality-gate" || last.Steps[len(last.Steps)-1].Status != "completed" {
		t.Fatalf("expected completed quality gate, got %+v", last.Steps)
	}
}

func TestHandleInputModeDetailedFailsVerifiedPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		text := "ok"
		if len(messages) == 1 && strings.Contains(messages[0].Content, "Verification task:") {
			text = "FAIL: missing final check"
		}
		return &brain.BrainResponse{
			Text:       text,
			StopReason: "end_turn",
		}, nil
	})

	_, err = gw.HandleInputModeDetailed(context.Background(), "Analyze this. Then implement it.", "main", "s-plan", "test", mcRuntime.ModePlanExecuteVerify)
	if err == nil {
		t.Fatal("expected verified plan failure")
	}

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	last := plans[len(plans)-1]
	if last.Status != "failed" || last.Steps[len(last.Steps)-1].ID != "quality-gate-2" || last.Steps[len(last.Steps)-1].Status != "failed" {
		t.Fatalf("expected failed quality gate, got %+v", last)
	}
}

func TestPersistPlanIncludesChildTasks(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	execCtx := mcRuntime.NewExecution("main", "s-child", "test")
	execCtx.Mode = mcRuntime.ModePlanExecute
	execCtx.Plan = mcRuntime.BuildTaskPlan(execCtx, "parent task")
	execCtx.Plan.ChildTasks = append(execCtx.Plan.ChildTasks, mcRuntime.ChildTask{
		ID:      "child-1",
		AgentID: "worker",
		Prompt:  "do child work",
		Status:  mcRuntime.StepCompleted,
		Result:  "done",
	})
	gw.persistPlan(execCtx)

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) != 1 || len(plans[0].ChildTasks) != 1 {
		t.Fatalf("expected persisted child task, got %+v", plans)
	}
	child := plans[0].ChildTasks[0]
	if child.ID != "child-1" || child.AgentID != "worker" || child.Status != "completed" {
		t.Fatalf("unexpected child task record: %+v", child)
	}
}

func TestCancelTaskCancelsRunningExecution(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	requestIDCh := make(chan string, 1)
	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		execCtx := mcRuntime.FromContext(ctx)
		if execCtx != nil {
			select {
			case requestIDCh <- execCtx.IDs.RequestID:
			default:
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})

	errCh := make(chan error, 1)
	go func() {
		_, runErr := gw.HandleInputModeDetailed(context.Background(), "long running", "main", "s-cancel", "test", mcRuntime.ModeDirect)
		errCh <- runErr
	}()

	requestID := <-requestIDCh
	if requestID == "" {
		t.Fatal("expected running request id")
	}
	if err := gw.CancelTask(requestID, ""); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	runErr := <-errCh
	if mcRuntime.CodeOf(runErr) != mcRuntime.CodeCancelled {
		t.Fatalf("expected cancelled error, got %v", runErr)
	}

	tasks, err := gw.TaskViews("main", 10, ExecutionLogFilter{RequestID: requestID})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "cancelled" {
		t.Fatalf("expected cancelled task view, got %+v", tasks)
	}
}

type brainFunc func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error)

func (f brainFunc) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	return f(ctx, messages, systemPrompt, tools)
}
