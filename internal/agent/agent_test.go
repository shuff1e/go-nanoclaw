package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	mcRuntime "go-nanoclaw/internal/runtime"
)

func TestParsePlannerSteps(t *testing.T) {
	steps, err := parsePlannerSteps(`{
		"steps": [
			{"id":"step-1","title":"Analyze","prompt":"Analyze the request","depends_on":[],"retry_policy":"none","failure_strategy":"fail-plan"},
			{"id":"step-2","title":"Execute","prompt":"Do the work","depends_on":["step-1"],"retry_policy":"retry-on-transient","failure_strategy":"fail-plan"}
		]
	}`)
	if err != nil {
		t.Fatalf("parse planner steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %+v", steps)
	}
	if steps[1].DependsOn[0] != "step-1" || steps[1].RetryPolicy != "retry-on-transient" {
		t.Fatalf("expected dependency and retry metadata, got %+v", steps[1])
	}
}

func TestSpawnToolSchemaKeepsNameWithNanoClawDescription(t *testing.T) {
	cfg := config.NewConfig()
	a, err := NewAgent(cfg.Agents["main"], t.TempDir(), cfg, 0)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	var found *brain.ToolSchema
	schemas := a.Hands.GetToolSchemas([]string{"delegate_task"})
	for i := range schemas {
		if schemas[i].Name == "delegate_task" {
			found = &schemas[i]
			break
		}
	}
	if found == nil {
		t.Fatal("delegate_task schema missing")
	}
	if strings.Contains(found.Description, "Spawn a child "+"agent to handle a subtask") {
		t.Fatalf("delegate_task still uses retired description: %q", found.Description)
	}
	if !strings.Contains(found.Description, "Delegate") {
		t.Fatalf("delegate_task description should describe delegation, got %q", found.Description)
	}
}

func TestRetiredDelegationNameDoesNotExposePrimarySchema(t *testing.T) {
	cfg := config.NewConfig()
	a, err := NewAgent(cfg.Agents["main"], t.TempDir(), cfg, 0)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	schemas := a.Hands.GetToolSchemas([]string{"spawn" + "_" + "agent"})
	if len(schemas) != 0 {
		t.Fatalf("expected retired delegation name to expose no schemas, got %+v", schemas)
	}
}

func TestParsePlannerStepsRejectsInvalidDependency(t *testing.T) {
	_, err := parsePlannerSteps(`{
		"steps": [
			{"id":"step-1","title":"Execute","prompt":"Do the work","depends_on":["missing"]}
		]
	}`)
	if err == nil {
		t.Fatal("expected invalid dependency error")
	}
}

func TestValidateStepDependenciesRequiresCompletedDependencies(t *testing.T) {
	plan := &mcRuntime.TaskPlan{Steps: []mcRuntime.TaskStep{
		{ID: "step-1", Status: mcRuntime.StepFailed},
		{ID: "step-2", DependsOn: []string{"step-1"}, Status: mcRuntime.StepPending},
	}}
	if err := validateStepDependencies(plan, plan.Steps[1]); err == nil {
		t.Fatal("expected dependency validation failure")
	}

	plan.Steps[0].Status = mcRuntime.StepCompleted
	if err := validateStepDependencies(plan, plan.Steps[1]); err != nil {
		t.Fatalf("expected completed dependency to pass, got %v", err)
	}
}

