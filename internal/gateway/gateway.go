// Package gateway implements the central control plane for NanoClaw.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go-nanoclaw/internal/agent"
	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/cron"
	"go-nanoclaw/internal/heartbeat"
	"go-nanoclaw/internal/hooks"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

// MessageHandler is called when an agent produces a response.
type MessageHandler func(agentID, response string)

type HandleResult struct {
	RequestID string
	TaskID    string
	AgentID   string
	Response  string
}

type AsyncHandleResult struct {
	RequestID  string `json:"request_id"`
	TaskID     string `json:"task_id"`
	AgentID    string `json:"agent_id"`
	Attempt    int    `json:"attempt"`
	MaxRetries int    `json:"max_retries,omitempty"`
	Status     string `json:"status"`
}

type HealthStatus struct {
	Status    string         `json:"status"`
	Checks    map[string]any `json:"checks"`
	Timestamp string         `json:"timestamp"`
}

type ExecutionLogFilter struct {
	SessionID string
	RequestID string
	Status    string
	Source    string
	Since     time.Time
	Until     time.Time
}

type ToolAuditLogFilter struct {
	SessionID string
	RequestID string
	TraceID   string
	Status    string
	ToolName  string
	Since     time.Time
	Until     time.Time
}

type ApprovalRecordFilter struct {
	SessionID string
	RequestID string
	TraceID   string
	TaskID    string
	Status    string
	ToolName  string
	Since     time.Time
	Until     time.Time
}

type TraceEventFilter struct {
	TraceID   string
	RequestID string
	SessionID string
	Span      string
	Event     string
	Status    string
	Since     time.Time
	Until     time.Time
}

type ExecutionLogSummary struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	BySource map[string]int `json:"by_source"`
}

type ToolAuditSummary struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByTool   map[string]int `json:"by_tool"`
}

