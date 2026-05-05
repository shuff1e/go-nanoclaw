package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go-nanoclaw/internal/hooks"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

func (gw *Gateway) persistExecution(execCtx *mcRuntime.ExecutionContext, status string, err error) {
	if gw.store == nil || execCtx == nil {
		return
	}
	log := store.ExecutionLog{
		TraceID:      execCtx.IDs.TraceID,
		RequestID:    execCtx.IDs.RequestID,
		SessionID:    execCtx.IDs.SessionID,
		TaskID:       execCtx.IDs.TaskID,
		AgentID:      execCtx.AgentID,
		Source:       execCtx.Source,
		Status:       status,
		Provider:     execCtx.Provider,
		Model:        execCtx.Model,
		InputTokens:  execCtx.Stats.InputTokens,
		OutputTokens: execCtx.Stats.OutputTokens,
		BrainCalls:   execCtx.Stats.BrainCalls,
		ToolCalls:    execCtx.Stats.ToolCalls,
		StartedAt:    execCtx.StartedAt,
	}
	if err != nil {
		log.Error = err.Error()
	}
	if saveErr := gw.store.SaveExecutionLog(log); saveErr != nil {
		slog.Error("Persist execution log failed", append([]any{"error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func (gw *Gateway) persistTrace(execCtx *mcRuntime.ExecutionContext, span, event, status string, metadata map[string]any, err error) {
	if gw.store == nil || execCtx == nil {
		return
	}
	runtimeEvent := mcRuntime.NewRuntimeEvent(execCtx, runtimeEventType(event, status), span, status, metadata, err)
	record := store.TraceEvent{
		TraceID:   execCtx.IDs.TraceID,
		RequestID: execCtx.IDs.RequestID,
		SessionID: execCtx.IDs.SessionID,
		TaskID:    execCtx.IDs.TaskID,
		AgentID:   execCtx.AgentID,
		Source:    execCtx.Source,
		Span:      span,
		Event:     event,
		Status:    status,
		Metadata:  metadata,
		At:        runtimeEvent.At,
	}
	if err != nil {
		record.Error = err.Error()
	}
	if saveErr := gw.store.SaveTraceEvent(execCtx.AgentID, record); saveErr != nil {
		slog.Error("Persist trace event failed", append([]any{"error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
	if gw.Events != nil {
		gw.Events.Emit(context.Background(), hooks.HookEvent{
			Type:    hooks.EventRuntime,
			Source:  "runtime",
			Payload: map[string]any{"event": runtimeEvent},
		})
	}
}

func runtimeEventType(event, status string) mcRuntime.EventType {
	switch event {
	case "queued":
		return mcRuntime.EventRunQueued
	case "started":
		return mcRuntime.EventRunStarted
	case "thinking":
		return mcRuntime.EventRunThinking
	case "planning":
		return mcRuntime.EventRunPlanning
	case "replanning":
		return mcRuntime.EventRunReplanning
	case "step_started":
		return mcRuntime.EventRunStepStarted
	case "step_completed":
		return mcRuntime.EventRunStepCompleted
	case "step_failed":
		return mcRuntime.EventRunStepFailed
	case "step_skipped":
		return mcRuntime.EventRunStepSkipped
	case "verifying":
		return mcRuntime.EventRunVerifying
	case "tool_started":
		return mcRuntime.EventToolStarted
	case "tool_completed":
		return mcRuntime.EventToolCompleted
	case "tool_failed":
		return mcRuntime.EventToolFailed
	case "completed":
		return mcRuntime.EventRunCompleted
	case "failed":
		return mcRuntime.EventRunFailed
	case "cancelled":
		return mcRuntime.EventRunCancelled
	case "awaiting_approval":
		return mcRuntime.EventRunAwaitingApproval
	case "rejected":
		return mcRuntime.EventApprovalRejected
	case "expired":
		return mcRuntime.EventApprovalExpired
	case "approved_tool_completed":
		return mcRuntime.EventApprovalToolCompleted
	case "approved_tool_failed":
		return mcRuntime.EventApprovalToolFailed
	default:
		if status == "failed" {
			return mcRuntime.EventRunFailed
		}
		return mcRuntime.EventType(event)
	}
}

func (gw *Gateway) persistPlan(execCtx *mcRuntime.ExecutionContext) {
	if gw.store == nil || execCtx == nil || execCtx.Plan == nil {
		return
	}
	plan := store.PlanRecord{
		RequestID:        execCtx.Plan.RequestID,
		TaskID:           execCtx.Plan.TaskID,
		SessionID:        execCtx.Plan.SessionID,
		AgentID:          execCtx.Plan.AgentID,
		Source:           execCtx.Plan.Source,
		RetryOfRequestID: execCtx.Plan.RetryOfRequestID,
		RetryOfTaskID:    execCtx.Plan.RetryOfTaskID,
		Attempt:          execCtx.Plan.Attempt,
		MaxRetries:       execCtx.Plan.MaxRetries,
		Mode:             string(execCtx.Plan.Mode),
		Goal:             execCtx.Plan.Goal,
		Status:           string(execCtx.Plan.Status),
		Summary:          execCtx.Plan.Summary,
		GeneratedAt:      execCtx.Plan.GeneratedAt,
		UpdatedAt:        execCtx.Plan.UpdatedAt,
		Steps:            make([]store.PlanStepRecord, 0, len(execCtx.Plan.Steps)),
		ChildTasks:       make([]store.PlanChildTaskRecord, 0, len(execCtx.Plan.ChildTasks)),
	}
	for _, step := range execCtx.Plan.Steps {
		plan.Steps = append(plan.Steps, store.PlanStepRecord{
			ID:              step.ID,
			Title:           step.Title,
			Prompt:          step.Prompt,
			DependsOn:       step.DependsOn,
			RetryPolicy:     step.RetryPolicy,
			FailureStrategy: step.FailureStrategy,
			Checkpoint:      step.Checkpoint,
			Status:          string(step.Status),
			Result:          step.Result,
			Error:           step.Error,
			StartedAt:       step.StartedAt,
			CompletedAt:     step.CompletedAt,
		})
	}
	for _, child := range execCtx.Plan.ChildTasks {
		plan.ChildTasks = append(plan.ChildTasks, store.PlanChildTaskRecord{
			ID:          child.ID,
			AgentID:     child.AgentID,
			Prompt:      child.Prompt,
			Status:      string(child.Status),
			Result:      child.Result,
			Error:       child.Error,
			StartedAt:   child.StartedAt,
			CompletedAt: child.CompletedAt,
		})
	}
	if err := gw.store.SavePlan(execCtx.AgentID, plan); err != nil {
		slog.Error("Persist plan failed", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func runtimePlanFromRecord(execCtx *mcRuntime.ExecutionContext, record *store.PlanRecord) *mcRuntime.TaskPlan {
	if record == nil {
		return nil
	}
	plan := &mcRuntime.TaskPlan{
		RequestID:        execCtx.IDs.RequestID,
		TaskID:           execCtx.IDs.TaskID,
		SessionID:        execCtx.IDs.SessionID,
		AgentID:          execCtx.AgentID,
		Source:           execCtx.Source,
		RetryOfRequestID: record.RequestID,
		RetryOfTaskID:    record.TaskID,
		Attempt:          max(record.Attempt+1, 2),
		MaxRetries:       record.MaxRetries,
		Mode:             mcRuntime.ExecutionMode(record.Mode),
		Goal:             record.Goal,
		Status:           mcRuntime.StepStatus(record.Status),
		Summary:          record.Summary,
		GeneratedAt:      time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
		Steps:            make([]mcRuntime.TaskStep, 0, len(record.Steps)),
		ChildTasks:       make([]mcRuntime.ChildTask, 0, len(record.ChildTasks)),
	}
	for _, step := range record.Steps {
		plan.Steps = append(plan.Steps, mcRuntime.TaskStep{
			ID:              step.ID,
			Title:           step.Title,
			Prompt:          step.Prompt,
			DependsOn:       step.DependsOn,
			RetryPolicy:     step.RetryPolicy,
			FailureStrategy: step.FailureStrategy,
			Checkpoint:      step.Checkpoint,
			Status:          mcRuntime.StepStatus(step.Status),
			Result:          step.Result,
			Error:           step.Error,
			StartedAt:       step.StartedAt,
			CompletedAt:     step.CompletedAt,
		})
	}
	for _, child := range record.ChildTasks {
		plan.ChildTasks = append(plan.ChildTasks, mcRuntime.ChildTask{
			ID:          child.ID,
			AgentID:     child.AgentID,
			Prompt:      child.Prompt,
			Status:      mcRuntime.StepStatus(child.Status),
			Result:      child.Result,
			Error:       child.Error,
			StartedAt:   child.StartedAt,
			CompletedAt: child.CompletedAt,
		})
	}
	return plan
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func round(f float64, places int) float64 {
	shift := 1.0
	for i := 0; i < places; i++ {
		shift *= 10
	}
	return float64(int(f*shift+0.5)) / shift
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func metricKey(agentID, provider, model string) string {
	return strings.Join([]string{agentID, provider, model}, "\x1f")
}

func splitMetricKey(key string) (string, string, string) {
	parts := strings.SplitN(key, "\x1f", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func toolMetricKey(agentID, toolName, status string) string {
	return strings.Join([]string{agentID, toolName, status}, "\x1f")
}

func splitToolMetricKey(key string) (string, string, string) {
	parts := strings.SplitN(key, "\x1f", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}
