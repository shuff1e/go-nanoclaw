// Package gateway implements the central control plane for NanoClaw.
package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go-nanoclaw/internal/agent"
	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/cron"
	"go-nanoclaw/internal/heartbeat"
	"go-nanoclaw/internal/hooks"
	mclog "go-nanoclaw/internal/log"
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
}

const autoRetryDelay = 200 * time.Millisecond

// NewGateway creates a new Gateway.
func NewGateway(cfg *config.Config) *Gateway {
	gw := &Gateway{
		Config:         cfg,
		Orchestrator:   agent.NewOrchestrator(cfg),
		Events:         hooks.NewEventBus(),
		heartbeats:     make(map[string]*heartbeat.Heartbeat),
		cronSchedulers: make(map[string]*cron.Scheduler),
		runningTasks:   make(map[string]context.CancelFunc),
		dispatcher:     mcRuntime.NewDispatcher(),
		store:          store.NewFSStore(cfg.ConfigDir),
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

// Stop stops all periodic checks and cron schedulers.
func (gw *Gateway) Stop() {
	gw.running = false
	for _, hb := range gw.heartbeats {
		hb.Stop()
	}
	for _, scheduler := range gw.cronSchedulers {
		scheduler.Stop()
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

// HandleInput processes user input through the specified agent.
func (gw *Gateway) HandleInput(ctx context.Context, text, agentID string) (string, error) {
	result, err := gw.HandleInputDetailed(ctx, text, agentID, agentID, "gateway")
	if err != nil {
		return "", err
	}
	return result.Response, nil
}

func (gw *Gateway) HandleInputDetailed(ctx context.Context, text, agentID, sessionID, source string) (*HandleResult, error) {
	return gw.HandleInputModeDetailed(ctx, text, agentID, sessionID, source, mcRuntime.ModeDirect)
}

func (gw *Gateway) HandleInputModeDetailed(ctx context.Context, text, agentID, sessionID, source string, mode mcRuntime.ExecutionMode) (*HandleResult, error) {
	reqNum := atomic.AddInt64(&gw.requestCount, 1)
	inputPreview := text
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:47] + "..."
	}
	mclog.Banner("📨", fmt.Sprintf("Gateway 收到请求 #%d", reqNum),
		fmt.Sprintf("Agent: %s | 输入: \"%s\"", agentID, inputPreview),
	)

	gw.Events.Emit(ctx, hooks.HookEvent{
		Type:    hooks.EventMessageReceived,
		Payload: map[string]any{"text": text, "agent_id": agentID},
		Source:  "gateway",
	})

	if strings.TrimSpace(text) == "" {
		err := mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing input text")
		atomic.AddInt64(&gw.errorCount, 1)
		return nil, err
	}

	execCtx := mcRuntime.NewExecution(agentID, sessionID, source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mode
	slog.Info("Gateway execution created",
		append(mcRuntime.LogAttrs(execCtx), "mode", execCtx.Mode)...,
	)
	return gw.executeInput(ctx, execCtx, text, agentID)
}

func (gw *Gateway) HandleInputModeAsyncDetailed(ctx context.Context, text, agentID, sessionID, source string, mode mcRuntime.ExecutionMode, maxRetries int) (*AsyncHandleResult, error) {
	reqNum := atomic.AddInt64(&gw.requestCount, 1)
	inputPreview := text
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:47] + "..."
	}
	mclog.Banner("📨", fmt.Sprintf("Gateway 收到异步请求 #%d", reqNum),
		fmt.Sprintf("Agent: %s | 输入: \"%s\"", agentID, inputPreview),
	)

	if strings.TrimSpace(text) == "" {
		err := mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing input text")
		atomic.AddInt64(&gw.errorCount, 1)
		return nil, err
	}

	execCtx := mcRuntime.NewExecution(agentID, sessionID, source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mode
	if mcRuntime.IsPlannedMode(execCtx.Mode) {
		execCtx.Plan = mcRuntime.BuildTaskPlan(execCtx, text)
		execCtx.Plan.MaxRetries = max(maxRetries, 0)
	}
	execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	slog.Info("Gateway async execution queued",
		append(mcRuntime.LogAttrs(execCtx), "mode", execCtx.Mode)...,
	)
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "queued", nil)
	gw.persistTrace(execCtx, "gateway", "queued", "queued", nil, nil)

	go func() {
		if _, err := gw.executeInput(context.Background(), execCtx, text, agentID); err != nil {
			slog.Warn("Async execution finished with error", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
			gw.maybeAutoRetry(agentID, execCtx, err)
		}
	}()

	return &AsyncHandleResult{
		RequestID:  execCtx.IDs.RequestID,
		TaskID:     execCtx.IDs.TaskID,
		AgentID:    execCtx.AgentID,
		Attempt:    1,
		MaxRetries: maxRetries,
		Status:     "queued",
	}, nil
}

func (gw *Gateway) executeInput(ctx context.Context, execCtx *mcRuntime.ExecutionContext, text, agentID string) (*HandleResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = mcRuntime.WithExecutionContext(runCtx, execCtx)
	if execCtx.OnPlanUpdate == nil {
		execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	}
	if execCtx.OnRuntimeEvent == nil {
		execCtx.OnRuntimeEvent = func(span, event, status string, metadata map[string]any, err error) {
			gw.persistTrace(execCtx, span, event, status, metadata, err)
		}
	}
	gw.registerTask(execCtx, cancel)
	defer gw.unregisterTask(execCtx)
	gw.persistExecution(execCtx, "started", nil)
	gw.persistTrace(execCtx, "gateway", "started", "started", nil, nil)

	result, err := gw.runtime.Process(ctx, execCtx, text, agentID)
	if err != nil {
		if mcRuntime.CodeOf(err) == mcRuntime.CodeApprovalRequired {
			gw.persistPlan(execCtx)
			gw.persistExecution(execCtx, "awaiting_approval", err)
			gw.persistTrace(execCtx, "gateway", "awaiting_approval", "awaiting_approval", nil, err)
			return nil, err
		}
		if mcRuntime.CodeOf(err) == mcRuntime.CodeCancelled {
			gw.persistPlan(execCtx)
			gw.persistExecution(execCtx, "cancelled", err)
			gw.persistTrace(execCtx, "gateway", "cancelled", "cancelled", nil, err)
			return nil, err
		}
		atomic.AddInt64(&gw.errorCount, 1)
		gw.persistPlan(execCtx)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "gateway", "failed", "failed", nil, err)
		return nil, err
	}

	gw.deliver(result.TargetAgentID, result.Response)
	gw.saveSession(result.TargetAgentID, text, result.Response)

	gw.Events.Emit(ctx, hooks.HookEvent{
		Type:    hooks.EventMessageSent,
		Payload: map[string]any{"agent_id": result.TargetAgentID, "response_length": len(result.Response), "request_id": execCtx.IDs.RequestID},
		Source:  "gateway",
	})
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "completed", nil)
	gw.persistTrace(execCtx, "gateway", "completed", "completed", map[string]any{"response_length": len(result.Response)}, nil)

	return &HandleResult{
		RequestID: execCtx.IDs.RequestID,
		TaskID:    execCtx.IDs.TaskID,
		AgentID:   result.TargetAgentID,
		Response:  result.Response,
	}, nil
}