func TestProcessPlannedExecutionRetriesTransientStep(t *testing.T) {
	cfg := config.NewConfig()
	workspace := t.TempDir()
	a, err := NewAgent(cfg.Agents["main"], workspace, cfg, 0)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	calls := 0
	a.Brain = testBrainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		calls++
		if calls == 1 {
			return nil, mcRuntime.Errorf(mcRuntime.CodeBrainFailed, "transient brain failure")
		}
		return &brain.BrainResponse{Text: "step complete", StopReason: "end_turn"}, nil
	})

	execCtx := mcRuntime.NewExecution("main", "s-retry", "test")
	execCtx.Mode = mcRuntime.ModePlanExecute
	execCtx.Plan = &mcRuntime.TaskPlan{
		RequestID:   execCtx.IDs.RequestID,
		TaskID:      execCtx.IDs.TaskID,
		SessionID:   execCtx.IDs.SessionID,
		AgentID:     execCtx.AgentID,
		Source:      execCtx.Source,
		Attempt:     1,
		Mode:        execCtx.Mode,
		Goal:        "retry one step",
		Status:      mcRuntime.StepPending,
		GeneratedAt: time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		Steps: []mcRuntime.TaskStep{
			{ID: "step-1", Title: "Transient step", Prompt: "Do work", RetryPolicy: "retry-on-transient", Status: mcRuntime.StepPending},
		},
	}

	result, err := a.ProcessExecution(context.Background(), execCtx, "retry one step")
	if err != nil {
		t.Fatalf("process planned execution: %v", err)
	}
	if !strings.Contains(result, "step complete") || calls != 2 {
		t.Fatalf("expected one retry and successful result, calls=%d result=%q", calls, result)
	}
	if execCtx.Plan.Status != mcRuntime.StepCompleted || execCtx.Plan.Steps[0].Status != mcRuntime.StepCompleted {
		t.Fatalf("expected completed plan after retry, got %+v", execCtx.Plan)
	}
}

func TestProcessPlannedExecutionSkipsBestEffortStep(t *testing.T) {
	cfg := config.NewConfig()
	workspace := t.TempDir()
	a, err := NewAgent(cfg.Agents["main"], workspace, cfg, 0)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	calls := 0
	a.Brain = testBrainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		calls++
		if calls == 1 {
			return nil, mcRuntime.Errorf(mcRuntime.CodeBrainFailed, "optional step failed")
		}
		return &brain.BrainResponse{Text: "required step complete", StopReason: "end_turn"}, nil
	})

	execCtx := mcRuntime.NewExecution("main", "s-skip", "test")
	execCtx.Mode = mcRuntime.ModePlanExecute
	execCtx.Plan = &mcRuntime.TaskPlan{
		RequestID:   execCtx.IDs.RequestID,
		TaskID:      execCtx.IDs.TaskID,
		SessionID:   execCtx.IDs.SessionID,
		AgentID:     execCtx.AgentID,
		Source:      execCtx.Source,
		Attempt:     1,
		Mode:        execCtx.Mode,
		Goal:        "skip optional step",
		Status:      mcRuntime.StepPending,
		GeneratedAt: time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		Steps: []mcRuntime.TaskStep{
			{ID: "step-1", Title: "Optional step", Prompt: "Optional work", FailureStrategy: "skip-step", Status: mcRuntime.StepPending},
			{ID: "step-2", Title: "Required step", Prompt: "Required work", Status: mcRuntime.StepPending},
		},
	}

	result, err := a.ProcessExecution(context.Background(), execCtx, "skip optional step")
	if err != nil {
		t.Fatalf("process planned execution: %v", err)
	}
	if !strings.Contains(result, "required step complete") || calls != 2 {
		t.Fatalf("expected optional skip and required success, calls=%d result=%q", calls, result)
	}
	if execCtx.Plan.Status != mcRuntime.StepCompleted {
		t.Fatalf("expected completed plan, got %+v", execCtx.Plan)
	}
	if execCtx.Plan.Steps[0].Status != mcRuntime.StepSkipped || execCtx.Plan.Steps[0].Checkpoint == "" {
		t.Fatalf("expected skipped optional step checkpoint, got %+v", execCtx.Plan.Steps[0])
	}
	if execCtx.Plan.Steps[1].Status != mcRuntime.StepCompleted {
		t.Fatalf("expected completed required step, got %+v", execCtx.Plan.Steps[1])
	}
}

