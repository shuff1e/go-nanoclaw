// Package hooks provides an event-driven lifecycle automation system.
package hooks

import (
	"context"
	"log/slog"
	"sort"
	"sync"
)

// Standard event types used throughout NanoClaw.
const (
	EventSessionCreated   = "nanoclaw.session.opened"
	EventSessionReset     = "nanoclaw.session.cleared"
	EventMessageReceived  = "nanoclaw.input.received"
	EventMessageSent      = "nanoclaw.output.sent"
	EventToolExecuted     = "nanoclaw.tool.completed"
	EventToolError        = "nanoclaw.tool.failed"
	EventCheckAlert       = "nanoclaw.check.alert"
	EventCheckIdle        = "nanoclaw.check.idle"
	EventCronExecuted     = "nanoclaw.schedule.ran"
	EventAgentSpawned     = "nanoclaw.agent.delegated"
	EventContextCompacted = "nanoclaw.context.summarized"
	EventReflectionDone   = "nanoclaw.memory.reflected"
	EventRuntime          = "nanoclaw.runtime.event"
)

const (
	EventHeartbeatAlert = EventCheckAlert
	EventHeartbeatOK    = EventCheckIdle
)

// HookHandler is a function that handles a hook event.
type HookHandler func(ctx context.Context, event HookEvent) error

// HookEvent is an event that triggers hooks.
type HookEvent struct {
	Type    string
	Payload map[string]any
	Source  string
}

// Hook is a registered event handler.
type Hook struct {
	Name        string
	EventType   string // "*" for global
	Handler     HookHandler
	Description string
	Priority    int // lower = runs first, default 100
}

// EventBus is the central event bus for hook-based communication.
type EventBus struct {
	mu          sync.RWMutex
	hooks       map[string][]Hook
	globalHooks []Hook
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		hooks: make(map[string][]Hook),
	}
}

// Register adds a hook to the event bus.
func (eb *EventBus) Register(hook Hook) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if hook.EventType == "*" {
		eb.globalHooks = append(eb.globalHooks, hook)
		sort.Slice(eb.globalHooks, func(i, j int) bool {
			return eb.globalHooks[i].Priority < eb.globalHooks[j].Priority
		})
	} else {
		eb.hooks[hook.EventType] = append(eb.hooks[hook.EventType], hook)
		sort.Slice(eb.hooks[hook.EventType], func(i, j int) bool {
			return eb.hooks[hook.EventType][i].Priority < eb.hooks[hook.EventType][j].Priority
		})
	}
	slog.Info("Hook registered", "name", hook.Name, "event_type", hook.EventType)
}

// Unregister removes a hook by name.
func (eb *EventBus) Unregister(name string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for eventType, hooks := range eb.hooks {
		filtered := hooks[:0]
		for _, h := range hooks {
			if h.Name != name {
				filtered = append(filtered, h)
			}
		}
		eb.hooks[eventType] = filtered
	}

	filtered := eb.globalHooks[:0]
	for _, h := range eb.globalHooks {
		if h.Name != name {
			filtered = append(filtered, h)
		}
	}
	eb.globalHooks = filtered
}

// Emit triggers all matching hooks for the given event.
func (eb *EventBus) Emit(ctx context.Context, event HookEvent) {
	eb.mu.RLock()
	handlers := make([]Hook, len(eb.globalHooks))
	copy(handlers, eb.globalHooks)
	if typed, ok := eb.hooks[event.Type]; ok {
		handlers = append(handlers, typed...)
	}
	eb.mu.RUnlock()

	sort.Slice(handlers, func(i, j int) bool {
		return handlers[i].Priority < handlers[j].Priority
	})

	for _, hook := range handlers {
		if err := hook.Handler(ctx, event); err != nil {
			slog.Error("Hook failed", "name", hook.Name, "event_type", event.Type, "error", err)
		}
	}
}

// ListHooks returns information about all registered hooks.
func (eb *EventBus) ListHooks() []map[string]any {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	var all []Hook
	all = append(all, eb.globalHooks...)
	for _, hooks := range eb.hooks {
		all = append(all, hooks...)
	}

	result := make([]map[string]any, 0, len(all))
	for _, h := range all {
		result = append(result, map[string]any{
			"name":        h.Name,
			"event_type":  h.EventType,
			"description": h.Description,
			"priority":    h.Priority,
		})
	}
	return result
}