func (gw *Gateway) RetryTaskAsync(ctx context.Context, agentID, requestID, taskID string, maxRetriesOverride int) (*AsyncHandleResult, error) {
	planRecord, err := gw.findPlanRecord(agentID, requestID, taskID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(planRecord.Goal) == "" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "plan has no goal to retry")
	}

	execCtx := mcRuntime.NewExecution(agentID, planRecord.SessionID, planRecord.Source)
	gw.applyExecutionBudget(execCtx)
	execCtx.Mode = mcRuntime.ExecutionMode(planRecord.Mode)
	if execCtx.Mode == "" {
		execCtx.Mode = mcRuntime.ModePlanExecute
	}
	execCtx.Plan = runtimePlanFromRecord(execCtx, planRecord)
	if maxRetriesOverride >= 0 {
		execCtx.Plan.MaxRetries = maxRetriesOverride
	}
	execCtx.OnPlanUpdate = func() { gw.persistPlan(execCtx) }
	gw.persistPlan(execCtx)
	gw.persistExecution(execCtx, "queued", nil)
	gw.persistTrace(execCtx, "gateway", "queued", "queued", nil, nil)

	go func(goal string) {
		if _, runErr := gw.executeInput(context.Background(), execCtx, goal, agentID); runErr != nil {
			slog.Warn("Retried async execution finished with error", append([]any{"error", runErr}, mcRuntime.LogAttrs(execCtx)...)...)
			gw.maybeAutoRetry(agentID, execCtx, runErr)
		}
	}(planRecord.Goal)

	return &AsyncHandleResult{
		RequestID:  execCtx.IDs.RequestID,
		TaskID:     execCtx.IDs.TaskID,
		AgentID:    execCtx.AgentID,
		Attempt:    execCtx.Plan.Attempt,
		MaxRetries: execCtx.Plan.MaxRetries,
		Status:     "queued",
	}, nil
}

func (gw *Gateway) maybeAutoRetry(agentID string, execCtx *mcRuntime.ExecutionContext, err error) {
	if execCtx == nil || execCtx.Plan == nil {
		return
	}
	if !mcRuntime.Retryable(err) {
		return
	}
	if execCtx.Plan.MaxRetries <= 0 {
		return
	}
	if execCtx.Plan.Attempt > execCtx.Plan.MaxRetries {
		return
	}
	slog.Info("Scheduling automatic retry",
		append(mcRuntime.LogAttrs(execCtx),
			"attempt", execCtx.Plan.Attempt,
			"max_retries", execCtx.Plan.MaxRetries,
			"delay_ms", autoRetryDelay.Milliseconds(),
		)...,
	)
	go func() {
		timer := time.NewTimer(autoRetryDelay)
		defer timer.Stop()
		<-timer.C
		if _, retryErr := gw.RetryTaskAsync(context.Background(), agentID, execCtx.IDs.RequestID, execCtx.IDs.TaskID, execCtx.Plan.MaxRetries); retryErr != nil {
			slog.Warn("Automatic retry scheduling failed", append([]any{"error", retryErr}, mcRuntime.LogAttrs(execCtx)...)...)
		}
	}()
}

func (gw *Gateway) registerTask(execCtx *mcRuntime.ExecutionContext, cancel context.CancelFunc) {
	if execCtx == nil || cancel == nil {
		return
	}
	gw.taskMu.Lock()
	defer gw.taskMu.Unlock()
	if execCtx.IDs.RequestID != "" {
		gw.runningTasks["request:"+execCtx.IDs.RequestID] = cancel
	}
	if execCtx.IDs.TaskID != "" {
		gw.runningTasks["task:"+execCtx.IDs.TaskID] = cancel
	}
}

func (gw *Gateway) unregisterTask(execCtx *mcRuntime.ExecutionContext) {
	if execCtx == nil {
		return
	}
	gw.taskMu.Lock()
	defer gw.taskMu.Unlock()
	if execCtx.IDs.RequestID != "" {
		delete(gw.runningTasks, "request:"+execCtx.IDs.RequestID)
	}
	if execCtx.IDs.TaskID != "" {
		delete(gw.runningTasks, "task:"+execCtx.IDs.TaskID)
	}
}

