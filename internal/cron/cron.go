// Package cron provides scheduled task execution for agents.
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go-nanoclaw/internal/agent"
	mclog "go-nanoclaw/internal/log"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

// Job represents a scheduled task.
type Job struct {
	Name     string
	Schedule string // "HH:MM" for daily, or "*/N" for every N minutes
	Prompt   string
	Enabled  bool
	LastRun  *time.Time
}

// OutputHandler is called when a cron job produces output.
type OutputHandler func(name, response string)

// Scheduler runs scheduled tasks at configured times.
type Scheduler struct {
	mu         sync.Mutex
	Agent      *agent.Agent
	dispatcher *mcRuntime.Dispatcher
	store      store.Store
	jobs       []Job
	onOutput   OutputHandler
	cancel     context.CancelFunc
}

// NewScheduler creates a new cron Scheduler.
func NewScheduler(a *agent.Agent, dispatcher *mcRuntime.Dispatcher, st store.Store) *Scheduler {
	return &Scheduler{
		Agent:      a,
		dispatcher: dispatcher,
		store:      st,
	}
}

// AddJob adds a cron job.
func (s *Scheduler) AddJob(job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].Name == job.Name {
			job.LastRun = s.jobs[i].LastRun
			s.jobs[i] = job
			slog.Info("Cron job updated", "name", job.Name, "schedule", job.Schedule)
			s.persistLocked()
			return
		}
	}
	s.jobs = append(s.jobs, job)
	slog.Info("Cron job added", "name", job.Name, "schedule", job.Schedule)
	s.persistLocked()
}

// SetOutputHandler sets the callback for cron job output.
func (s *Scheduler) SetOutputHandler(handler OutputHandler) {
	s.onOutput = handler
}

// Start begins the cron scheduler loop in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.loop(ctx)
	s.mu.Lock()
	count := len(s.jobs)
	s.mu.Unlock()
	slog.Info("Cron scheduler started", "jobs", count)
}

// Stop stops the cron scheduler.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkJobs(ctx)
		}
	}
}

func (s *Scheduler) checkJobs(ctx context.Context) {
	mclog.Step("CronScheduler.checkJobs", "agent_id", s.Agent.ID)
	s.mu.Lock()
	now := time.Now()
	var toRun []int
	for i := range s.jobs {
		if !s.jobs[i].Enabled {
			continue
		}
		if s.shouldRun(&s.jobs[i], now) {
			toRun = append(toRun, i)
		}
	}
	s.mu.Unlock()

	for _, i := range toRun {
		s.executeJob(ctx, i, now)
	}
}

func (s *Scheduler) shouldRun(job *Job, now time.Time) bool {
	schedule := job.Schedule

	// Every N minutes: */N
	if len(schedule) > 2 && schedule[:2] == "*/" {
		var interval int
		if _, err := fmt.Sscanf(schedule[2:], "%d", &interval); err != nil || interval <= 0 {
			return false
		}
		if job.LastRun == nil {
			return true
		}
		elapsed := now.Sub(*job.LastRun).Minutes()
		return elapsed >= float64(interval)
	}

	// Daily at HH:MM
	var hour, minute int
	if _, err := fmt.Sscanf(schedule, "%d:%d", &hour, &minute); err == nil {
		if now.Hour() == hour && now.Minute() == minute {
			if job.LastRun == nil || job.LastRun.Day() != now.Day() ||
				job.LastRun.Month() != now.Month() || job.LastRun.Year() != now.Year() {
				return true
			}
		}
		return false
	}

	return false
}

func (s *Scheduler) executeJob(ctx context.Context, index int, now time.Time) {
	name, prompt := s.prepareJob(index, now)
	if name == "" {
		return
	}

	execCtx := mcRuntime.NewExecution(s.Agent.ID, s.Agent.ID+":cron", "cron")
	execCtx.Budget.MaxWallClock = 1 * time.Minute
	slog.Info("Cron executing", append([]any{"name", name}, mcRuntime.LogAttrs(execCtx))...)
	var response string
	err := s.dispatcher.Run(mcRuntime.WithExecutionContext(ctx, execCtx), execCtx, func(runCtx context.Context) error {
		var runErr error
		response, runErr = s.Agent.ProcessExecution(runCtx, execCtx, fmt.Sprintf("[Scheduled job %s]\n%s", name, prompt))
		return runErr
	})
	if err != nil {
		slog.Error("Cron job failed", append([]any{"name", name, "error", err}, mcRuntime.LogAttrs(execCtx))...)
		return
	}

	if s.onOutput != nil {
		s.onOutput(name, response)
	}
	slog.Info("Cron completed", append([]any{"name", name, "response", response[:min(100, len(response))]}, mcRuntime.LogAttrs(execCtx))...)
}

// RunJobNow manually triggers a cron job by name.
func (s *Scheduler) RunJobNow(ctx context.Context, name string) (string, error) {
	index := s.findJob(name)
	if index < 0 {
		return fmt.Sprintf("Job '%s' not found", name), nil
	}
	s.executeJob(ctx, index, time.Now())
	return fmt.Sprintf("Job '%s' executed", name), nil
}

// ListJobs returns information about all cron jobs.
func (s *Scheduler) ListJobs() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]map[string]any, 0, len(s.jobs))
	for _, j := range s.jobs {
		var lastRun *string
		if j.LastRun != nil {
			s := j.LastRun.Format(time.RFC3339)
			lastRun = &s
		}
		result = append(result, map[string]any{
			"name":     j.Name,
			"schedule": j.Schedule,
			"enabled":  j.Enabled,
			"last_run": lastRun,
		})
	}
	return result
}

func (s *Scheduler) LoadJobs(records []store.CronJobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = nil
	for _, record := range records {
		s.jobs = append(s.jobs, Job{
			Name:     record.Name,
			Schedule: record.Schedule,
			Prompt:   record.Prompt,
			Enabled:  record.Enabled,
			LastRun:  record.LastRun,
		})
	}
}

func (s *Scheduler) Records(agentID string) []store.CronJobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordsLocked(agentID)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// prepareJob updates job state under lock and returns name/prompt.
// Returns empty name if index is out of range.
func (s *Scheduler) prepareJob(index int, now time.Time) (name, prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.jobs) {
		return "", ""
	}
	job := &s.jobs[index]
	job.LastRun = &now
	name = job.Name
	prompt = job.Prompt
	s.persistLocked()
	return name, prompt
}

// findJob returns the index of a job by name, or -1 if not found.
func (s *Scheduler) findJob(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *Scheduler) persistLocked() {
	if s.store == nil {
		return
	}
	records := s.recordsLocked(s.Agent.ID)
	if err := s.store.SaveCronJobs(s.Agent.ID, records); err != nil {
		slog.Error("Persist cron jobs failed", "agent_id", s.Agent.ID, "error", err)
	}
}

func (s *Scheduler) recordsLocked(agentID string) []store.CronJobRecord {
	records := make([]store.CronJobRecord, 0, len(s.jobs))
	for _, job := range s.jobs {
		records = append(records, store.CronJobRecord{
			AgentID:  agentID,
			Name:     job.Name,
			Schedule: job.Schedule,
			Prompt:   job.Prompt,
			Enabled:  job.Enabled,
			LastRun:  job.LastRun,
		})
	}
	return records
}