func TestVerifiedPlanReplansOnceAfterVerificationFailure(t *testing.T) {
	cfg := config.NewConfig()
	workspace := t.TempDir()
	a, err := NewAgent(cfg.Agents["main"], workspace, cfg, 0)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	verificationCalls := 0
	a.Brain = testBrainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		if len(messages) == 1 && strings.Contains(messages[0].Content, "Verification task:") {
			verificationCalls++
			if verificationCalls == 1 {
				return &brain.BrainResponse{Text: "FAIL: missing corrected output", StopReason: "end_turn"}, nil
			}
			return &brain.BrainResponse{Text: "PASS: corrected", StopReason: "end_turn"}, nil
		}
		return &brain.BrainResponse{Text: "corrected output", StopReason: "end_turn"}, nil
	})

	execCtx := mcRuntime.NewExecution("main", "s-replan", "test")
	execCtx.Mode = mcRuntime.ModePlanExecuteVerify
	execCtx.Plan = &mcRuntime.TaskPlan{
		RequestID:   execCtx.IDs.RequestID,
		TaskID:      execCtx.IDs.TaskID,
		SessionID:   execCtx.IDs.SessionID,
		AgentID:     execCtx.AgentID,
		Source:      execCtx.Source,
		Attempt:     1,
		Mode:        execCtx.Mode,
		Goal:        "recover verification",
		Status:      mcRuntime.StepPending,
		GeneratedAt: time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		Steps: []mcRuntime.TaskStep{
			{ID: "step-1", Title: "Initial step", Prompt: "Do initial work", Status: mcRuntime.StepPending},
		},
	}

	_, err = a.ProcessExecution(context.Background(), execCtx, "recover verification")
	if err != nil {
		t.Fatalf("process verified plan with replan: %v", err)
	}
	if verificationCalls != 2 {
		t.Fatalf("expected two verification calls, got %d", verificationCalls)
	}
	var foundReplan bool
	for _, step := range execCtx.Plan.Steps {
		if step.ID == "replan-1" && step.Status == mcRuntime.StepCompleted {
			foundReplan = true
		}
	}
	if !foundReplan || execCtx.Plan.Status != mcRuntime.StepCompleted {
		t.Fatalf("expected completed replan step and plan, got %+v", execCtx.Plan)
	}
	if execCtx.Plan.Steps[len(execCtx.Plan.Steps)-1].ID != "quality-gate-2" {
		t.Fatalf("expected second quality gate after replan, got %+v", execCtx.Plan.Steps)
	}
}

func TestSpawnRespectsMaxSubagentsPerTask(t *testing.T) {
	cfg := config.NewConfig()
	def := cfg.Agents["main"]
	def.MaxSpawnDepth = 1
	def.MaxSubagents = 1

	a := &Agent{ID: "main", Config: cfg, AgentDef: def}
	execCtx := mcRuntime.NewExecution("main", "s1", "test")
	execCtx.Stats.SpawnCalls = 1

	result, err := a.Spawn(mcRuntime.WithExecutionContext(context.Background(), execCtx), "delegate this", "main")
	if err != nil {
		t.Fatalf("spawn should return user-visible error without Go error: %v", err)
	}
	if !strings.Contains(result, "Max subagents per task (1) reached") {
		t.Fatalf("expected quota error, got %q", result)
	}
}

type testBrainFunc func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error)

func (f testBrainFunc) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	return f(ctx, messages, systemPrompt, tools)
}

func TestSpawnRecordsFailedChildTaskInPlan(t *testing.T) {
	cfg := config.NewConfig()
	def := cfg.Agents["main"]
	def.MaxSpawnDepth = 1
	def.MaxSubagents = 4
	def.SubagentsAllow = []string{"missing"}

	a := &Agent{ID: "main", Config: cfg, AgentDef: def}
	execCtx := mcRuntime.NewExecution("main", "s1", "test")
	execCtx.Mode = mcRuntime.ModePlanExecute
	execCtx.Plan = mcRuntime.BuildTaskPlan(execCtx, "parent task")

	result, err := a.Spawn(mcRuntime.WithExecutionContext(context.Background(), execCtx), "delegate this", "missing")
	if err != nil {
		t.Fatalf("spawn should return user-visible error without Go error: %v", err)
	}
	if !strings.Contains(result, "agent 'missing' not defined") {
		t.Fatalf("expected missing agent error, got %q", result)
	}
	if len(execCtx.Plan.ChildTasks) != 1 {
		t.Fatalf("expected one child task, got %+v", execCtx.Plan.ChildTasks)
	}
	child := execCtx.Plan.ChildTasks[0]
	if child.AgentID != "missing" || child.Status != mcRuntime.StepFailed || child.Error == "" {
		t.Fatalf("expected failed child lineage, got %+v", child)
	}
}
