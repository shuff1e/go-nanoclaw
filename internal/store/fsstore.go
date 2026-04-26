package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type FSStore struct {
	baseDir string
	mu      sync.Mutex
}

func NewFSStore(baseDir string) *FSStore {
	return &FSStore{
		baseDir: baseDir,
	}
}

func (s *FSStore) HealthCheck() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.baseDir, ".health-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err := f.WriteString("ok"); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (s *FSStore) SaveSessionEntry(agentID string, entry SessionEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "sessions", sanitize(agentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadRecentSessionEntries(agentID string, limit int) ([]SessionEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "sessions", sanitize(agentID)+".jsonl")
	return loadJSONL[SessionEntry](path, limit)
}

func (s *FSStore) SaveCronJobs(agentID string, jobs []CronJobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "cron", sanitize(agentID)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (s *FSStore) LoadCronJobs(agentID string) ([]CronJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "cron", sanitize(agentID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var jobs []CronJobRecord
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse cron store: %w", err)
	}
	return jobs, nil
}

func (s *FSStore) SaveExecutionLog(log ExecutionLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "executions", sanitize(log.AgentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(log)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadExecutionLogs(agentID string, limit int) ([]ExecutionLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "executions", sanitize(agentID)+".jsonl")
	return loadJSONL[ExecutionLog](path, limit)
}

func (s *FSStore) SaveToolAuditLog(log ToolAuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "tool-audit", sanitize(log.AgentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(log)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadToolAuditLogs(agentID string, limit int) ([]ToolAuditLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "tool-audit", sanitize(agentID)+".jsonl")
	return loadJSONL[ToolAuditLog](path, limit)
}

func (s *FSStore) SaveApprovalRecord(agentID string, record ApprovalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "approvals", sanitize(agentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadApprovalRecords(agentID string, limit int) ([]ApprovalRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "approvals", sanitize(agentID)+".jsonl")
	return loadJSONL[ApprovalRecord](path, limit)
}

func (s *FSStore) SaveTraceEvent(agentID string, event TraceEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "traces", sanitize(agentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadTraceEvents(agentID string, limit int) ([]TraceEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "traces", sanitize(agentID)+".jsonl")
	return loadJSONL[TraceEvent](path, limit)
}

func (s *FSStore) SavePlan(agentID string, plan PlanRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "plans", sanitize(agentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadPlans(agentID string, limit int) ([]PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "plans", sanitize(agentID)+".jsonl")
	return loadJSONL[PlanRecord](path, limit)
}

func (s *FSStore) SaveMemoryRecord(agentID string, record MemoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "memory", sanitize(agentID)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *FSStore) LoadMemoryRecords(agentID string, limit int) ([]MemoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, "memory", sanitize(agentID)+".jsonl")
	return loadJSONL[MemoryRecord](path, limit)
}

func loadJSONL[T any](path string, limit int) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var records []T
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}
	return records, nil
}

func sanitize(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	return replacer.Replace(s)
}
