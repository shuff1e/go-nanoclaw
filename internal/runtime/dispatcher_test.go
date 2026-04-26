package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcherSerializesSameAgent(t *testing.T) {
	dispatcher := NewDispatcher()
	execCtx := NewExecution("main", "session-1", "test")

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64

	runFn := func() {
		err := dispatcher.Run(context.Background(), execCtx, func(ctx context.Context) error {
			cur := concurrent.Add(1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			concurrent.Add(-1)
			return nil
		})
		if err != nil {
			t.Errorf("dispatcher run failed: %v", err)
		}
	}

	go runFn()
	<-entered
	go runFn()

	select {
	case <-entered:
		t.Fatal("second execution entered before first finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	time.Sleep(50 * time.Millisecond)

	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected max concurrency 1, got %d", maxConcurrent.Load())
	}
}

func TestDispatcherAllowsDifferentAgents(t *testing.T) {
	dispatcher := NewDispatcher()
	execCtx1 := NewExecution("agent-a", "session-a", "test")
	execCtx2 := NewExecution("agent-b", "session-b", "test")

	entered := make(chan struct{}, 2)
	release := make(chan struct{})

	runFn := func(execCtx *ExecutionContext) {
		err := dispatcher.Run(context.Background(), execCtx, func(ctx context.Context) error {
			entered <- struct{}{}
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("dispatcher run failed: %v", err)
		}
	}

	go runFn(execCtx1)
	go runFn(execCtx2)

	timeout := time.After(200 * time.Millisecond)
	count := 0
	for count < 2 {
		select {
		case <-entered:
			count++
		case <-timeout:
			t.Fatal("expected different agents to run concurrently")
		}
	}

	close(release)
}

func TestDispatcherSerializesDifferentSessionsOnSameAgent(t *testing.T) {
	dispatcher := NewDispatcher()
	execCtx1 := NewExecution("same-agent", "session-a", "test")
	execCtx2 := NewExecution("same-agent", "session-b", "test")

	entered := make(chan struct{}, 2)
	release := make(chan struct{})

	runFn := func(execCtx *ExecutionContext) {
		err := dispatcher.Run(context.Background(), execCtx, func(ctx context.Context) error {
			entered <- struct{}{}
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("dispatcher run failed: %v", err)
		}
	}

	go runFn(execCtx1)
	<-entered
	go runFn(execCtx2)

	select {
	case <-entered:
		t.Fatal("different sessions on same agent should be serialized")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
}
