package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"go-nanoclaw/internal/hooks"
	mclog "go-nanoclaw/internal/log"
	mcRuntime "go-nanoclaw/internal/runtime"
)

// HandleInput processes user input through the specified agent.
func (gw *Gateway) HandleInput(ctx context.Context, text, agentID string) (string, error) {
	result, err := gw.HandleInputDetailed(ctx, text, agentID, agentID, "gateway")
	if err != nil {
		return "", err
	}
	return result.Response, nil
}

func (gw *Gateway) HandleInputDetailed(ctx context.Context, text, agentID, sessionID, source string) (*HandleResult, error) {
	return gw.HandleInputModeDetailed(ctx, text, agentID, sessionID, source, mcRuntime.ModeDirect)
}

func (gw *Gateway) HandleInputModeDetailed(ctx context.Context, text, agentID, sessionID, source string, mode mcRuntime.ExecutionMode) (*HandleResult, error) {
	reqNum := atomic.AddInt64(&gw.requestCount, 1)
	inputPreview := text
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:47] + "..."
	}
	mclog.Banner("📨", fmt.Sprintf("Gateway 收到请求 #%d", reqNum),
		fmt.Sprintf("Agent: %s | 输入: \"%s\"", agentID, inputPreview),
	)

	gw.Events.Emit(ctx, hooks.HookEvent{
		Type:    hooks.EventMessageReceived,
		Payload: map[string]any{"text": text, "agent_id": agentID},
		Source:  "gateway",
	})

	if strings.TrimSpace(text) == "" {
		err := mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing input text")
		atomic.AddInt64(&gw.errorCount, 1)
		return nil, err
	}

	execCtx := mcRuntime.NewExecution(agentID, sessionID, source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mode
	slog.Info("Gateway execution created",
		append(mcRuntime.LogAttrs(execCtx), "mode", execCtx.Mode)...,
	)
	return gw.executeInput(ctx, execCtx, text, agentID)
}

func (gw *Gateway) HandleInputModeAsyncDetailed(ctx context.Context, text, agentID, sessionID, source string, mode mcRuntime.ExecutionMode, maxRetries int) (*AsyncHandleResult, error) {
	reqNum := atomic.AddInt64(&gw.requestCount, 1)
	inputPreview := text
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:47] + "..."
	}
	mclog.Banner("📨", fmt.Sprintf("Gateway 收到异步请求 #%d", reqNum),
		fmt.Sprintf("Agent: %s | 输入: \"%s\"", agentID, inputPreview),
	)

	if strings.TrimSpace(text) == "" {
		err := mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing input text")
		atomic.AddInt64(&gw.errorCount, 1)
		return nil, err
	}

	execCtx := mcRuntime.NewExecution(agentID, sessionID, source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mode
	if mcRuntime.IsPlannedMode(execCtx.Mode) {
		execCtx.Plan = mcRuntime.BuildTaskPlan(execCtx, text)
		execCtx.Plan.MaxRetries = max(maxRetries, 0)
	}
	execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	slog.Info("Gateway async execution queued",
		append(mcRuntime.LogAttrs(execCtx), "mode", execCtx.Mode)...,
	)
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "queued", nil)
	gw.persistTrace(execCtx, "gateway", "queued", "queued", nil, nil)

	go func() {
		if _, err := gw.executeInput(context.Background(), execCtx, text, agentID); err != nil {
			slog.Warn("Async execution finished with error", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
			gw.maybeAutoRetry(agentID, execCtx, err)
		}
	}()

	return &AsyncHandleResult{
		RequestID:  execCtx.IDs.RequestID,
		TaskID:     execCtx.IDs.TaskID,
		AgentID:    execCtx.AgentID,
		Attempt:    1,
		MaxRetries: maxRetries,
		Status:     "queued",
	}, nil
}

func (gw *Gateway) executeInput(ctx context.Context, execCtx *mcRuntime.ExecutionContext, text, agentID string) (*HandleResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = mcRuntime.WithExecutionContext(runCtx, execCtx)
	if execCtx.OnPlanUpdate == nil {
		execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	}
	if execCtx.OnRuntimeEvent == nil {
		execCtx.OnRuntimeEvent = func(span, event, status string, metadata map[string]any, err error) {
			gw.persistTrace(execCtx, span, event, status, metadata, err)
		}
	}
	gw.registerTask(execCtx, cancel)
	defer gw.unregisterTask(execCtx)
	gw.persistExecution(execCtx, "started", nil)
	gw.persistTrace(execCtx, "gateway", "started", "started", nil, nil)

	result, err := gw.runtime.Process(ctx, execCtx, text, agentID)
	if err != nil {
		if mcRuntime.CodeOf(err) == mcRuntime.CodeApprovalRequired {
			gw.persistPlan(execCtx)
			gw.persistExecution(execCtx, "awaiting_approval", err)
			gw.persistTrace(execCtx, "gateway", "awaiting_approval", "awaiting_approval", nil, err)
			return nil, err
		}
		if mcRuntime.CodeOf(err) == mcRuntime.CodeCancelled {
			gw.persistPlan(execCtx)
			gw.persistExecution(execCtx, "cancelled", err)
			gw.persistTrace(execCtx, "gateway", "cancelled", "cancelled", nil, err)
			return nil, err
		}
		atomic.AddInt64(&gw.errorCount, 1)
		gw.persistPlan(execCtx)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "gateway", "failed", "failed", nil, err)
		return nil, err
	}

	gw.deliver(result.TargetAgentID, result.Response)
	gw.saveSession(result.TargetAgentID, text, result.Response)

	gw.Events.Emit(ctx, hooks.HookEvent{
		Type:    hooks.EventMessageSent,
		Payload: map[string]any{"agent_id": result.TargetAgentID, "response_length": len(result.Response), "request_id": execCtx.IDs.RequestID},
		Source:  "gateway",
	})
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "completed", nil)
	gw.persistTrace(execCtx, "gateway", "completed", "completed", map[string]any{"response_length": len(result.Response)}, nil)

	return &HandleResult{
		RequestID: execCtx.IDs.RequestID,
		TaskID:    execCtx.IDs.TaskID,
		AgentID:   result.TargetAgentID,
		Response:  result.Response,
	}, nil
}

