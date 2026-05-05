package cron

import (
	"context"
	"testing"
	"time"

	"go-nanoclaw/internal/agent"
	"go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

func TestAddJobUpdatesByName(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	s.AddJob(Job{Name: "job1", Schedule: "*/5", Prompt: "first", Enabled: true})
	s.AddJob(Job{Name: "job1", Schedule: "*/10", Prompt: "second", Enabled: true})

	jobs := s.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0]["schedule"] != "*/10" {
		t.Fatalf("expected updated schedule, got %v", jobs[0]["schedule"])
	}
}

func TestRunJobNowNotFound(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	msg, err := s.RunJobNow(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == "" {
		t.Fatal("expected response message")
	}
}

func TestShouldRunEveryNMinutes(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	job := &Job{Schedule: "*/5", Enabled: true}
	// No LastRun → should run
	if !s.shouldRun(job, now) {
		t.Fatal("expected job to run with no LastRun")
	}

	// LastRun 3 minutes ago → should NOT run
	past := now.Add(-3 * time.Minute)
	job.LastRun = &past
	if s.shouldRun(job, now) {
		t.Fatal("expected job not to run (only 3 min elapsed)")
	}

	// LastRun 6 minutes ago → should run
	past = now.Add(-6 * time.Minute)
	job.LastRun = &past
	if !s.shouldRun(job, now) {
		t.Fatal("expected job to run (6 min elapsed)")
	}
}

func TestShouldRunDailyHHMM(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	now := time.Date(2025, 1, 1, 14, 30, 0, 0, time.UTC)

	job := &Job{Schedule: "14:30", Enabled: true}
	// No LastRun → should run
	if !s.shouldRun(job, now) {
		t.Fatal("expected job to run at matching HH:MM with no LastRun")
	}

	// Already ran today → should NOT run
	past := now.Add(-1 * time.Hour)
	job.LastRun = &past
	if s.shouldRun(job, now) {
		t.Fatal("expected job not to run (already ran today)")
	}

	// Ran yesterday → should run
	yesterday := now.Add(-24 * time.Hour)
	job.LastRun = &yesterday
	if !s.shouldRun(job, now) {
		t.Fatal("expected job to run (last run was yesterday)")
	}
}

func TestShouldRunInvalidSchedule(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	now := time.Now()

	job := &Job{Schedule: "invalid", Enabled: true}
	if s.shouldRun(job, now) {
		t.Fatal("expected invalid schedule to not run")
	}

	job2 := &Job{Schedule: "*/abc", Enabled: true}
	if s.shouldRun(job2, now) {
		t.Fatal("expected invalid interval to not run")
	}
}

func TestLoadJobs(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	lastRun := time.Now()
	records := []store.CronJobRecord{
		{Name: "a", Schedule: "*/5", Prompt: "do a", Enabled: true, LastRun: &lastRun},
		{Name: "b", Schedule: "10:00", Prompt: "do b", Enabled: false},
	}

	s.LoadJobs(records)
	jobs := s.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// Verify fields
	for _, j := range jobs {
		name := j["name"].(string)
		switch name {
		case "a":
			if j["schedule"] != "*/5" {
				t.Errorf("expected schedule */5, got %v", j["schedule"])
			}
			if j["last_run"] == nil {
				t.Error("expected last_run to be set for job a")
			}
		case "b":
			if j["enabled"] != false {
				t.Errorf("expected enabled=false, got %v", j["enabled"])
			}
		default:
			t.Errorf("unexpected job: %s", name)
		}
	}
}

func TestLoadJobsPreservesLastRun(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	lastRun := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	s.AddJob(Job{Name: "j1", Schedule: "*/5", Prompt: "p", Enabled: true})
	// LoadJobs replaces jobs
	s.LoadJobs([]store.CronJobRecord{
		{Name: "j1", Schedule: "*/10", Prompt: "p2", Enabled: true, LastRun: &lastRun},
	})

	jobs := s.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0]["schedule"] != "*/10" {
		t.Errorf("expected schedule */10, got %v", jobs[0]["schedule"])
	}
}

func TestRecords(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	s.AddJob(Job{Name: "j1", Schedule: "*/5", Prompt: "p1", Enabled: true})
	s.AddJob(Job{Name: "j2", Schedule: "10:00", Prompt: "p2", Enabled: false})

	records := s.Records("main")
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	names := map[string]bool{}
	for _, r := range records {
		names[r.Name] = true
		if r.AgentID != "main" {
			t.Errorf("expected agentID=main, got %s", r.AgentID)
		}
	}
	if !names["j1"] || !names["j2"] {
		t.Errorf("expected both j1 and j2, got %v", names)
	}
}

func TestMultipleJobs(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	s.AddJob(Job{Name: "a", Schedule: "*/5", Prompt: "pa", Enabled: true})
	s.AddJob(Job{Name: "b", Schedule: "*/10", Prompt: "pb", Enabled: true})
	s.AddJob(Job{Name: "c", Schedule: "12:00", Prompt: "pc", Enabled: false})

	jobs := s.ListJobs()
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}
}

func TestListJobsFields(t *testing.T) {
	s := NewScheduler(&agent.Agent{ID: "main"}, runtime.NewDispatcher(), nil)
	lastRun := time.Date(2025, 6, 1, 8, 0, 0, 0, time.UTC)
	s.AddJob(Job{Name: "j", Schedule: "*/5", Prompt: "p", Enabled: true, LastRun: &lastRun})

	jobs := s.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j["name"] != "j" {
		t.Errorf("expected name=j, got %v", j["name"])
	}
	if j["schedule"] != "*/5" {
		t.Errorf("expected schedule=*/5, got %v", j["schedule"])
	}
	if j["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", j["enabled"])
	}
	if j["last_run"] == nil {
		t.Error("expected last_run to be set")
	}
}
