package runtime

import "time"

type EventType string

const (
	EventRunQueued             EventType = "run.queued"
	EventRunStarted            EventType = "run.started"
	EventRunThinking           EventType = "run.thinking"
	EventRunPlanning           EventType = "run.planning"
	EventRunReplanning         EventType = "run.replanning"
	EventRunStepStarted        EventType = "run.step_started"
	EventRunStepCompleted      EventType = "run.step_completed"
	EventRunStepFailed         EventType = "run.step_failed"
	EventRunStepSkipped        EventType = "run.step_skipped"
	EventRunVerifying          EventType = "run.verifying"
	EventToolStarted           EventType = "tool.started"
	EventToolCompleted         EventType = "tool.completed"
	EventToolFailed            EventType = "tool.failed"
	EventRunCompleted          EventType = "run.completed"
	EventRunFailed             EventType = "run.failed"
	EventRunCancelled          EventType = "run.cancelled"
	EventRunAwaitingApproval   EventType = "run.awaiting_approval"
	EventApprovalRejected      EventType = "approval.rejected"
	EventApprovalExpired       EventType = "approval.expired"
	EventApprovalToolCompleted EventType = "approval.tool_completed"
	EventApprovalToolFailed    EventType = "approval.tool_failed"
)

type RuntimeEvent struct {
	Type      EventType      `json:"type"`
	TraceID   string         `json:"trace_id,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Source    string         `json:"source,omitempty"`
	Span      string         `json:"span,omitempty"`
	Status    string         `json:"status,omitempty"`
	Error     string         `json:"error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	At        time.Time      `json:"at"`
}

func NewRuntimeEvent(execCtx *ExecutionContext, eventType EventType, span, status string, metadata map[string]any, err error) RuntimeEvent {
	ev := RuntimeEvent{
		Type:     eventType,
		Span:     span,
		Status:   status,
		Metadata: metadata,
		At:       time.Now().UTC(),
	}
	if execCtx != nil {
		ev.TraceID = execCtx.IDs.TraceID
		ev.RequestID = execCtx.IDs.RequestID
		ev.SessionID = execCtx.IDs.SessionID
		ev.TaskID = execCtx.IDs.TaskID
		ev.AgentID = execCtx.AgentID
		ev.Source = execCtx.Source
	}
	if err != nil {
		ev.Error = err.Error()
	}
	return ev
}
