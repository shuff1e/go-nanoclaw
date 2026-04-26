package runtime

import (
	"fmt"
	"strings"
	"time"
)

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

type TaskStep struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Prompt          string     `json:"prompt"`
	DependsOn       []string   `json:"depends_on,omitempty"`
	RetryPolicy     string     `json:"retry_policy,omitempty"`
	FailureStrategy string     `json:"failure_strategy,omitempty"`
	Checkpoint      string     `json:"checkpoint,omitempty"`
	Status          StepStatus `json:"status"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	StartedAt       time.Time  `json:"started_at,omitempty"`
	CompletedAt     time.Time  `json:"completed_at,omitempty"`
}

type ChildTask struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agent_id"`
	Prompt      string     `json:"prompt"`
	Status      StepStatus `json:"status"`
	Result      string     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	StartedAt   time.Time  `json:"started_at,omitempty"`
	CompletedAt time.Time  `json:"completed_at,omitempty"`
}

type TaskPlan struct {
	RequestID        string        `json:"request_id"`
	TaskID           string        `json:"task_id"`
	SessionID        string        `json:"session_id"`
	AgentID          string        `json:"agent_id"`
	Source           string        `json:"source"`
	RetryOfRequestID string        `json:"retry_of_request_id,omitempty"`
	RetryOfTaskID    string        `json:"retry_of_task_id,omitempty"`
	Attempt          int           `json:"attempt"`
	MaxRetries       int           `json:"max_retries,omitempty"`
	Mode             ExecutionMode `json:"mode"`
	Goal             string        `json:"goal"`
	Status           StepStatus    `json:"status"`
	Summary          string        `json:"summary,omitempty"`
	GeneratedAt      time.Time     `json:"generated_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Steps            []TaskStep    `json:"steps"`
	ChildTasks       []ChildTask   `json:"child_tasks,omitempty"`
}

func ParseExecutionMode(raw string) (ExecutionMode, bool) {
	mode := ExecutionMode(strings.TrimSpace(raw))
	switch mode {
	case "", ModeDirect:
		return ModeDirect, true
	case ModePlanExecute, ModePlanExecuteVerify:
		return mode, true
	default:
		return "", false
	}
}

func IsPlannedMode(mode ExecutionMode) bool {
	return mode == ModePlanExecute || mode == ModePlanExecuteVerify
}

func BuildTaskPlan(execCtx *ExecutionContext, goal string) *TaskPlan {
	now := time.Now().UTC()
	steps := buildPlanSteps(goal)
	plan := &TaskPlan{
		Mode:        ModePlanExecute,
		Goal:        strings.TrimSpace(goal),
		Status:      StepPending,
		Attempt:     1,
		GeneratedAt: now,
		UpdatedAt:   now,
		Steps:       steps,
	}
	if execCtx != nil {
		plan.RequestID = execCtx.IDs.RequestID
		plan.TaskID = execCtx.IDs.TaskID
		plan.SessionID = execCtx.IDs.SessionID
		plan.AgentID = execCtx.AgentID
		plan.Source = execCtx.Source
		if IsPlannedMode(execCtx.Mode) {
			plan.Mode = execCtx.Mode
		}
	}
	return plan
}

func buildPlanSteps(goal string) []TaskStep {
	clauses := splitGoal(goal)
	if len(clauses) <= 1 {
		return []TaskStep{
			{ID: "step-1", Title: "Analyze request", Prompt: "Understand the request, constraints, and success criteria before acting.", Status: StepPending},
			{ID: "step-2", Title: "Execute task", Prompt: strings.TrimSpace(goal), Status: StepPending},
			{ID: "step-3", Title: "Verify result", Prompt: "Verify the result against the original request. Call out gaps or remaining work.", Status: StepPending},
		}
	}

	steps := make([]TaskStep, 0, len(clauses))
	for i, clause := range clauses {
		title := fmt.Sprintf("Step %d", i+1)
		steps = append(steps, TaskStep{
			ID:     fmt.Sprintf("step-%d", i+1),
			Title:  title,
			Prompt: clause,
			Status: StepPending,
		})
	}
	return steps
}

func splitGoal(goal string) []string {
	normalized := strings.TrimSpace(goal)
	if normalized == "" {
		return nil
	}

	replacer := strings.NewReplacer(
		"\r\n", "\n",
		"。", "\n",
		"；", "\n",
		";", "\n",
		" then ", "\n",
		" Then ", "\n",
		" and then ", "\n",
		" 然后 ", "\n",
		" 接着 ", "\n",
	)
	normalized = replacer.Replace(normalized)

	rawParts := strings.Split(normalized, "\n")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "-* \t")
		part = strings.Trim(part, ".,，")
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}