func (gw *Gateway) CancelTask(requestID, taskID string) error {
	gw.taskMu.RLock()
	defer gw.taskMu.RUnlock()
	if requestID != "" {
		if cancel := gw.runningTasks["request:"+requestID]; cancel != nil {
			cancel()
			return nil
		}
	}
	if taskID != "" {
		if cancel := gw.runningTasks["task:"+taskID]; cancel != nil {
			cancel()
			return nil
		}
	}
	return mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "task not running")
}

func (gw *Gateway) runningTaskCount() int {
	gw.taskMu.RLock()
	defer gw.taskMu.RUnlock()
	return len(gw.runningTasks) / 2
}

// Health returns health status for monitoring.
func (gw *Gateway) Health() map[string]any {
	uptime := float64(0)
	if !gw.startTime.IsZero() {
		uptime = time.Since(gw.startTime).Seconds()
	}

	agentsInfo := make(map[string]any)
	for _, aid := range gw.Orchestrator.ListAgents() {
		a, err := gw.Orchestrator.GetOrCreateAgent(aid)
		if err != nil {
			continue
		}
		agentsInfo[aid] = map[string]any{
			"context_messages": a.Context.HistoryLen(),
			"turn_count":       a.TurnCount,
			"skills":           len(a.SkillRegistry.ListSkills()),
		}
	}

	periodicCheckIDs := make([]string, 0)
	for id := range gw.heartbeats {
		periodicCheckIDs = append(periodicCheckIDs, id)
	}

	cronIDs := make([]string, 0)
	for id := range gw.cronSchedulers {
		cronIDs = append(cronIDs, id)
	}

	status := "stopped"
	if gw.running {
		status = "healthy"
	}

	return map[string]any{
		"status":          status,
		"uptime_seconds":  round(uptime, 1),
		"config_version":  gw.Config.ConfigVersion,
		"config_hash":     gw.Config.Fingerprint(),
		"requests":        atomic.LoadInt64(&gw.requestCount),
		"errors":          atomic.LoadInt64(&gw.errorCount),
		"agents":          agentsInfo,
		"periodic_checks": periodicCheckIDs,
		"heartbeats":      periodicCheckIDs,
		"cron_schedulers": cronIDs,
		"hooks":           len(gw.Events.ListHooks()),
		"channels":        len(gw.messageHandlers),
		"timestamp":       time.Now().Format(time.RFC3339),
	}
}

