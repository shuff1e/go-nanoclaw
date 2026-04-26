package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSStoreHealthCheck(t *testing.T) {
	st := NewFSStore(t.TempDir())
	if err := st.HealthCheck(); err != nil {
		t.Fatalf("health check: %v", err)
	}
}

func TestFSStoreHealthCheckFailsForFileBaseDir(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("write file base dir: %v", err)
	}
	st := NewFSStore(path)
	if err := st.HealthCheck(); err == nil {
		t.Fatal("expected health check failure for file base dir")
	}
}

func TestFSStoreSessionRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	if err := st.SaveSessionEntry("main", SessionEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		User:      "u1",
		Assistant: "a1",
	}); err != nil {
		t.Fatalf("save session entry: %v", err)
	}

	entries, err := st.LoadRecentSessionEntries("main", 10)
	if err != nil {
		t.Fatalf("load session entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].User != "u1" || entries[0].Assistant != "a1" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestFSStoreCronRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	now := time.Now()
	in := []CronJobRecord{{
		AgentID:  "main",
		Name:     "job1",
		Schedule: "*/5",
		Prompt:   "ping",
		Enabled:  true,
		LastRun:  &now,
	}}
	if err := st.SaveCronJobs("main", in); err != nil {
		t.Fatalf("save cron jobs: %v", err)
	}
	out, err := st.LoadCronJobs("main")
	if err != nil {
		t.Fatalf("load cron jobs: %v", err)
	}
	if len(out) != 1 || out[0].Name != "job1" {
		t.Fatalf("unexpected cron records: %+v", out)
	}
}

func TestFSStoreMemoryRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	record := MemoryRecord{
		AgentID:    "main",
		Category:   "facts",
		Content:    "user prefers concise answers",
		Source:     "test",
		Confidence: 0.9,
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
		RecordedAt: time.Now(),
	}
	if err := st.SaveMemoryRecord("main", record); err != nil {
		t.Fatalf("save memory record: %v", err)
	}
	records, err := st.LoadMemoryRecords("main", 10)
	if err != nil {
		t.Fatalf("load memory records: %v", err)
	}
	if len(records) != 1 || records[0].Content != record.Content || records[0].Confidence != 0.9 || records[0].Source != "test" {
		t.Fatalf("unexpected memory records: %+v", records)
	}
}

func TestFSStoreExecutionAndToolAuditRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	if err := st.SaveExecutionLog(ExecutionLog{AgentID: "main", RequestID: "r1", Status: "completed", StartedAt: time.Now()}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	if err := st.SaveToolAuditLog(ToolAuditLog{AgentID: "main", ToolName: "run_command", Status: "denied", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("save tool audit log: %v", err)
	}
	executions, err := st.LoadExecutionLogs("main", 10)
	if err != nil {
		t.Fatalf("load execution logs: %v", err)
	}
	if len(executions) != 1 || executions[0].RequestID != "r1" {
		t.Fatalf("unexpected executions: %+v", executions)
	}
	audits, err := st.LoadToolAuditLogs("main", 10)
	if err != nil {
		t.Fatalf("load tool audits: %v", err)
	}
	if len(audits) != 1 || audits[0].ToolName != "run_command" {
		t.Fatalf("unexpected tool audits: %+v", audits)
	}
}

func TestFSStoreApprovalRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	record := ApprovalRecord{
		ApprovalID:  "approval-1",
		AgentID:     "main",
		RequestID:   "req-1",
		ToolName:    "run_command",
		Status:      "pending",
		RequestedAt: time.Now().UTC(),
	}
	if err := st.SaveApprovalRecord("main", record); err != nil {
		t.Fatalf("save approval record: %v", err)
	}
	records, err := st.LoadApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("load approval records: %v", err)
	}
	if len(records) != 1 || records[0].ApprovalID != "approval-1" || records[0].Status != "pending" {
		t.Fatalf("unexpected approval records: %+v", records)
	}
}

func TestFSStorePlanRoundTrip(t *testing.T) {
	st := NewFSStore(t.TempDir())
	plan := PlanRecord{
		AgentID:     "main",
		RequestID:   "req-1",
		TaskID:      "task-1",
		Mode:        "plan_execute",
		Goal:        "Implement the feature",
		Status:      "completed",
		GeneratedAt: time.Now(),
		UpdatedAt:   time.Now(),
		Steps: []PlanStepRecord{
			{ID: "step-1", Title: "Analyze", Status: "completed"},
			{ID: "step-2", Title: "Execute", Status: "completed"},
		},
	}
	if err := st.SavePlan("main", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	plans, err := st.LoadPlans("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) != 1 || plans[0].RequestID != "req-1" || len(plans[0].Steps) != 2 {
		t.Fatalf("unexpected plans: %+v", plans)
	}
}
