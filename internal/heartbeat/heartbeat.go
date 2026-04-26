// Package heartbeat runs scheduled check cycles for agents.
package heartbeat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go-nanoclaw/internal/agent"
	mclog "go-nanoclaw/internal/log"
	mcRuntime "go-nanoclaw/internal/runtime"
)

const IdleMarker = "HEARTBEAT_OK"

// AlertHandler is called when a check cycle produces an alert.
type AlertHandler func(response string)

// Heartbeat runs periodic check cycles for an agent.
type Heartbeat struct {
	Agent      *agent.Agent
	Interval   time.Duration
	dispatcher *mcRuntime.Dispatcher
	onAlert    AlertHandler
	cancel     context.CancelFunc
}

// New creates a new Heartbeat.
func New(a *agent.Agent, intervalMinutes int, dispatcher *mcRuntime.Dispatcher) *Heartbeat {
	return &Heartbeat{
		Agent:      a,
		Interval:   time.Duration(intervalMinutes) * time.Minute,
		dispatcher: dispatcher,
	}
}

// SetAlertHandler sets the callback for check alerts.
func (h *Heartbeat) SetAlertHandler(handler AlertHandler) {
	h.onAlert = handler
}

// Start begins the check loop in a goroutine.
func (h *Heartbeat) Start(ctx context.Context) {
	ctx, h.cancel = context.WithCancel(ctx)
	go h.loop(ctx)
	slog.Info("Check loop started", "interval_min", int(h.Interval.Minutes()))
}

// Stop stops the check loop.
func (h *Heartbeat) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	slog.Info("Check loop stopped")
}

func (h *Heartbeat) loop(ctx context.Context) {
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := h.Tick(ctx); err != nil {
				slog.Error("Check loop error", "error", err)
			}
		}
	}
}

// Tick executes one periodic check cycle. Returns the response text, or empty if no action is due.
func (h *Heartbeat) Tick(ctx context.Context) (string, error) {
	mclog.Section("CheckCycle.Tick", "agent_id", h.Agent.ID)
	if !h.isActiveHours() {
		slog.Debug("Check cycle skipped: outside active hours")
		return "", nil
	}

	heartbeatMD, err := h.Agent.Memory.ReadFile("HEARTBEAT.md")
	if err != nil || strings.TrimSpace(heartbeatMD) == "" {
		slog.Debug("Check cycle skipped: instructions empty")
		return "", nil
	}

	prompt := "Read HEARTBEAT.md from your context.\n" +
		"Report only work that needs attention now; do not revive stale chat context.\n" +
		"When no action is due, respond with HEARTBEAT_OK."

	execCtx := mcRuntime.NewExecution(h.Agent.ID, h.Agent.ID+":heartbeat", "heartbeat")
	execCtx.Budget.MaxWallClock = time.Minute
	var response string
	err = h.dispatcher.Run(mcRuntime.WithExecutionContext(ctx, execCtx), execCtx, func(runCtx context.Context) error {
		var runErr error
		response, runErr = h.Agent.ProcessExecution(runCtx, execCtx, prompt)
		return runErr
	})
	if err != nil {
		slog.Error("Check cycle execution failed", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx))...)
		return "", fmt.Errorf("check cycle process: %w", err)
	}

	if strings.Contains(response, IdleMarker) {
		slog.Debug("Check cycle idle")
		return "", nil
	}

	slog.Info("Check cycle alert", append([]any{"response", response[:min(200, len(response))]}, mcRuntime.LogAttrs(execCtx))...)

	if h.onAlert != nil {
		h.onAlert(response)
	}

	return response, nil
}

func (h *Heartbeat) isActiveHours() bool {
	cfg := h.Agent.AgentDef.Heartbeat
	now := time.Now()

	startH, startM, err1 := parseTime(cfg.ActiveHoursStart)
	endH, endM, err2 := parseTime(cfg.ActiveHoursEnd)
	if err1 != nil || err2 != nil {
		return true
	}

	start := time.Date(now.Year(), now.Month(), now.Day(), startH, startM, 0, 0, now.Location())
	end := time.Date(now.Year(), now.Month(), now.Day(), endH, endM, 0, 0, now.Location())

	if start.Before(end) || start.Equal(end) {
		return !now.Before(start) && !now.After(end)
	}
	// Crosses midnight
	return !now.Before(start) || !now.After(end)
}

func parseTime(s string) (int, int, error) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	return h, m, err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