func (gw *Gateway) RetryTaskAsync(ctx context.Context, agentID, requestID, taskID string, maxRetriesOverride int) (*AsyncHandleResult, error) {
	planRecord, err := gw.findPlanRecord(agentID, requestID, taskID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(planRecord.Goal) == "" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "plan has no goal to retry")
	}

	execCtx := mcRuntime.NewExecution(agentID, planRecord.SessionID, planRecord.Source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mcRuntime.ExecutionMode(planRecord.Mode)
	if execCtx.Mode == "" {
		execCtx.Mode = mcRuntime.ModePlanExecute
	}
	execCtx.Plan = runtimePlanFromRecord(execCtx, planRecord)
	if maxRetriesOverride >= 0 {
		execCtx.Plan.MaxRetries = maxRetriesOverride
	}
	execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "queued", nil)
	gw.persistTrace(execCtx, "gateway", "queued", "queued", nil, nil)

	go func(goal string) {
		if _, runErr := gw.executeInput(context.Background(), execCtx, goal, agentID); runErr != nil {
			slog.Warn("Retried async execution finished with error", append([]any{"error", runErr}, mcRuntime.LogAttrs(execCtx)...)...)
			gw.maybeAutoRetry(agentID, execCtx, runErr)
		}
	}(planRecord.Goal)

	return &AsyncHandleResult{
		RequestID:  execCtx.IDs.RequestID,
		TaskID:     execCtx.IDs.TaskID,
		AgentID:    execCtx.AgentID,
		Attempt:    execCtx.Plan.Attempt,
		MaxRetries: execCtx.Plan.MaxRetries,
		Status:     "queued",
	}, nil
}

func (gw *Gateway) maybeAutoRetry(agentID string, execCtx *mcRuntime.ExecutionContext, err error) {
	if execCtx == nil || execCtx.Plan == nil {
		return
	}
	if !mcRuntime.Retryable(err) {
		return
	}
	if execCtx.Plan.MaxRetries <= 0 {
		return
	}
	if execCtx.Plan.Attempt > execCtx.Plan.MaxRetries {
		return
	}
	slog.Info("Scheduling automatic retry",
		append(mcRuntime.LogAttrs(execCtx),
			"attempt", execCtx.Plan.Attempt,
			"max_retries", execCtx.Plan.MaxRetries,
			"delay_ms", autoRetryDelay.Milliseconds(),
		)...,
	)
	go func() {
		timer := time.NewTimer(autoRetryDelay)
		defer timer.Stop()
		<-timer.C
		if _, retryErr := gw.RetryTaskAsync(context.Background(), agentID, execCtx.IDs.RequestID, execCtx.IDs.TaskID, execCtx.Plan.MaxRetries); retryErr != nil {
			slog.Warn("Automatic retry scheduling failed", append([]any{"error", retryErr}, mcRuntime.LogAttrs(execCtx)...)...)
		}
	}()
}

func (gw *Gateway) registerTask(execCtx *mcRuntime.ExecutionContext, cancel context.CancelFunc) {
	if execCtx == nil || cancel == nil {
		return
	}
	gw.taskMu.Lock()
	defer gw.taskMu.Unlock()
	if execCtx.IDs.RequestID != "" {
		gw.runningTasks["request:"+execCtx.IDs.RequestID] = cancel
	}
	if execCtx.IDs.TaskID != "" {
		gw.runningTasks["task:"+execCtx.IDs.TaskID] = cancel
	}
}

func (gw *Gateway) unregisterTask(execCtx *mcRuntime.ExecutionContext) {
	if execCtx == nil {
		return
	}
	gw.taskMu.Lock()
	defer gw.taskMu.Unlock()
	if execCtx.IDs.RequestID != "" {
		delete(gw.runningTasks, "request:"+execCtx.IDs.RequestID)
	}
	if execCtx.IDs.TaskID != "" {
		delete(gw.runningTasks, "task:"+execCtx.IDs.TaskID)
	}
}

func (gw *Gateway) CancelTask(requestID, taskID string) error {
	gw.taskMu.RLock()
	defer gw.taskMu.RUnlock()
	if requestID != "" {
		if cancel := gw.runningTasks["request:"+requestID]; cancel != nil {
			cancel()
			return nil
		}
	}
	if taskID != "" {
		if cancel := gw.runningTasks["task:"+taskID]; cancel != nil {
			cancel()
			return nil
		}
	}
	return mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "task not running")
}

func (gw *Gateway) runningTaskCount() int {
	gw.taskMu.RLock()
	defer gw.taskMu.RUnlock()
	return len(gw.runningTasks) / 2
}
