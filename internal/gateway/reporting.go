package gateway

import (
	"context"
	"fmt"
	"strings"
	"sort"
	"time"

	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

func (gw *Gateway) ExecutionLogs(agentID string, limit int) ([]store.ExecutionLog, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadExecutionLogs(agentID, limit)
}

func (gw *Gateway) FilteredExecutionLogs(agentID string, limit int, filter ExecutionLogFilter) ([]store.ExecutionLog, error) {
	logs, err := gw.ExecutionLogs(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ExecutionLogFilter{}) {
		return logs, nil
	}
	var filtered []store.ExecutionLog
	for _, log := range logs {
		if filter.SessionID != "" && log.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && log.RequestID != filter.RequestID {
			continue
		}
		if filter.Status != "" && log.Status != filter.Status {
			continue
		}
		if filter.Source != "" && log.Source != filter.Source {
			continue
		}
		if !filter.Since.IsZero() && log.StartedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && log.StartedAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered, nil
}

func (gw *Gateway) SummarizeExecutionLogs(logs []store.ExecutionLog) ExecutionLogSummary {
	summary := ExecutionLogSummary{
		Total:    len(logs),
		ByStatus: make(map[string]int),
		BySource: make(map[string]int),
	}
	for _, log := range logs {
		summary.ByStatus[log.Status]++
		summary.BySource[log.Source]++
	}
	return summary
}

func (gw *Gateway) SummarizeToolAuditLogs(logs []store.ToolAuditLog) ToolAuditSummary {
	summary := ToolAuditSummary{
		Total:    len(logs),
		ByStatus: make(map[string]int),
		ByTool:   make(map[string]int),
	}
	for _, log := range logs {
		summary.ByStatus[log.Status]++
		summary.ByTool[log.ToolName]++
	}
	return summary
}

func (gw *Gateway) PlanRecords(agentID string, limit int) ([]store.PlanRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadPlans(agentID, limit)
}

func (gw *Gateway) findPlanRecord(agentID, requestID, taskID string) (*store.PlanRecord, error) {
	plans, err := gw.PlanRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	var latest *store.PlanRecord
	for i := range plans {
		plan := plans[i]
		if requestID != "" && plan.RequestID != requestID {
			continue
		}
		if taskID != "" && plan.TaskID != taskID {
			continue
		}
		if latest == nil || plan.UpdatedAt.After(latest.UpdatedAt) {
			copyPlan := plan
			latest = &copyPlan
		}
	}
	if latest == nil {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "plan not found")
	}
	return latest, nil
}

func (gw *Gateway) TaskViews(agentID string, limit int, filter ExecutionLogFilter) ([]TaskView, error) {
	eventFilter := filter
	eventFilter.Status = ""
	logs, err := gw.FilteredExecutionLogs(agentID, 0, eventFilter)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return nil, nil
	}

	taskMap := make(map[string]*TaskView)
	order := make([]string, 0)
	for _, log := range logs {
		key := log.RequestID
		if key == "" {
			key = log.TaskID
		}
		if key == "" {
			continue
		}

		view, ok := taskMap[key]
		if !ok {
			view = &TaskView{
				RequestID:   log.RequestID,
				TaskID:      log.TaskID,
				SessionID:   log.SessionID,
				AgentID:     log.AgentID,
				Source:      log.Source,
				Status:      log.Status,
				StartedAt:   log.StartedAt,
				LastEventAt: log.StartedAt,
			}
			taskMap[key] = view
			order = append(order, key)
		}

		view.EventCount++
		if view.RequestID == "" {
			view.RequestID = log.RequestID
		}
		if view.TaskID == "" {
			view.TaskID = log.TaskID
		}
		if view.SessionID == "" {
			view.SessionID = log.SessionID
		}
		if view.AgentID == "" {
			view.AgentID = log.AgentID
		}
		if view.Source == "" {
			view.Source = log.Source
		}
		if log.StartedAt.Before(view.StartedAt) || view.StartedAt.IsZero() {
			view.StartedAt = log.StartedAt
		}
		if log.StartedAt.After(view.LastEventAt) || view.LastEventAt.IsZero() {
			view.LastEventAt = log.StartedAt
		}
		if log.Error != "" {
			view.Error = log.Error
		}

		switch log.Status {
		case "failed":
			view.Status = "failed"
			view.FailedAt = log.StartedAt
		case "cancelled":
			if view.Status != "failed" {
				view.Status = "cancelled"
			}
			view.CancelledAt = log.StartedAt
		case "awaiting_approval":
			if view.Status != "failed" && view.Status != "cancelled" {
				view.Status = "awaiting_approval"
			}
		case "completed":
			if view.Status != "failed" && view.Status != "cancelled" && view.Status != "awaiting_approval" {
				view.Status = "completed"
			}
			view.CompletedAt = log.StartedAt
		case "started":
			if view.Status == "" {
				view.Status = "running"
			}
			if view.Status == "started" {
				view.Status = "running"
			}
		default:
			if view.Status == "" {
				view.Status = log.Status
			}
		}
	}

	plans, err := gw.PlanRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	planMap := make(map[string]store.PlanRecord, len(plans))
	for _, plan := range plans {
		key := plan.RequestID
		if key == "" {
			key = plan.TaskID
		}
		if key == "" {
			continue
		}
		existing, ok := planMap[key]
		if !ok || plan.UpdatedAt.After(existing.UpdatedAt) {
			planMap[key] = plan
		}
	}

	tasks := make([]TaskView, 0, len(taskMap))
	for _, key := range order {
		view := *taskMap[key]
		if plan, ok := planMap[key]; ok {
			view.RetryOfRequestID = plan.RetryOfRequestID
			view.RetryOfTaskID = plan.RetryOfTaskID
			view.Attempt = plan.Attempt
			view.MaxRetries = plan.MaxRetries
			view.Mode = plan.Mode
			view.Plan = &plan
		}
		if filter.Status != "" && view.Status != filter.Status {
			continue
		}
		tasks = append(tasks, view)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks, nil
}

func (gw *Gateway) SummarizeTaskViews(tasks []TaskView) TaskViewSummary {
	summary := TaskViewSummary{
		Total:     len(tasks),
		ByStatus:  make(map[string]int),
		BySource:  make(map[string]int),
		ByAttempt: make(map[string]int),
	}
	for _, task := range tasks {
		summary.ByStatus[task.Status]++
		summary.BySource[task.Source]++
		attempt := task.Attempt
		if attempt <= 0 {
			attempt = 1
		}
		summary.ByAttempt[fmt.Sprintf("%d", attempt)]++
		if attempt > summary.MaxAttempt {
			summary.MaxAttempt = attempt
		}
		if task.RetryOfRequestID != "" || task.RetryOfTaskID != "" {
			summary.RetriedTotal++
		}
		if task.MaxRetries > 0 {
			summary.AutoRetryable++
		}
	}
	return summary
}

func (gw *Gateway) ToolAuditLogs(agentID string, limit int) ([]store.ToolAuditLog, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadToolAuditLogs(agentID, limit)
}

func (gw *Gateway) FilteredToolAuditLogs(agentID string, limit int, filter ToolAuditLogFilter) ([]store.ToolAuditLog, error) {
	logs, err := gw.ToolAuditLogs(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ToolAuditLogFilter{}) {
		return logs, nil
	}
	var filtered []store.ToolAuditLog
	for _, log := range logs {
		if filter.SessionID != "" && log.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && log.RequestID != filter.RequestID {
			continue
		}
		if filter.TraceID != "" && log.TraceID != filter.TraceID {
			continue
		}
		if filter.Status != "" && log.Status != filter.Status {
			continue
		}
		if filter.ToolName != "" && log.ToolName != filter.ToolName {
			continue
		}
		if !filter.Since.IsZero() && log.OccurredAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && log.OccurredAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered, nil
}

func (gw *Gateway) ApprovalRecords(agentID string, limit int) ([]store.ApprovalRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadApprovalRecords(agentID, limit)
}

func (gw *Gateway) FilteredApprovalRecords(agentID string, limit int, filter ApprovalRecordFilter) ([]store.ApprovalRecord, error) {
	records, err := gw.ApprovalRecords(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ApprovalRecordFilter{}) {
		return records, nil
	}
	var filtered []store.ApprovalRecord
	for _, record := range records {
		if filter.SessionID != "" && record.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && record.RequestID != filter.RequestID {
			continue
		}
		if filter.TraceID != "" && record.TraceID != filter.TraceID {
			continue
		}
		if filter.TaskID != "" && record.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && record.Status != filter.Status {
			continue
		}
		if filter.ToolName != "" && record.ToolName != filter.ToolName {
			continue
		}
		if !filter.Since.IsZero() && record.RequestedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && record.RequestedAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered, nil
}

func (gw *Gateway) DecideApproval(agentID, approvalID, decision, decidedBy string) (*store.ApprovalRecord, error) {
	approvalID = strings.TrimSpace(approvalID)
	decision = strings.TrimSpace(decision)
	if approvalID == "" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing approval_id")
	}
	if decision != "approved" && decision != "rejected" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "decision must be approved or rejected")
	}
	records, err := gw.ApprovalRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	var latest *store.ApprovalRecord
	for i := range records {
		record := records[i]
		if record.ApprovalID != approvalID {
			continue
		}
		if latest == nil || record.RequestedAt.After(latest.RequestedAt) || record.DecidedAt.After(latest.DecidedAt) {
			copyRecord := record
			latest = &copyRecord
		}
	}
	if latest == nil {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "approval not found")
	}
	if latest.Status != "pending" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "approval already decided")
	}
	if timeout := gw.approvalTimeout(agentID); timeout > 0 && time.Since(latest.RequestedAt) > timeout {
		expired := *latest
		expired.Status = "expired"
		expired.DecidedAt = time.Now().UTC()
		expired.DecidedBy = strings.TrimSpace(decidedBy)
		if expired.DecidedBy == "" {
			expired.DecidedBy = "api"
		}
		if err := gw.store.SaveApprovalRecord(agentID, expired); err != nil {
			return nil, err
		}
		execCtx := approvalExecutionContext(agentID, expired)
		gw.applyExecutionBudget(execCtx)
		err := mcRuntime.Errorf(mcRuntime.CodeApprovalRequired, "approval expired for tool %s", expired.ToolName)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "approval", "expired", "expired", map[string]any{"approval_id": approvalID, "tool_name": expired.ToolName}, err)
		return nil, err
	}
	decided := *latest
	decided.Status = decision
	decided.DecidedAt = time.Now().UTC()
	decided.DecidedBy = strings.TrimSpace(decidedBy)
	if decided.DecidedBy == "" {
		decided.DecidedBy = "api"
	}
	if err := gw.store.SaveApprovalRecord(agentID, decided); err != nil {
		return nil, err
	}
	execCtx := approvalExecutionContext(agentID, decided)
	gw.applyExecutionBudget(execCtx)
	if decision == "rejected" {
		err := mcRuntime.Errorf(mcRuntime.CodeApprovalRequired, "approval rejected for tool %s", decided.ToolName)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "approval", "rejected", "rejected", map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, err)
		return &decided, nil
	}
	a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		wrapped := mcRuntime.Wrap(mcRuntime.CodeInternal, err, "get agent '%s'", agentID)
		gw.persistExecution(execCtx, "failed", wrapped)
		return nil, wrapped
	}
	gw.configureAgentStorage(agentID, a)
	execCtx.Stats.ToolCalls = 1
	toolResult, toolErr := a.Hands.ExecuteApproved(mcRuntime.WithExecutionContext(context.Background(), execCtx), decided.ToolName, decided.Arguments)
	if toolErr != nil {
		gw.persistExecution(execCtx, "failed", toolErr)
		gw.persistTrace(execCtx, "approval", "approved_tool_failed", string(toolResult.Status), map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, toolErr)
		return nil, toolErr
	}
	gw.persistExecution(execCtx, "completed", nil)
	gw.persistTrace(execCtx, "approval", "approved_tool_completed", string(toolResult.Status), map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, nil)
	return &decided, nil
}