func (gw *Gateway) Liveness() HealthStatus {
	status := "stopped"
	if gw.running {
		status = "alive"
	}
	return HealthStatus{
		Status: status,
		Checks: map[string]any{
			"process": true,
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
}

func (gw *Gateway) Readiness() HealthStatus {
	status := "ready"
	storeReady := gw.store != nil
	storeError := ""
	if storeReady {
		if err := gw.store.HealthCheck(); err != nil {
			storeReady = false
			storeError = err.Error()
		}
	}
	if !gw.running || !storeReady {
		status = "not_ready"
	}
	checks := map[string]any{
		"gateway_running": gw.running,
		"store_ready":     storeReady,
		"config_version":  gw.Config.ConfigVersion,
		"config_hash":     gw.Config.Fingerprint(),
	}
	if storeError != "" {
		checks["store_error"] = storeError
	}
	return HealthStatus{
		Status:    status,
		Checks:    checks,
		Timestamp: time.Now().Format(time.RFC3339),
	}
}

func (gw *Gateway) Metrics() string {
	var buf bytes.Buffer
	healthy := 0
	if gw.running {
		healthy = 1
	}
	runningTasks := gw.runningTaskCount()

	buf.WriteString("# TYPE nanoclaw_gateway_up gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_gateway_up %d\n", healthy)
	buf.WriteString("# TYPE nanoclaw_requests_total counter\n")
	fmt.Fprintf(&buf, "nanoclaw_requests_total %d\n", atomic.LoadInt64(&gw.requestCount))
	buf.WriteString("# TYPE nanoclaw_errors_total counter\n")
	fmt.Fprintf(&buf, "nanoclaw_errors_total %d\n", atomic.LoadInt64(&gw.errorCount))
	buf.WriteString("# TYPE nanoclaw_running_tasks gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_running_tasks %d\n", runningTasks)
	buf.WriteString("# TYPE nanoclaw_agents gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_agents %d\n", len(gw.Config.Agents))
	buf.WriteString("# TYPE nanoclaw_heartbeats gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_heartbeats %d\n", len(gw.heartbeats))
	buf.WriteString("# TYPE nanoclaw_cron_schedulers gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_cron_schedulers %d\n", len(gw.cronSchedulers))
	buf.WriteString("# TYPE nanoclaw_channels gauge\n")
	fmt.Fprintf(&buf, "nanoclaw_channels %d\n", len(gw.messageHandlers))

	agentIDs := make([]string, 0, len(gw.Config.Agents))
	for agentID := range gw.Config.Agents {
		agentIDs = append(agentIDs, agentID)
	}
	sort.Strings(agentIDs)
	buf.WriteString("# TYPE nanoclaw_agent_context_messages gauge\n")
	buf.WriteString("# TYPE nanoclaw_agent_turn_count gauge\n")
	buf.WriteString("# TYPE nanoclaw_agent_skills gauge\n")
	buf.WriteString("# TYPE nanoclaw_task_status gauge\n")
	buf.WriteString("# TYPE nanoclaw_task_attempt gauge\n")
	buf.WriteString("# TYPE nanoclaw_retried_tasks_total gauge\n")
	buf.WriteString("# TYPE nanoclaw_auto_retryable_tasks gauge\n")
	buf.WriteString("# TYPE nanoclaw_brain_calls_total counter\n")
	buf.WriteString("# TYPE nanoclaw_tool_calls_total counter\n")
	buf.WriteString("# TYPE nanoclaw_input_tokens_total counter\n")
	buf.WriteString("# TYPE nanoclaw_output_tokens_total counter\n")
	buf.WriteString("# TYPE nanoclaw_tool_audit_total counter\n")
	brainCallTotals := make(map[string]int)
	toolCallTotals := make(map[string]int)
	inputTokenTotals := make(map[string]int)
	outputTokenTotals := make(map[string]int)
	toolAuditTotals := make(map[string]int)
	for _, agentID := range agentIDs {
		a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
		if err != nil {
			continue
		}
		fmt.Fprintf(&buf, "nanoclaw_agent_context_messages{agent_id=%q} %d\n", agentID, a.Context.HistoryLen())
		fmt.Fprintf(&buf, "nanoclaw_agent_turn_count{agent_id=%q} %d\n", agentID, a.TurnCount)
		fmt.Fprintf(&buf, "nanoclaw_agent_skills{agent_id=%q} %d\n", agentID, len(a.SkillRegistry.ListSkills()))
		tasks, err := gw.TaskViews(agentID, 0, ExecutionLogFilter{})
		if err != nil {
			continue
		}
		summary := gw.SummarizeTaskViews(tasks)
		for status, count := range summary.ByStatus {
			fmt.Fprintf(&buf, "nanoclaw_task_status{agent_id=%q,status=%q} %d\n", agentID, status, count)
		}
		for attempt, count := range summary.ByAttempt {
			fmt.Fprintf(&buf, "nanoclaw_task_attempt{agent_id=%q,attempt=%q} %d\n", agentID, attempt, count)
		}
		fmt.Fprintf(&buf, "nanoclaw_retried_tasks_total{agent_id=%q} %d\n", agentID, summary.RetriedTotal)
		fmt.Fprintf(&buf, "nanoclaw_auto_retryable_tasks{agent_id=%q} %d\n", agentID, summary.AutoRetryable)

		executionLogs, err := gw.ExecutionLogs(agentID, 0)
		if err == nil {
			for _, log := range executionLogs {
				if log.Status != "completed" && log.Status != "failed" && log.Status != "cancelled" {
					continue
				}
				modelKey := metricKey(agentID, log.Provider, log.Model)
				brainCallTotals[modelKey] += log.BrainCalls
				inputTokenTotals[modelKey] += log.InputTokens
				outputTokenTotals[modelKey] += log.OutputTokens
				toolCallTotals[agentID] += log.ToolCalls
			}
		}

		toolLogs, err := gw.ToolAuditLogs(agentID, 0)
		if err == nil {
			for _, log := range toolLogs {
				toolAuditTotals[toolMetricKey(agentID, log.ToolName, log.Status)]++
			}
		}
	}

	for key, count := range brainCallTotals {
		agentID, provider, model := splitMetricKey(key)
		fmt.Fprintf(&buf, "nanoclaw_brain_calls_total{agent_id=%q,provider=%q,model=%q} %d\n", agentID, provider, model, count)
	}
	for agentID, count := range toolCallTotals {
		fmt.Fprintf(&buf, "nanoclaw_tool_calls_total{agent_id=%q} %d\n", agentID, count)
	}
	for key, count := range inputTokenTotals {
		agentID, provider, model := splitMetricKey(key)
		fmt.Fprintf(&buf, "nanoclaw_input_tokens_total{agent_id=%q,provider=%q,model=%q} %d\n", agentID, provider, model, count)
	}
	for key, count := range outputTokenTotals {
		agentID, provider, model := splitMetricKey(key)
		fmt.Fprintf(&buf, "nanoclaw_output_tokens_total{agent_id=%q,provider=%q,model=%q} %d\n", agentID, provider, model, count)
	}
	for key, count := range toolAuditTotals {
		agentID, toolName, status := splitToolMetricKey(key)
		fmt.Fprintf(&buf, "nanoclaw_tool_audit_total{agent_id=%q,tool_name=%q,status=%q} %d\n", agentID, toolName, status, count)
	}

	return buf.String()
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

func (gw *Gateway) ExecutionLogs(agentID string, limit int) ([]store.ExecutionLog, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadExecutionLogs(agentID, limit)
}

func (gw *Gateway) FilteredExecutionLogs(agentID string, limit int, filter ExecutionLogFilter) ([]store.ExecutionLog, error) {
	logs, err := gw.ExecutionLogs(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ExecutionLogFilter{}) {
		return logs, nil
	}
	var filtered []store.ExecutionLog
	for _, log := range logs {
		if filter.SessionID != "" && log.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && log.RequestID != filter.RequestID {
			continue
		}
		if filter.Status != "" && log.Status != filter.Status {
			continue
		}
		if filter.Source != "" && log.Source != filter.Source {
			continue
		}
		if !filter.Since.IsZero() && log.StartedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && log.StartedAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered, nil
}

func (gw *Gateway) SummarizeExecutionLogs(logs []store.ExecutionLog) ExecutionLogSummary {
	summary := ExecutionLogSummary{
		Total:    len(logs),
		ByStatus: make(map[string]int),
		BySource: make(map[string]int),
	}
	for _, log := range logs {
		summary.ByStatus[log.Status]++
		summary.BySource[log.Source]++
	}
	return summary
}

func (gw *Gateway) SummarizeToolAuditLogs(logs []store.ToolAuditLog) ToolAuditSummary {
	summary := ToolAuditSummary{
		Total:    len(logs),
		ByStatus: make(map[string]int),
		ByTool:   make(map[string]int),
	}
	for _, log := range logs {
		summary.ByStatus[log.Status]++
		summary.ByTool[log.ToolName]++
	}
	return summary
}

func (gw *Gateway) PlanRecords(agentID string, limit int) ([]store.PlanRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadPlans(agentID, limit)
}

func (gw *Gateway) findPlanRecord(agentID, requestID, taskID string) (*store.PlanRecord, error) {
	plans, err := gw.PlanRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	var latest *store.PlanRecord
	for i := range plans {
		plan := plans[i]
		if requestID != "" && plan.RequestID != requestID {
			continue
		}
		if taskID != "" && plan.TaskID != taskID {
			continue
		}
		if latest == nil || plan.UpdatedAt.After(latest.UpdatedAt) {
			copyPlan := plan
			latest = &copyPlan
		}
	}
	if latest == nil {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "plan not found")
	}
	return latest, nil
}

func (gw *Gateway) TaskViews(agentID string, limit int, filter ExecutionLogFilter) ([]TaskView, error) {
	eventFilter := filter
	eventFilter.Status = ""
	logs, err := gw.FilteredExecutionLogs(agentID, 0, eventFilter)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return nil, nil
	}

	taskMap := make(map[string]*TaskView)
	order := make([]string, 0)
	for _, log := range logs {
		key := log.RequestID
		if key == "" {
			key = log.TaskID
		}
		if key == "" {
			continue
		}

		view, ok := taskMap[key]
		if !ok {
			view = &TaskView{
				RequestID:   log.RequestID,
				TaskID:      log.TaskID,
				SessionID:   log.SessionID,
				AgentID:     log.AgentID,
				Source:      log.Source,
				Status:      log.Status,
				StartedAt:   log.StartedAt,
				LastEventAt: log.StartedAt,
			}
			taskMap[key] = view
			order = append(order, key)
		}

		view.EventCount++
		if view.RequestID == "" {
			view.RequestID = log.RequestID
		}
		if view.TaskID == "" {
			view.TaskID = log.TaskID
		}
		if view.SessionID == "" {
			view.SessionID = log.SessionID
		}
		if view.AgentID == "" {
			view.AgentID = log.AgentID
		}
		if view.Source == "" {
			view.Source = log.Source
		}
		if log.StartedAt.Before(view.StartedAt) || view.StartedAt.IsZero() {
			view.StartedAt = log.StartedAt
		}
		if log.StartedAt.After(view.LastEventAt) || view.LastEventAt.IsZero() {
			view.LastEventAt = log.StartedAt
		}
		if log.Error != "" {
			view.Error = log.Error
		}

		switch log.Status {
		case "failed":
			view.Status = "failed"
			view.FailedAt = log.StartedAt
		case "cancelled":
			if view.Status != "failed" {
				view.Status = "cancelled"
			}
			view.CancelledAt = log.StartedAt
		case "awaiting_approval":
			if view.Status != "failed" && view.Status != "cancelled" {
				view.Status = "awaiting_approval"
			}
		case "completed":
			if view.Status != "failed" && view.Status != "cancelled" && view.Status != "awaiting_approval" {
				view.Status = "completed"
			}
			view.CompletedAt = log.StartedAt
		case "started":
			if view.Status == "" {
				view.Status = "running"
			}
			if view.Status == "started" {
				view.Status = "running"
			}
		default:
			if view.Status == "" {
				view.Status = log.Status
			}
		}
	}

	plans, err := gw.PlanRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	planMap := make(map[string]store.PlanRecord, len(plans))
	for _, plan := range plans {
		key := plan.RequestID
		if key == "" {
			key = plan.TaskID
		}
		if key == "" {
			continue
		}
		existing, ok := planMap[key]
		if !ok || plan.UpdatedAt.After(existing.UpdatedAt) {
			planMap[key] = plan
		}
	}

	tasks := make([]TaskView, 0, len(taskMap))
	for _, key := range order {
		view := *taskMap[key]
		if plan, ok := planMap[key]; ok {
			view.RetryOfRequestID = plan.RetryOfRequestID
			view.RetryOfTaskID = plan.RetryOfTaskID
			view.Attempt = plan.Attempt
			view.MaxRetries = plan.MaxRetries
			view.Mode = plan.Mode
			view.Plan = &plan
		}
		if filter.Status != "" && view.Status != filter.Status {
			continue
		}
		tasks = append(tasks, view)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks, nil
}

func (gw *Gateway) SummarizeTaskViews(tasks []TaskView) TaskViewSummary {
	summary := TaskViewSummary{
		Total:     len(tasks),
		ByStatus:  make(map[string]int),
		BySource:  make(map[string]int),
		ByAttempt: make(map[string]int),
	}
	for _, task := range tasks {
		summary.ByStatus[task.Status]++
		summary.BySource[task.Source]++
		attempt := task.Attempt
		if attempt <= 0 {
			attempt = 1
		}
		summary.ByAttempt[fmt.Sprintf("%d", attempt)]++
		if attempt > summary.MaxAttempt {
			summary.MaxAttempt = attempt
		}
		if task.RetryOfRequestID != "" || task.RetryOfTaskID != "" {
			summary.RetriedTotal++
		}
		if task.MaxRetries > 0 {
			summary.AutoRetryable++
		}
	}
	return summary
}

func (gw *Gateway) ToolAuditLogs(agentID string, limit int) ([]store.ToolAuditLog, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadToolAuditLogs(agentID, limit)
}

func (gw *Gateway) FilteredToolAuditLogs(agentID string, limit int, filter ToolAuditLogFilter) ([]store.ToolAuditLog, error) {
	logs, err := gw.ToolAuditLogs(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ToolAuditLogFilter{}) {
		return logs, nil
	}
	var filtered []store.ToolAuditLog
	for _, log := range logs {
		if filter.SessionID != "" && log.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && log.RequestID != filter.RequestID {
			continue
		}
		if filter.TraceID != "" && log.TraceID != filter.TraceID {
			continue
		}
		if filter.Status != "" && log.Status != filter.Status {
			continue
		}
		if filter.ToolName != "" && log.ToolName != filter.ToolName {
			continue
		}
		if !filter.Since.IsZero() && log.OccurredAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && log.OccurredAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered, nil
}

func (gw *Gateway) ApprovalRecords(agentID string, limit int) ([]store.ApprovalRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadApprovalRecords(agentID, limit)
}

func (gw *Gateway) FilteredApprovalRecords(agentID string, limit int, filter ApprovalRecordFilter) ([]store.ApprovalRecord, error) {
	records, err := gw.ApprovalRecords(agentID, limit)
	if err != nil {
		return nil, err
	}
	if filter == (ApprovalRecordFilter{}) {
		return records, nil
	}
	var filtered []store.ApprovalRecord
	for _, record := range records {
		if filter.SessionID != "" && record.SessionID != filter.SessionID {
			continue
		}
		if filter.RequestID != "" && record.RequestID != filter.RequestID {
			continue
		}
		if filter.TraceID != "" && record.TraceID != filter.TraceID {
			continue
		}
		if filter.TaskID != "" && record.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && record.Status != filter.Status {
			continue
		}
		if filter.ToolName != "" && record.ToolName != filter.ToolName {
			continue
		}
		if !filter.Since.IsZero() && record.RequestedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && record.RequestedAt.After(filter.Until) {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered, nil
}

func (gw *Gateway) DecideApproval(agentID, approvalID, decision, decidedBy string) (*store.ApprovalRecord, error) {
	approvalID = strings.TrimSpace(approvalID)
	decision = strings.TrimSpace(decision)
	if approvalID == "" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "missing approval_id")
	}
	if decision != "approved" && decision != "rejected" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "decision must be approved or rejected")
	}
	records, err := gw.ApprovalRecords(agentID, 0)
	if err != nil {
		return nil, err
	}
	var latest *store.ApprovalRecord
	for i := range records {
		record := records[i]
		if record.ApprovalID != approvalID {
			continue
		}
		if latest == nil || record.RequestedAt.After(latest.RequestedAt) || record.DecidedAt.After(latest.DecidedAt) {
			copyRecord := record
			latest = &copyRecord
		}
	}
	if latest == nil {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "approval not found")
	}
	if latest.Status != "pending" {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "approval already decided")
	}
	if timeout := gw.approvalTimeout(agentID); timeout > 0 && time.Since(latest.RequestedAt) > timeout {
		expired := *latest
		expired.Status = "expired"
		expired.DecidedAt = time.Now().UTC()
		expired.DecidedBy = strings.TrimSpace(decidedBy)
		if expired.DecidedBy == "" {
			expired.DecidedBy = "api"
		}
		if err := gw.store.SaveApprovalRecord(agentID, expired); err != nil {
			return nil, err
		}
		execCtx := approvalExecutionContext(agentID, expired)
		gw.applyExecutionBudget(execCtx)
		err := mcRuntime.Errorf(mcRuntime.CodeApprovalRequired, "approval expired for tool %s", expired.ToolName)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "approval", "expired", "expired", map[string]any{"approval_id": approvalID, "tool_name": expired.ToolName}, err)
		return nil, err
	}
	decided := *latest
	decided.Status = decision
	decided.DecidedAt = time.Now().UTC()
	decided.DecidedBy = strings.TrimSpace(decidedBy)
	if decided.DecidedBy == "" {
		decided.DecidedBy = "api"
	}
	if err := gw.store.SaveApprovalRecord(agentID, decided); err != nil {
		return nil, err
	}
	execCtx := approvalExecutionContext(agentID, decided)
	gw.applyExecutionBudget(execCtx)
	if decision == "rejected" {
		err := mcRuntime.Errorf(mcRuntime.CodeApprovalRequired, "approval rejected for tool %s", decided.ToolName)
		gw.persistExecution(execCtx, "failed", err)
		gw.persistTrace(execCtx, "approval", "rejected", "rejected", map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, err)
		return &decided, nil
	}
	a, err := gw.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		wrapped := mcRuntime.Wrap(mcRuntime.CodeInternal, err, "get agent '%s'", agentID)
		gw.persistExecution(execCtx, "failed", wrapped)
		return nil, wrapped
	}
	gw.configureAgentStorage(agentID, a)
	execCtx.Stats.ToolCalls = 1
	toolResult, toolErr := a.Hands.ExecuteApproved(mcRuntime.WithExecutionContext(context.Background(), execCtx), decided.ToolName, decided.Arguments)
	if toolErr != nil {
		gw.persistExecution(execCtx, "failed", toolErr)
		gw.persistTrace(execCtx, "approval", "approved_tool_failed", string(toolResult.Status), map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, toolErr)
		return nil, toolErr
	}
	gw.persistExecution(execCtx, "completed", nil)
	gw.persistTrace(execCtx, "approval", "approved_tool_completed", string(toolResult.Status), map[string]any{"approval_id": approvalID, "tool_name": decided.ToolName}, nil)
	return &decided, nil
}

