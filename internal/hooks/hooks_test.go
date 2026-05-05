package hooks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegisterAndEmitTyped(t *testing.T) {
	eb := NewEventBus()
	var called int
	eb.Register(Hook{
		Name:      "test-hook",
		EventType: EventMessageReceived,
		Handler: func(ctx context.Context, event HookEvent) error {
			called++
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if called != 1 {
		t.Fatalf("expected handler called once, got %d", called)
	}
}

func TestEmitOnlyMatchesEventType(t *testing.T) {
	eb := NewEventBus()
	var called int
	eb.Register(Hook{
		Name:      "hook-a",
		EventType: EventMessageReceived,
		Handler: func(ctx context.Context, event HookEvent) error {
			called++
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventToolExecuted})
	if called != 0 {
		t.Fatalf("expected handler not called, got %d", called)
	}
}

func TestGlobalHookFiresOnAnyEvent(t *testing.T) {
	eb := NewEventBus()
	var count int
	eb.Register(Hook{
		Name:      "global",
		EventType: "*",
		Handler: func(ctx context.Context, event HookEvent) error {
			count++
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	eb.Emit(context.Background(), HookEvent{Type: EventToolExecuted})
	if count != 2 {
		t.Fatalf("expected global hook called twice, got %d", count)
	}
}

func TestPriorityOrdering(t *testing.T) {
	eb := NewEventBus()
	var order []int

	eb.Register(Hook{
		Name:      "low-priority",
		EventType: EventMessageReceived,
		Priority:  200,
		Handler: func(ctx context.Context, event HookEvent) error {
			order = append(order, 2)
			return nil
		},
	})
	eb.Register(Hook{
		Name:      "high-priority",
		EventType: EventMessageReceived,
		Priority:  10,
		Handler: func(ctx context.Context, event HookEvent) error {
			order = append(order, 1)
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected priority order [1,2], got %v", order)
	}
}

func TestGlobalAndTypedHookPriority(t *testing.T) {
	eb := NewEventBus()
	var order []string

	eb.Register(Hook{
		Name:      "global-first",
		EventType: "*",
		Priority:  10,
		Handler: func(ctx context.Context, event HookEvent) error {
			order = append(order, "global")
			return nil
		},
	})
	eb.Register(Hook{
		Name:      "typed-second",
		EventType: EventMessageReceived,
		Priority:  100,
		Handler: func(ctx context.Context, event HookEvent) error {
			order = append(order, "typed")
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if len(order) != 2 || order[0] != "global" || order[1] != "typed" {
		t.Fatalf("expected [global, typed], got %v", order)
	}
}

func TestUnregister(t *testing.T) {
	eb := NewEventBus()
	var called int
	eb.Register(Hook{
		Name:      "removable",
		EventType: EventMessageReceived,
		Handler: func(ctx context.Context, event HookEvent) error {
			called++
			return nil
		},
	})

	eb.Unregister("removable")
	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if called != 0 {
		t.Fatalf("expected handler not called after unregister, got %d", called)
	}
}

func TestUnregisterGlobal(t *testing.T) {
	eb := NewEventBus()
	var called int
	eb.Register(Hook{
		Name:      "global-removable",
		EventType: "*",
		Handler: func(ctx context.Context, event HookEvent) error {
			called++
			return nil
		},
	})

	eb.Unregister("global-removable")
	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if called != 0 {
		t.Fatalf("expected global handler not called after unregister, got %d", called)
	}
}

func TestHookErrorDoesNotBlockOthers(t *testing.T) {
	eb := NewEventBus()
	var secondCalled int
	eb.Register(Hook{
		Name:      "failing",
		EventType: EventMessageReceived,
		Priority:  10,
		Handler: func(ctx context.Context, event HookEvent) error {
			return errors.New("boom")
		},
	})
	eb.Register(Hook{
		Name:      "after-fail",
		EventType: EventMessageReceived,
		Priority:  20,
		Handler: func(ctx context.Context, event HookEvent) error {
			secondCalled++
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
	if secondCalled != 1 {
		t.Fatalf("expected second hook called despite first failing, got %d", secondCalled)
	}
}

func TestListHooks(t *testing.T) {
	eb := NewEventBus()
	eb.Register(Hook{
		Name:        "h1",
		EventType:   EventMessageReceived,
		Description: "test hook",
		Priority:    50,
	})
	eb.Register(Hook{
		Name:        "h2",
		EventType:   "*",
		Description: "global hook",
		Priority:    10,
	})

	list := eb.ListHooks()
	if len(list) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(list))
	}
}

func TestEmitWithPayload(t *testing.T) {
	eb := NewEventBus()
	var received map[string]any
	eb.Register(Hook{
		Name:      "payload-check",
		EventType: EventMessageReceived,
		Handler: func(ctx context.Context, event HookEvent) error {
			received = event.Payload
			return nil
		},
	})

	eb.Emit(context.Background(), HookEvent{
		Type:    EventMessageReceived,
		Payload: map[string]any{"key": "value"},
		Source:  "test",
	})
	if received["key"] != "value" {
		t.Fatalf("expected payload[key]=value, got %v", received)
	}
}

func TestConcurrentEmit(t *testing.T) {
	eb := NewEventBus()
	var count atomic.Int32
	eb.Register(Hook{
		Name:      "concurrent",
		EventType: EventMessageReceived,
		Handler: func(ctx context.Context, event HookEvent) error {
			count.Add(1)
			return nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eb.Emit(context.Background(), HookEvent{Type: EventMessageReceived})
		}()
	}
	wg.Wait()

	if count.Load() != 100 {
		t.Fatalf("expected 100 calls, got %d", count.Load())
	}
}
