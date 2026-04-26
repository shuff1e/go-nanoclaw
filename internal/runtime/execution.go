package runtime

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type ExecutionMode string

const (
	ModeDirect            ExecutionMode = "direct"
	ModePlanExecute       ExecutionMode = "plan_execute"
	ModePlanExecuteVerify ExecutionMode = "plan_execute_verify"
)

type ExecutionIDs struct {
	TraceID   string
	RequestID string
	SessionID string
	TaskID    string
}

type ExecutionBudget struct {
	MaxWallClock       time.Duration
	MaxToolRounds      int
	MaxToolCalls       int
	MaxToolOutputBytes int
}

type ExecutionContext struct {
	IDs            ExecutionIDs
	AgentID        string
	Source         string
	StartedAt      time.Time
	Deadline       time.Time
	Mode           ExecutionMode
	Budget         ExecutionBudget
	Plan           *TaskPlan
	Provider       string
	Model          string
	Stats          ExecutionStats
	OnPlanUpdate   func()
	OnRuntimeEvent func(span, event, status string, metadata map[string]any, err error)
}

type ExecutionStats struct {
	InputTokens  int
	OutputTokens int
	BrainCalls   int
	ToolCalls    int
	SpawnCalls   int
}

type contextKey string

const executionContextKey contextKey = "nanoclaw_execution_context"

var idCounter atomic.Uint64

func DefaultBudget() ExecutionBudget {
	return ExecutionBudget{
		MaxWallClock:       2 * time.Minute,
		MaxToolRounds:      10,
		MaxToolCalls:       32,
		MaxToolOutputBytes: 20000,
	}
}

func NewExecution(agentID, sessionID, source string) *ExecutionContext {
	now := time.Now()
	budget := DefaultBudget()
	return &ExecutionContext{
		IDs: ExecutionIDs{
			TraceID:   newID("trace"),
			RequestID: newID("req"),
			SessionID: sessionID,
			TaskID:    newID("task"),
		},
		AgentID:   agentID,
		Source:    source,
		StartedAt: now,
		Deadline:  now.Add(budget.MaxWallClock),
		Mode:      ModeDirect,
		Budget:    budget,
	}
}

func NewDetachedExecution(agentID, sessionID string) *ExecutionContext {
	return NewExecution(agentID, sessionID, "internal")
}

func WithExecutionContext(ctx context.Context, execCtx *ExecutionContext) context.Context {
	if execCtx == nil {
		return ctx
	}
	return context.WithValue(ctx, executionContextKey, execCtx)
}

func FromContext(ctx context.Context) *ExecutionContext {
	if ctx == nil {
		return nil
	}
	execCtx, _ := ctx.Value(executionContextKey).(*ExecutionContext)
	return execCtx
}

func ContextWithDeadline(ctx context.Context, execCtx *ExecutionContext) (context.Context, context.CancelFunc) {
	if execCtx == nil || execCtx.Deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, execCtx.Deadline)
}

func LogAttrs(execCtx *ExecutionContext) []any {
	if execCtx == nil {
		return nil
	}
	return []any{
		"trace_id", execCtx.IDs.TraceID,
		"request_id", execCtx.IDs.RequestID,
		"session_id", execCtx.IDs.SessionID,
		"task_id", execCtx.IDs.TaskID,
		"agent_id", execCtx.AgentID,
		"source", execCtx.Source,
	}
}

func MaxToolOutputBytes(ctx context.Context, fallback int) int {
	execCtx := FromContext(ctx)
	if execCtx != nil && execCtx.Budget.MaxToolOutputBytes > 0 {
		return execCtx.Budget.MaxToolOutputBytes
	}
	return fallback
}

func TruncateToolOutput(ctx context.Context, text string, fallback int) string {
	limit := MaxToolOutputBytes(ctx, fallback)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n\n[...truncated...]"
}

func newID(prefix string) string {
	n := idCounter.Add(1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}
