package store

import "time"

type SessionEntry struct {
	Timestamp string `json:"ts"`
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type ExecutionLog struct {
	TraceID      string    `json:"trace_id,omitempty"`
	RequestID    string    `json:"request_id"`
	SessionID    string    `json:"session_id"`
	TaskID       string    `json:"task_id"`
	AgentID      string    `json:"agent_id"`
	Source       string    `json:"source"`
	Status       string    `json:"status"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	BrainCalls   int       `json:"brain_calls,omitempty"`
	ToolCalls    int       `json:"tool_calls,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

type ToolAuditLog struct {
	TraceID    string         `json:"trace_id,omitempty"`
	RequestID  string         `json:"request_id"`
	SessionID  string         `json:"session_id"`
	TaskID     string         `json:"task_id"`
	AgentID    string         `json:"agent_id"`
	ToolName   string         `json:"tool_name"`
	Status     string         `json:"status"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type ApprovalRecord struct {
	ApprovalID        string         `json:"approval_id"`
	TraceID           string         `json:"trace_id,omitempty"`
	RequestID         string         `json:"request_id"`
	SessionID         string         `json:"session_id"`
	TaskID            string         `json:"task_id"`
	AgentID           string         `json:"agent_id"`
	ToolName          string         `json:"tool_name"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	ArgumentsRedacted map[string]any `json:"arguments_redacted,omitempty"`
	Status            string         `json:"status"`
	Reason            string         `json:"reason,omitempty"`
	RequestedAt       time.Time      `json:"requested_at"`
	DecidedAt         time.Time      `json:"decided_at,omitempty"`
	DecidedBy         string         `json:"decided_by,omitempty"`
}

type TraceEvent struct {
	TraceID   string         `json:"trace_id"`
	RequestID string         `json:"request_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	AgentID   string         `json:"agent_id"`
	Source    string         `json:"source,omitempty"`
	Span      string         `json:"span"`
	Event     string         `json:"event"`
	Status    string         `json:"status,omitempty"`
	Error     string         `json:"error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	At        time.Time      `json:"at"`
}

type MemoryRecord struct {
	AgentID    string    `json:"agent_id"`
	Category   string    `json:"category"`
	Content    string    `json:"content"`
	Source     string    `json:"source,omitempty"`
	Confidence float64   `json:"confidence,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

type CronJobRecord struct {
	AgentID  string     `json:"agent_id"`
	Name     string     `json:"name"`
	Schedule string     `json:"schedule"`
	Prompt   string     `json:"prompt"`
	Enabled  bool       `json:"enabled"`
	LastRun  *time.Time `json:"last_run,omitempty"`
}

type PlanStepRecord struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Prompt          string    `json:"prompt"`
	DependsOn       []string  `json:"depends_on,omitempty"`
	RetryPolicy     string    `json:"retry_policy,omitempty"`
	FailureStrategy string    `json:"failure_strategy,omitempty"`
	Checkpoint      string    `json:"checkpoint,omitempty"`
	Status          string    `json:"status"`
	Result          string    `json:"result,omitempty"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

type PlanChildTaskRecord struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Prompt      string    `json:"prompt"`
	Status      string    `json:"status"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

type PlanRecord struct {
	RequestID        string                `json:"request_id"`
	TaskID           string                `json:"task_id"`
	SessionID        string                `json:"session_id"`
	AgentID          string                `json:"agent_id"`
	Source           string                `json:"source"`
	RetryOfRequestID string                `json:"retry_of_request_id,omitempty"`
	RetryOfTaskID    string                `json:"retry_of_task_id,omitempty"`
	Attempt          int                   `json:"attempt"`
	MaxRetries       int                   `json:"max_retries,omitempty"`
	Mode             string                `json:"mode"`
	Goal             string                `json:"goal"`
	Status           string                `json:"status"`
	Summary          string                `json:"summary,omitempty"`
	GeneratedAt      time.Time             `json:"generated_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	Steps            []PlanStepRecord      `json:"steps"`
	ChildTasks       []PlanChildTaskRecord `json:"child_tasks,omitempty"`
}

type Store interface {
	HealthCheck() error

	SaveSessionEntry(agentID string, entry SessionEntry) error
	LoadRecentSessionEntries(agentID string, limit int) ([]SessionEntry, error)

	SaveCronJobs(agentID string, jobs []CronJobRecord) error
	LoadCronJobs(agentID string) ([]CronJobRecord, error)

	SaveExecutionLog(log ExecutionLog) error
	LoadExecutionLogs(agentID string, limit int) ([]ExecutionLog, error)
	SaveToolAuditLog(log ToolAuditLog) error
	LoadToolAuditLogs(agentID string, limit int) ([]ToolAuditLog, error)
	SaveApprovalRecord(agentID string, record ApprovalRecord) error
	LoadApprovalRecords(agentID string, limit int) ([]ApprovalRecord, error)
	SaveTraceEvent(agentID string, event TraceEvent) error
	LoadTraceEvents(agentID string, limit int) ([]TraceEvent, error)
	SavePlan(agentID string, plan PlanRecord) error
	LoadPlans(agentID string, limit int) ([]PlanRecord, error)

	SaveMemoryRecord(agentID string, record MemoryRecord) error
	LoadMemoryRecords(agentID string, limit int) ([]MemoryRecord, error)
}