type TaskView struct {
	RequestID        string            `json:"request_id"`
	TaskID           string            `json:"task_id"`
	SessionID        string            `json:"session_id"`
	AgentID          string            `json:"agent_id"`
	Source           string            `json:"source"`
	RetryOfRequestID string            `json:"retry_of_request_id,omitempty"`
	RetryOfTaskID    string            `json:"retry_of_task_id,omitempty"`
	Attempt          int               `json:"attempt,omitempty"`
	MaxRetries       int               `json:"max_retries,omitempty"`
	Mode             string            `json:"mode,omitempty"`
	Status           string            `json:"status"`
	Error            string            `json:"error,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	LastEventAt      time.Time         `json:"last_event_at"`
	EventCount       int               `json:"event_count"`
	CompletedAt      time.Time         `json:"completed_at,omitempty"`
	FailedAt         time.Time         `json:"failed_at,omitempty"`
	CancelledAt      time.Time         `json:"cancelled_at,omitempty"`
	Plan             *store.PlanRecord `json:"plan,omitempty"`
}

type TaskViewSummary struct {
	Total         int            `json:"total"`
	ByStatus      map[string]int `json:"by_status"`
	BySource      map[string]int `json:"by_source"`
	ByAttempt     map[string]int `json:"by_attempt"`
	RetriedTotal  int            `json:"retried_total"`
	MaxAttempt    int            `json:"max_attempt"`
	AutoRetryable int            `json:"auto_retryable"`
}

// Gateway is the central control plane coordinating agents, scheduled checks, and channels.
type Gateway struct {
	Config       *config.Config
	Orchestrator *agent.Orchestrator
	Events       *hooks.EventBus

	heartbeats      map[string]*heartbeat.Heartbeat
	cronSchedulers  map[string]*cron.Scheduler
	taskMu          sync.RWMutex
	runningTasks    map[string]context.CancelFunc
	messageMu       sync.RWMutex
	messageHandlers []MessageHandler
	dispatcher      *mcRuntime.Dispatcher
	store           store.Store
	runtime         agentRuntime
	running         bool
	startTime       time.Time
	requestCount    int64
	errorCount      int64
	rootCtx         context.Context
	rootCancel      context.CancelFunc
}

const autoRetryDelay = 200 * time.Millisecond

// NewGateway creates a new Gateway.
func NewGateway(cfg *config.Config) *Gateway {
	ctx, cancel := context.WithCancel(context.Background())
	gw := &Gateway{
		Config:         cfg,
		Orchestrator:   agent.NewOrchestrator(cfg),
		Events:         hooks.NewEventBus(),
		heartbeats:     make(map[string]*heartbeat.Heartbeat),
		cronSchedulers: make(map[string]*cron.Scheduler),
		runningTasks:   make(map[string]context.CancelFunc),
		dispatcher:     mcRuntime.NewDispatcher(),
		store:          store.NewFSStore(cfg.ConfigDir),
		rootCtx:        ctx,
		rootCancel:     cancel,
	}
	gw.runtime = &nativeAgentRuntime{gateway: gw}
	return gw
}

func (gw *Gateway) applyExecutionBudget(execCtx *mcRuntime.ExecutionContext) {
	if execCtx == nil || gw.Config == nil {
		return
	}
	if gw.Config.MaxWallClockSec > 0 {
		execCtx.Budget.MaxWallClock = time.Duration(gw.Config.MaxWallClockSec) * time.Second
		execCtx.Deadline = execCtx.StartedAt.Add(execCtx.Budget.MaxWallClock)
	}
	if gw.Config.MaxToolRounds > 0 {
		execCtx.Budget.MaxToolRounds = gw.Config.MaxToolRounds
	}
	if gw.Config.MaxToolCalls > 0 {
		execCtx.Budget.MaxToolCalls = gw.Config.MaxToolCalls
	}
	if gw.Config.MaxToolOutputBytes > 0 {
		execCtx.Budget.MaxToolOutputBytes = gw.Config.MaxToolOutputBytes
	}
}

// Start initializes all agents, periodic checks, and cron schedulers.
func (gw *Gateway) Start(ctx context.Context) error {
	gw.running = true
	gw.startTime = time.Now()

	// Derive root context from caller's context so both can cancel
	gw.rootCtx, gw.rootCancel = context.WithCancel(ctx)

	if err := os.MkdirAll(gw.Config.ConfigDir, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	for agentID, agentDef := range gw.Config.Agents {
		a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
		if err != nil {
			return fmt.Errorf("create agent '%s': %w", agentID, err)
		}
		gw.configureAgentStorage(agentID, a)
		a.Memory.CreateDefaults()
		gw.restoreSession(agentID, a)
		gw.restoreCronJobs(agentID)
		gw.registerCronTools(agentID)

		if agentDef.Heartbeat.Enabled {
			hb := heartbeat.New(a, agentDef.Heartbeat.IntervalMinutes, gw.dispatcher)
			aid := agentID
			hb.SetAlertHandler(func(response string) {
				gw.deliver(aid, response)
				gw.Events.Emit(ctx, hooks.HookEvent{
					Type:    hooks.EventCheckAlert,
					Payload: map[string]any{"agent_id": aid, "alert": response[:min(200, len(response))]},
					Source:  "periodic_check",
				})
			})
			gw.heartbeats[agentID] = hb
			hb.Start(ctx)
		}

		for _, cj := range agentDef.CronJobs {
			if cj.Name != "" && cj.Schedule != "" && cj.Prompt != "" {
				gw.AddCronJob(ctx, agentID, cron.Job{
					Name:     cj.Name,
					Schedule: cj.Schedule,
					Prompt:   cj.Prompt,
					Enabled:  true,
				})
			}
		}
	}

	for _, scheduler := range gw.cronSchedulers {
		scheduler.Start(ctx)
	}

	slog.Info("Gateway started",
		"agents", len(gw.Config.Agents),
		"periodic_checks", len(gw.heartbeats),
		"cron_schedulers", len(gw.cronSchedulers),
	)
	return nil
}

func (gw *Gateway) registerCronTools(agentID string) {
	a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		return
	}

	aid := agentID
	a.Hands.RegisterTool("schedule_task", func(ctx context.Context, args map[string]any) (string, error) {
		name, _ := args["name"].(string)
		schedule, _ := args["schedule"].(string)
		prompt, _ := args["prompt"].(string)
		gw.AddCronJob(ctx, aid, cron.Job{
			Name:     name,
			Schedule: schedule,
			Prompt:   prompt,
			Enabled:  true,
		})
		return fmt.Sprintf("Scheduled '%s' for %s. Trigger prompt: %s",
			name, schedule, truncate(prompt, 100)), nil
	}, brain.ToolSchema{
		Name:        "schedule_task",
		Description: "Register an agent job that runs on a daily time or fixed-minute interval.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Stable job identifier"},
				"schedule": map[string]any{"type": "string", "description": "Timing expression: HH:MM daily or */N every N minutes"},
				"prompt":   map[string]any{"type": "string", "description": "Instruction sent to the agent at run time"},
			},
			"required": []string{"name", "schedule", "prompt"},
		},
	})

	a.Hands.RegisterTool("list_schedules", func(ctx context.Context, args map[string]any) (string, error) {
		scheduler, ok := gw.cronSchedulers[aid]
		if !ok {
			return "No cron jobs scheduled.", nil
		}
		jobs := scheduler.ListJobs()
		if len(jobs) == 0 {
			return "No cron jobs scheduled.", nil
		}
		var lines []string
		for _, j := range jobs {
			lines = append(lines, fmt.Sprintf("- %s: schedule=%s, enabled=%v, last_run=%v",
				j["name"], j["schedule"], j["enabled"], j["last_run"]))
		}
		return strings.Join(lines, "\n"), nil
	}, brain.ToolSchema{
		Name:        "list_schedules",
		Description: "Return configured scheduled jobs and their latest run state.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	})
}

// AddCronJob adds a cron job for a specific agent.
func (gw *Gateway) AddCronJob(ctx context.Context, agentID string, job cron.Job) {
	a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		return
	}
	needStart := false
	if _, ok := gw.cronSchedulers[agentID]; !ok {
		scheduler := cron.NewScheduler(a, gw.dispatcher, gw.store)
		aid := agentID
		scheduler.SetOutputHandler(func(name, response string) {
			gw.deliver(aid, fmt.Sprintf("[Cron: %s] %s", name, response))
			gw.Events.Emit(ctx, hooks.HookEvent{
				Type:    hooks.EventCronExecuted,
				Payload: map[string]any{"agent_id": aid, "job_name": name},
				Source:  "cron",
			})
		})
		gw.cronSchedulers[agentID] = scheduler
		needStart = gw.running
	}
	gw.cronSchedulers[agentID].AddJob(job)
	if needStart {
		gw.cronSchedulers[agentID].Start(ctx)
	}
}

// Stop stops all periodic checks, cron schedulers, and cancels running tasks.
func (gw *Gateway) Stop() {
	gw.running = false

	// Cancel root context to stop all running tasks
	if gw.rootCancel != nil {
		gw.rootCancel()
	}

	for _, hb := range gw.heartbeats {
		hb.Stop()
	}
	for _, scheduler := range gw.cronSchedulers {
		scheduler.Stop()
	}

	// Wait for running tasks to finish (with timeout)
	done := make(chan struct{})
	go func() {
		gw.taskMu.RLock()
		defer gw.taskMu.RUnlock()
		for len(gw.runningTasks) > 0 {
			gw.taskMu.RUnlock()
			time.Sleep(50 * time.Millisecond)
			gw.taskMu.RLock()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		slog.Warn("Timeout waiting for running tasks to complete")
	}

	slog.Info("Gateway stopped")
}

// OnMessage registers a message delivery handler.
func (gw *Gateway) OnMessage(handler MessageHandler) {
	gw.messageMu.Lock()
	defer gw.messageMu.Unlock()
	gw.messageHandlers = append(gw.messageHandlers, handler)
}

// RegisterHook registers a hook on the Gateway's event bus.
func (gw *Gateway) RegisterHook(hook hooks.Hook) {
	gw.Events.Register(hook)
}

// GetPeriodicCheck returns the periodic check loop for an agent, if any.
func (gw *Gateway) GetPeriodicCheck(agentID string) *heartbeat.Heartbeat {
	return gw.heartbeats[agentID]
}

// GetHeartbeat preserves the pre-check-cycle API name for callers that have not migrated yet.
func (gw *Gateway) GetHeartbeat(agentID string) *heartbeat.Heartbeat {
	return gw.GetPeriodicCheck(agentID)
}

func (gw *Gateway) Store() store.Store {
	return gw.store
}

func (gw *Gateway) deliver(agentID, response string) {
	gw.messageMu.RLock()
	handlers := make([]MessageHandler, len(gw.messageHandlers))
	copy(handlers, gw.messageHandlers)
	gw.messageMu.RUnlock()

	for _, handler := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Delivery handler panic", "error", r, "agent_id", agentID)
				}
			}()
			handler(agentID, response)
		}()
	}
}

func (gw *Gateway) saveSession(agentID, userInput, response string) {
	entry := store.SessionEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		User:      truncate(userInput, 1000),
		Assistant: truncate(response, 2000),
	}
	_ = gw.store.SaveSessionEntry(agentID, entry)
}

func (gw *Gateway) restoreSession(agentID string, a *agent.Agent) {
	entries, err := gw.store.LoadRecentSessionEntries(agentID, 20)
	if err != nil {
		return
	}

	restored := 0
	for _, entry := range entries {
		if entry.User != "" {
			a.Context.AddMessage(brain.Message{Role: "user", Content: entry.User})
			restored++
		}
		if entry.Assistant != "" {
			a.Context.AddMessage(brain.Message{Role: "assistant", Content: entry.Assistant})
			restored++
		}
	}

	if restored > 0 {
		slog.Info("Restored session", "agent", agentID, "messages", restored)
	}
}

func (gw *Gateway) restoreCronJobs(agentID string) {
	records, err := gw.store.LoadCronJobs(agentID)
	if err != nil || len(records) == 0 {
		return
	}
	a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		return
	}
	scheduler, ok := gw.cronSchedulers[agentID]
	if !ok {
		scheduler = cron.NewScheduler(a, gw.dispatcher, gw.store)
		gw.cronSchedulers[agentID] = scheduler
	}
	scheduler.LoadJobs(records)
}

func (gw *Gateway) configureAgentStorage(agentID string, a *agent.Agent) {
	if a == nil || gw.store == nil {
		return
	}
	a.Memory.SetStore(agentID, gw.store)
	a.Hands.SetStore(agentID, gw.store)
}