func (gw *Gateway) approvalTimeout(agentID string) time.Duration {
	if gw.Config == nil {
		return 0
	}
	def, err := gw.Config.GetAgent(agentID)
	if err != nil || def.ToolPolicies.ApprovalTimeoutMin <= 0 {
		return 0
	}
	return time.Duration(def.ToolPolicies.ApprovalTimeoutMin) * time.Minute
}

func approvalExecutionContext(agentID string, approval store.ApprovalRecord) *mcRuntime.ExecutionContext {
	return &mcRuntime.ExecutionContext{
		IDs: mcRuntime.ExecutionIDs{
			TraceID:   approval.TraceID,
			RequestID: approval.RequestID,
			SessionID: approval.SessionID,
			TaskID:    approval.TaskID,
		},
		AgentID:   agentID,
		Source:    "approval",
		StartedAt: time.Now(),
		Mode:      mcRuntime.ModeDirect,
	}
}

func (gw *Gateway) SessionEntries(agentID string, limit int) ([]store.SessionEntry, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadRecentSessionEntries(agentID, limit)
}

func (gw *Gateway) MemoryRecords(agentID string, limit int) ([]store.MemoryRecord, error) {
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadMemoryRecords(agentID, limit)
}

func (gw *Gateway) CronJobs(agentID string) ([]store.CronJobRecord, error) {
	if scheduler, ok := gw.cronSchedulers[agentID]; ok {
		return scheduler.Records(agentID), nil
	}
	if gw.store == nil {
		return nil, nil
	}
	return gw.store.LoadCronJobs(agentID)
}

