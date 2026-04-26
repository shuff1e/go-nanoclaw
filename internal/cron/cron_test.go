package cron

import (
	"context"
	"testing"

	"go-nanoclaw/internal/agent"
	"go-nanoclaw/internal/runtime"
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