func (gw *Gateway) approvalTimeout(agentID string) time.Duration {
	if gw.Config == nil {
		return 0
	}
	def, err := gw.Config.GetAgent(agentID)
	if err != nil || def.ToolPolicies.ApprovalTimeoutMin <= 0 {
		return 0
	}
	return time.Duration(def.ToolPolicies.ApprovalTimeoutMin) * time.Minute
}

func approvalExecutionContext(agentID string, approval store.ApprovalRecord) *mcRuntime.ExecutionContext {
	return &mcRuntime.ExecutionContext{
		IDs: mcRuntime.ExecutionIDs{
			TraceID:   approval.TraceID,
			RequestID: approval.RequestID,
			SessionID: approval.SessionID,
			TaskID:    approval.TaskID,
		},
		AgentID:   agentID,
		Source:    "approval",
		StartedAt: time.Now(),
		Mode:      mcRuntime.ModeDirect,
	}
}

func (gw *Gateway) SessionEntries(agentID string, limit int) ([]store.SessionEntry, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadRecentSessionEntries(agentID, limit)
}

func (gw *Gateway) MemoryRecords(agentID string, limit int) ([]store.MemoryRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadMemoryRecords(agentID, limit)
}

func (gw *Gateway) CronJobs(agentID string) ([]store.CronJobRecord, error) {
	if scheduler, ok := gw.cronSchedulers[agentID]; ok {
		return scheduler.Records(agentID), nil
	}
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadCronJobs(agentID)
}

func (gw *Gateway) TraceEvents(agentID string, limit int, filter TraceEventFilter) ([]store.TraceEvent, error) {
	if gw.store == nil {
		return nil, nil
	}
	events, err := gw.store.LoadTraceEvents(agentID, limit)
	if err != nil {
		return nil, err
	}
	filtered := make([]store.TraceEvent, 0, len(events))
	for _, event := range events {
		if filter.TraceID != "" && event.TraceID != filter.TraceID {
			continue
		}
		if filter.RequestID != "" && event.RequestID != filter.RequestID {
			continue
		}
		if filter.SessionID != "" && event.SessionID != filter.SessionID {
			continue
		}
		if filter.Span != "" && event.Span != filter.Span {
			continue
		}
		if filter.Event != "" && event.Event != filter.Event {
			continue
		}
		if filter.Status != "" && event.Status != filter.Status {
			continue
		}
		if !filter.Since.IsZero() && event.At.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && event.At.After(filter.Until) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered, nil
}