func (gw *Gateway) TraceEvents(agentID string, limit int, filter TraceEventFilter) ([]store.TraceEvent, error) {
	if gw.store == nil {
		return nil, nil
	}
	events, err := gw.store.LoadTraceEvents(agentID, limit)
	if err != nil {
		return nil, err
	}
	filtered := make([]store.TraceEvent, 0, len(events))
	for _, event := range events {
		if filter.TraceID != "" && event.TraceID != filter.TraceID {
			continue
		}
		if filter.RequestID != "" && event.RequestID != filter.RequestID {
			continue
		}
		if filter.SessionID != "" && event.SessionID != filter.SessionID {
			continue
		}
		if filter.Span != "" && event.Span != filter.Span {
			continue
		}
		if filter.Event != "" && event.Event != filter.Event {
			continue
		}
		if filter.Status != "" && event.Status != filter.Status {
			continue
		}
		if !filter.Since.IsZero() && event.At.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && event.At.After(filter.Until) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered, nil
}

func (gw *Gateway) persistExecution(execCtx *mcRuntime.ExecutionContext, status string, err error) {
	if gw.store == nil || execCtx == nil {
		return
	}
	log := store.ExecutionLog{
		TraceID:      execCtx.IDs.TraceID,
		RequestID:    execCtx.IDs.RequestID,
		SessionID:    execCtx.IDs.SessionID,
		TaskID:       execCtx.IDs.TaskID,
		AgentID:      execCtx.AgentID,
		Source:       execCtx.Source,
		Status:       status,
		Provider:     execCtx.Provider,
		Model:        execCtx.Model,
		InputTokens:  execCtx.Stats.InputTokens,
		OutputTokens: execCtx.Stats.OutputTokens,
		BrainCalls:   execCtx.Stats.BrainCalls,
		ToolCalls:    execCtx.Stats.ToolCalls,
		StartedAt:    execCtx.StartedAt,
	}
	if err != nil {
		log.Error = err.Error()
	}
	if saveErr := gw.store.SaveExecutionLog(log); saveErr != nil {
		slog.Error("Persist execution log failed", append([]any{"error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func (gw *Gateway) persistTrace(execCtx *mcRuntime.ExecutionContext, span, event, status string, metadata map[string]any, err error) {
	if gw.store == nil || execCtx == nil {
		return
	}
	runtimeEvent := mcRuntime.NewRuntimeEvent(execCtx, runtimeEventType(event, status), span, status, metadata, err)
	record := store.TraceEvent{
		TraceID:   execCtx.IDs.TraceID,
		RequestID: execCtx.IDs.RequestID,
		SessionID: execCtx.IDs.SessionID,
		TaskID:    execCtx.IDs.TaskID,
		AgentID:   execCtx.AgentID,
		Source:    execCtx.Source,
		Span:      span,
		Event:     event,
		Status:    status,
		Metadata:  metadata,
		At:        runtimeEvent.At,
	}
	if err != nil {
		record.Error = err.Error()
	}
	if saveErr := gw.store.SaveTraceEvent(execCtx.AgentID, record); saveErr != nil {
		slog.Error("Persist trace event failed", append([]any{"error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
	if gw.Events != nil {
		gw.Events.Emit(context.Background(), hooks.HookEvent{
			Type:    hooks.EventRuntime,
			Source:  "runtime",
			Payload: map[string]any{"event": runtimeEvent},
		})
	}
}

func runtimeEventType(event, status string) mcRuntime.EventType {
	switch event {
	case "queued":
		return mcRuntime.EventRunQueued
	case "started":
		return mcRuntime.EventRunStarted
	case "thinking":
		return mcRuntime.EventRunThinking
	case "planning":
		return mcRuntime.EventRunPlanning
	case "replanning":
		return mcRuntime.EventRunReplanning
	case "step_started":
		return mcRuntime.EventRunStepStarted
	case "step_completed":
		return mcRuntime.EventRunStepCompleted
	case "step_failed":
		return mcRuntime.EventRunStepFailed
	case "step_skipped":
		return mcRuntime.EventRunStepSkipped
	case "verifying":
		return mcRuntime.EventRunVerifying
	case "tool_started":
		return mcRuntime.EventToolStarted
	case "tool_completed":
		return mcRuntime.EventToolCompleted
	case "tool_failed":
		return mcRuntime.EventToolFailed
	case "completed":
		return mcRuntime.EventRunCompleted
	case "failed":
		return mcRuntime.EventRunFailed
	case "cancelled":
		return mcRuntime.EventRunCancelled
	case "awaiting_approval":
		return mcRuntime.EventRunAwaitingApproval
	case "rejected":
		return mcRuntime.EventApprovalRejected
	case "expired":
		return mcRuntime.EventApprovalExpired
	case "approved_tool_completed":
		return mcRuntime.EventApprovalToolCompleted
	case "approved_tool_failed":
		return mcRuntime.EventApprovalToolFailed
	default:
		if status == "failed" {
			return mcRuntime.EventRunFailed
		}
		return mcRuntime.EventType(event)
	}
}

func (gw *Gateway) persistPlan(execCtx *mcRuntime.ExecutionContext) {
	if gw.store == nil || execCtx == nil || execCtx.Plan == nil {
		return
	}
	plan := store.PlanRecord{
		RequestID:        execCtx.Plan.RequestID,
		TaskID:           execCtx.Plan.TaskID,
		SessionID:        execCtx.Plan.SessionID,
		AgentID:          execCtx.Plan.AgentID,
		Source:           execCtx.Plan.Source,
		RetryOfRequestID: execCtx.Plan.RetryOfRequestID,
		RetryOfTaskID:    execCtx.Plan.RetryOfTaskID,
		Attempt:          execCtx.Plan.Attempt,
		MaxRetries:       execCtx.Plan.MaxRetries,
		Mode:             string(execCtx.Plan.Mode),
		Goal:             execCtx.Plan.Goal,
		Status:           string(execCtx.Plan.Status),
		Summary:          execCtx.Plan.Summary,
		GeneratedAt:      execCtx.Plan.GeneratedAt,
		UpdatedAt:        execCtx.Plan.UpdatedAt,
		Steps:            make([]store.PlanStepRecord, 0, len(execCtx.Plan.Steps)),
		ChildTasks:       make([]store.PlanChildTaskRecord, 0, len(execCtx.Plan.ChildTasks)),
	}
	for _, step := range execCtx.Plan.Steps {
		plan.Steps = append(plan.Steps, store.PlanStepRecord{
			ID:              step.ID,
			Title:           step.Title,
			Prompt:          step.Prompt,
			DependsOn:       step.DependsOn,
			RetryPolicy:     step.RetryPolicy,
			FailureStrategy: step.FailureStrategy,
			Checkpoint:      step.Checkpoint,
			Status:          string(step.Status),
			Result:          step.Result,
			Error:           step.Error,
			StartedAt:       step.StartedAt,
			CompletedAt:     step.CompletedAt,
		})
	}
	for _, child := range execCtx.Plan.ChildTasks {
		plan.ChildTasks = append(plan.ChildTasks, store.PlanChildTaskRecord{
			ID:          child.ID,
			AgentID:     child.AgentID,
			Prompt:      child.Prompt,
			Status:      string(child.Status),
			Result:      child.Result,
			Error:       child.Error,
			StartedAt:   child.StartedAt,
			CompletedAt: child.CompletedAt,
		})
	}
	if err := gw.store.SavePlan(execCtx.AgentID, plan); err != nil {
		slog.Error("Persist plan failed", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func runtimePlanFromRecord(execCtx *mcRuntime.ExecutionContext, record *store.PlanRecord) *mcRuntime.TaskPlan {
	if record == nil {
		return nil
	}
	plan := &mcRuntime.TaskPlan{
		RequestID:        execCtx.IDs.RequestID,
		TaskID:           execCtx.IDs.TaskID,
		SessionID:        execCtx.IDs.SessionID,
		AgentID:          execCtx.AgentID,
		Source:           execCtx.Source,
		RetryOfRequestID: record.RequestID,
		RetryOfTaskID:    record.TaskID,
		Attempt:          max(record.Attempt+1, 2),
		MaxRetries:       record.MaxRetries,
		Mode:             mcRuntime.ExecutionMode(record.Mode),
		Goal:             record.Goal,
		Status:           mcRuntime.StepStatus(record.Status),
		Summary:          record.Summary,
		GeneratedAt:      time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
		Steps:            make([]mcRuntime.TaskStep, 0, len(record.Steps)),
		ChildTasks:       make([]mcRuntime.ChildTask, 0, len(record.ChildTasks)),
	}
	for _, step := range record.Steps {
		plan.Steps = append(plan.Steps, mcRuntime.TaskStep{
			ID:              step.ID,
			Title:           step.Title,
			Prompt:          step.Prompt,
			DependsOn:       step.DependsOn,
			RetryPolicy:     step.RetryPolicy,
			FailureStrategy: step.FailureStrategy,
			Checkpoint:      step.Checkpoint,
			Status:          mcRuntime.StepStatus(step.Status),
			Result:          step.Result,
			Error:           step.Error,
			StartedAt:       step.StartedAt,
			CompletedAt:     step.CompletedAt,
		})
	}
	for _, child := range record.ChildTasks {
		plan.ChildTasks = append(plan.ChildTasks, mcRuntime.ChildTask{
			ID:          child.ID,
			AgentID:     child.AgentID,
			Prompt:      child.Prompt,
			Status:      mcRuntime.StepStatus(child.Status),
			Result:      child.Result,
			Error:       child.Error,
			StartedAt:   child.StartedAt,
			CompletedAt: child.CompletedAt,
		})
	}
	return plan
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func round(f float64, places int) float64 {
	shift := 1.0
	for i := 0; i < places; i++ {
		shift *= 10
	}
	return float64(int(f*shift+0.5)) / shift
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func metricKey(agentID, provider, model string) string {
	return strings.Join([]string{agentID, provider, model}, "\x1f")
}

func splitMetricKey(key string) (string, string, string) {
	parts := strings.SplitN(key, "\x1f", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func toolMetricKey(agentID, toolName, status string) string {
	return strings.Join([]string{agentID, toolName, status}, "\x1f")
}

func splitToolMetricKey(key string) (string, string, string) {
	parts := strings.SplitN(key, "\x1f", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}
