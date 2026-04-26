// Package agent implements the core agent loop and multi-agent orchestration.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	mcContext "go-nanoclaw/internal/context"
	"go-nanoclaw/internal/hands"
	mclog "go-nanoclaw/internal/log"
	"go-nanoclaw/internal/memory"
	"go-nanoclaw/internal/router"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/skills"
)

// Agent is a single agent instance with its own isolated context.
type Agent struct {
	ID                 string
	Config             *config.Config
	AgentDef           *config.AgentDef
	SpawnDepth         int
	Memory             *memory.Memory
	Brain              brain.Brain
	Hands              *hands.Hands
	SkillRegistry      *skills.Registry
	Router             *router.Router
	Context            *mcContext.Manager
	TurnCount          int64
	ReflectionInterval int

	skillIndex string
}

type requestFrame struct {
	routeResult router.RouteResult
	bootstrap   string
	window      mcContext.Window
	tools       []brain.ToolSchema
}

// NewAgent creates a new Agent.
func NewAgent(agentDef *config.AgentDef, workspace string, cfg *config.Config, spawnDepth int) (*Agent, error) {
	b, err := brain.NewBrain(agentDef.Brain)
	if err != nil {
		return nil, fmt.Errorf("create brain: %w", err)
	}

	mem := memory.New(workspace, cfg.BootstrapMaxChars)
	h := hands.New(workspace, mem, agentDef.ToolPolicies)
	sr := skills.NewRegistry()
	r := router.NewRouter(sr)
	ctx := mcContext.NewManager(cfg.MaxContextChars)

	sr.LoadFromDirectory(filepath.Join(workspace, "skills"))
	skillIndex := sr.BuildIndex()

	a := &Agent{
		ID:                 agentDef.ID,
		Config:             cfg,
		AgentDef:           agentDef,
		SpawnDepth:         spawnDepth,
		Memory:             mem,
		Brain:              b,
		Hands:              h,
		SkillRegistry:      sr,
		Router:             r,
		Context:            ctx,
		ReflectionInterval: 5,
		skillIndex:         skillIndex,
	}

	a.registerSpawnTool()
	return a, nil
}

func (a *Agent) registerSpawnTool() {
	if a.SpawnDepth >= a.AgentDef.MaxSpawnDepth {
		return
	}

	a.Hands.RegisterTool("delegate_task", func(ctx context.Context, args map[string]any) (string, error) {
		task, _ := args["task"].(string)
		agentID, _ := args["agent_id"].(string)
		if agentID == "" {
			agentID = a.ID
		}
		return a.Spawn(ctx, task, agentID)
	}, brain.ToolSchema{
		Name:        "delegate_task",
		Description: "Delegate a bounded task to an allowed agent and return that agent's final output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Self-contained work request for the delegated agent",
				},
				"agent_id": map[string]any{
					"type":        "string",
					"description": "Agent to run the delegated work; defaults to the current agent",
				},
			},
			"required": []string{"task"},
		},
	})
}

// ProcessMessage processes a user message through the full agent loop.
func (a *Agent) ProcessMessage(ctx context.Context, userInput string) (string, error) {
	execCtx := mcRuntime.FromContext(ctx)
	if execCtx == nil {
		execCtx = mcRuntime.NewDetachedExecution(a.ID, a.ID)
	}
	return a.ProcessExecution(ctx, execCtx, userInput)
}

// ProcessExecution processes a user message through the full agent loop.
func (a *Agent) ProcessExecution(ctx context.Context, execCtx *mcRuntime.ExecutionContext, userInput string) (string, error) {
	if execCtx == nil {
		execCtx = mcRuntime.NewDetachedExecution(a.ID, a.ID)
	}
	switch execCtx.Mode {
	case mcRuntime.ModePlanExecute, mcRuntime.ModePlanExecuteVerify:
		return a.processPlannedExecution(ctx, execCtx, userInput)
	case "", mcRuntime.ModeDirect:
		return a.processDirectExecution(ctx, execCtx, userInput)
	default:
		return "", mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "unsupported execution mode: %s", execCtx.Mode)
	}
}

func (a *Agent) processDirectExecution(ctx context.Context, execCtx *mcRuntime.ExecutionContext, userInput string) (string, error) {
	ctx = mcRuntime.WithExecutionContext(ctx, execCtx)
	ctx, cancel := mcRuntime.ContextWithDeadline(ctx, execCtx)
	defer cancel()
	execCtx.Provider = a.AgentDef.Brain.Provider
	execCtx.Model = a.AgentDef.Brain.Model

	mclog.ResetSteps()
	inputPreview := userInput
	if len(inputPreview) > 50 {
		inputPreview = inputPreview[:47] + "..."
	}
	turnNum := atomic.LoadInt64(&a.TurnCount) + 1
	mclog.Banner("🤖", fmt.Sprintf("Agent.ProcessMessage  turn=%d", turnNum),
		fmt.Sprintf("Agent: %s | 输入: \"%s\"", a.ID, inputPreview),
	)
	if execCtx != nil {
		slog.Info("Agent execution started", mcRuntime.LogAttrs(execCtx)...)
	}

	frame := a.prepareRequestFrame(userInput)
	response, err := a.runModelTurn(ctx, execCtx, frame.window, frame.tools, 0)
	if err != nil {
		return "", a.wrapModelTurnError(ctx, err, 0)
	}

	maxToolRounds := execCtx.Budget.MaxToolRounds
	if maxToolRounds <= 0 {
		maxToolRounds = 10
	}
	roundCount := 0
	totalToolCalls := 0

	for len(response.ToolCalls) > 0 && roundCount < maxToolRounds {
		roundCount++

		// ── Step N: Tool Loop ──
		mclog.Narrative(
			fmt.Sprintf("工具循环 第%d轮", roundCount),
			"LLM请求执行工具，执行后将结果反馈给LLM",
		)

		a.Context.AddMessage(brain.Message{
			Role:      "assistant",
			Content:   response.Text,
			ToolCalls: response.ToolCalls,
		})

		for i, tc := range response.ToolCalls {
			totalToolCalls++
			execCtx.Stats.ToolCalls = totalToolCalls
			if execCtx.Budget.MaxToolCalls > 0 && totalToolCalls > execCtx.Budget.MaxToolCalls {
				return "", mcRuntime.Errorf(mcRuntime.CodeTimeout, "tool call budget exceeded")
			}
			isLastTool := i == len(response.ToolCalls)-1
			desc := a.Hands.GetToolDescription(tc.Name)
			if desc == "" {
				desc = "(自定义工具)"
			}
			mclog.Tree(0, isLastTool && false, fmt.Sprintf("执行: %s — %s", tc.Name, desc))

			argsJSON := fmt.Sprintf("%v", tc.Arguments)
			if len(argsJSON) > 120 {
				argsJSON = argsJSON[:117] + "..."
			}
			mclog.Tree(1, false, fmt.Sprintf("参数: %s", argsJSON))

			a.emitRuntimeEvent(execCtx, "tool", "tool_started", "running_tool", map[string]any{
				"tool_name": tc.Name,
				"round":     roundCount,
				"index":     i + 1,
			}, nil)
			toolResult, toolErr := a.Hands.ExecuteStructured(ctx, tc.Name, tc.Arguments)
			result := toolResult.AsMessage()
			if toolErr != nil {
				slog.Warn("Tool execution failed", append([]any{"tool", tc.Name, "error", toolErr}, mcRuntime.LogAttrs(execCtx)...)...)
				a.emitRuntimeEvent(execCtx, "tool", "tool_failed", string(toolResult.Status), map[string]any{
					"tool_name": tc.Name,
					"round":     roundCount,
					"index":     i + 1,
				}, toolErr)
			} else {
				a.emitRuntimeEvent(execCtx, "tool", "tool_completed", string(toolResult.Status), map[string]any{
					"tool_name": tc.Name,
					"round":     roundCount,
					"index":     i + 1,
				}, nil)
			}
			resultPreview := result
			if len(resultPreview) > 100 {
				resultPreview = resultPreview[:97] + "..."
			}
			mclog.Tree(1, true, fmt.Sprintf("结果 (%d chars): %s", len(result), resultPreview))

			a.Context.AddMessage(brain.Message{
				Role:       "tool_result",
				Content:    result,
				ToolCallID: tc.ID,
			})
			if toolResult.Status == hands.ToolStatusApprovalRequired {
				return "", mcRuntime.Errorf(mcRuntime.CodeApprovalRequired, "approval required for tool %s", tc.Name)
			}
		}
		mclog.Tree(0, true, "将工具结果注入上下文，重新调用LLM")

		// ── Tool results are now part of the request frame ──
		mclog.Narrative(
			fmt.Sprintf("模型推理 第%d轮", roundCount),
			"上下文已更新，包含工具执行结果",
		)
		mclog.Tree(0, false, fmt.Sprintf("上下文更新: +%d tool_result 消息", len(response.ToolCalls)))
		mclog.Tree(0, false, "等待响应...")

		frame.window = a.Context.Build(frame.bootstrap, frame.routeResult.ExtraSystemPrompt, a.skillIndex)
		response, err = a.runModelTurn(ctx, execCtx, frame.window, frame.tools, roundCount)
		if err != nil {
			return "", a.wrapModelTurnError(ctx, err, roundCount)
		}
	}

	a.Context.AddMessage(brain.Message{Role: "assistant", Content: response.Text})

	turnCount := atomic.AddInt64(&a.TurnCount, 1)
	mclog.Completion("完成",
		"turn", turnCount,
		"tool_rounds", roundCount,
		"response", fmt.Sprintf("%d chars", len(response.Text)),
	)

	if a.Context.NeedsCompaction() {
		slog.Info("Context reduction requested")
		thinkFn := func(msgs []brain.Message, sysPrompt string) (*brain.BrainResponse, error) {
			return a.Brain.Think(ctx, msgs, sysPrompt, nil)
		}
		a.Context.Compact(thinkFn, frame.bootstrap)
	}

	if turnCount%int64(a.ReflectionInterval) == 0 {
		a.reflect(ctx)
	}

	return response.Text, nil
}

func (a *Agent) prepareRequestFrame(userInput string) requestFrame {
	routeResult := a.Router.Route(userInput, a.ID)
	mclog.Narrative("请求准备", "解析输入、匹配技能并构建模型请求帧")
	mclog.Tree(0, false, "扫描技能触发器 (keyword/regex)...")
	if len(routeResult.MatchedSkills) > 0 {
		names := make([]string, len(routeResult.MatchedSkills))
		for i, s := range routeResult.MatchedSkills {
			names[i] = s.Name
		}
		mclog.Tree(0, false, fmt.Sprintf("匹配技能: %s", strings.Join(names, ", ")))
	} else {
		mclog.Tree(0, false, "匹配技能: (无)")
	}
	mclog.Tree(0, true, fmt.Sprintf("目标Agent: %s (当前Agent)", a.ID))

	a.Context.AddMessage(brain.Message{Role: "user", Content: userInput})

	isFirst := a.Context.HistoryLen() <= 1
	bootstrap := a.Memory.AssembleBootstrap(isFirst)
	window := a.Context.Build(bootstrap, routeResult.ExtraSystemPrompt, a.skillIndex)
	toolSchemas := a.Hands.GetToolSchemas(a.AgentDef.AllowedTools)

	mclog.Narrative("请求帧", "组合运行说明、技能上下文、历史消息和工具能力")
	bootstrapFiles := a.Memory.ListBootstrapFiles(isFirst)
	mclog.Tree(0, false, fmt.Sprintf("工作区说明: %s -> %d chars",
		strings.Join(bootstrapFiles, " + "), len(bootstrap)))

	skillList := a.SkillRegistry.ListSkills()
	if len(skillList) > 0 {
		skillNames := make([]string, len(skillList))
		for i, s := range skillList {
			skillNames[i] = s["name"]
		}
		mclog.Tree(0, false, fmt.Sprintf("技能索引: %d skill(s) [%s]", len(skillList), strings.Join(skillNames, ", ")))
	} else {
		mclog.Tree(0, false, "技能索引: (无已注册技能)")
	}

	history := a.Context.History()
	roleCounts := map[string]int{}
	for _, m := range history {
		roleCounts[m.Role]++
	}
	mclog.Tree(0, false, fmt.Sprintf("历史消息: %d条 (user:%d assistant:%d tool_result:%d)",
		len(history), roleCounts["user"], roleCounts["assistant"], roleCounts["tool_result"]))
	mclog.Tree(0, false, fmt.Sprintf("系统提示词: %d chars", len(window.SystemPrompt)))

	toolNames := make([]string, len(toolSchemas))
	for i, t := range toolSchemas {
		toolNames[i] = t.Name
	}
	mclog.Tree(0, true, fmt.Sprintf("可用工具: %d个 [%s]", len(toolSchemas), strings.Join(toolNames, ", ")))

	return requestFrame{
		routeResult: routeResult,
		bootstrap:   bootstrap,
		window:      window,
		tools:       toolSchemas,
	}
}

func (a *Agent) runModelTurn(ctx context.Context, execCtx *mcRuntime.ExecutionContext, window mcContext.Window, toolSchemas []brain.ToolSchema, round int) (*brain.BrainResponse, error) {
	var toolsParam []brain.ToolSchema
	if len(toolSchemas) > 0 {
		toolsParam = toolSchemas
	}
	mclog.Narrative("模型推理", fmt.Sprintf("Provider: %s", a.AgentDef.Brain.Provider))
	mclog.Tree(0, false, fmt.Sprintf("Model: %s | 最大输出 Token: %d", a.AgentDef.Brain.Model, a.AgentDef.Brain.MaxTokens))
	mclog.Tree(0, false, "等待响应...")

	a.emitRuntimeEvent(execCtx, "brain", "thinking", "thinking", map[string]any{
		"provider": a.AgentDef.Brain.Provider,
		"model":    a.AgentDef.Brain.Model,
		"round":    round,
	}, nil)
	response, err := a.Brain.Think(ctx, window.Messages, window.SystemPrompt, toolsParam)
	if err != nil {
		a.emitRuntimeEvent(execCtx, "brain", "thinking", "failed", map[string]any{"round": round}, err)
		return nil, err
	}
	a.recordBrainUsage(execCtx, response)
	a.emitRuntimeEvent(execCtx, "brain", "thinking", "completed", map[string]any{
		"round":       round,
		"stop_reason": response.StopReason,
		"tool_calls":  len(response.ToolCalls),
	}, nil)
	a.logBrainResponse(response)
	return response, nil
}

func (a *Agent) wrapModelTurnError(ctx context.Context, err error, round int) error {
	if ctx.Err() == context.Canceled {
		return mcRuntime.Wrap(mcRuntime.CodeCancelled, err, "execution cancelled during model turn %d", round)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return mcRuntime.Wrap(mcRuntime.CodeTimeout, err, "execution timed out during model turn %d", round)
	}
	return mcRuntime.Wrap(mcRuntime.CodeBrainFailed, err, "model turn %d", round)
}

func (a *Agent) processPlannedExecution(ctx context.Context, execCtx *mcRuntime.ExecutionContext, userInput string) (string, error) {
	ctx = mcRuntime.WithExecutionContext(ctx, execCtx)
	if execCtx.Plan == nil {
		a.emitRuntimeEvent(execCtx, "planner", "planning", "planning", map[string]any{"goal": truncate(userInput, 200)}, nil)
		execCtx.Plan = a.buildStructuredPlan(ctx, execCtx, userInput)
		a.emitRuntimeEvent(execCtx, "planner", "planning", "completed", map[string]any{"steps": len(execCtx.Plan.Steps)}, nil)
	}
	execCtx.Plan.Status = mcRuntime.StepRunning
	execCtx.Plan.UpdatedAt = time.Now().UTC()
	a.checkpointPlan(execCtx)

	slog.Info("Agent plan generated",
		append(
			mcRuntime.LogAttrs(execCtx),
			"mode", execCtx.Mode,
			"steps", len(execCtx.Plan.Steps),
		)...,
	)

	results := make([]string, 0, len(execCtx.Plan.Steps))
	for i := range execCtx.Plan.Steps {
		step := &execCtx.Plan.Steps[i]
		if step.Status == mcRuntime.StepCompleted {
			if step.Result != "" {
				results = append(results, fmt.Sprintf("%s: %s", step.Title, truncate(step.Result, 200)))
			}
			continue
		}
		if err := validateStepDependencies(execCtx.Plan, *step); err != nil {
			if stepFailureStrategyAllowsSkip(*step) {
				a.skipPlanStep(execCtx, step, i+1, "dependency_not_completed", err)
				continue
			}
			now := time.Now().UTC()
			step.Status = mcRuntime.StepFailed
			step.Error = err.Error()
			step.CompletedAt = now
			step.Checkpoint = fmt.Sprintf("failed:%s:%s", step.ID, now.Format(time.RFC3339Nano))
			execCtx.Plan.Status = mcRuntime.StepFailed
			execCtx.Plan.UpdatedAt = now
			execCtx.Plan.Summary = fmt.Sprintf("Plan failed before %s", step.Title)
			a.checkpointPlan(execCtx)
			a.emitRuntimeEvent(execCtx, "workflow.step", "step_failed", "failed", map[string]any{
				"step_id": step.ID,
				"title":   step.Title,
				"index":   i + 1,
				"reason":  "dependency_not_completed",
			}, err)
			return "", err
		}

		step.Status = mcRuntime.StepRunning
		step.Error = ""
		step.StartedAt = time.Now().UTC()
		step.CompletedAt = time.Time{}
		execCtx.Plan.UpdatedAt = step.StartedAt
		a.checkpointPlan(execCtx)
		a.emitRuntimeEvent(execCtx, "workflow.step", "step_started", "running_tool", map[string]any{
			"step_id": step.ID,
			"title":   step.Title,
			"index":   i + 1,
			"total":   len(execCtx.Plan.Steps),
		}, nil)

		stepPrompt := a.planStepPrompt(userInput, *step, i+1, len(execCtx.Plan.Steps))
		result, err := a.executePlanStepWithRetry(ctx, execCtx, stepPrompt, *step, i+1)
		if err != nil {
			if stepFailureStrategyAllowsSkip(*step) {
				a.skipPlanStep(execCtx, step, i+1, "step_failed", err)
				continue
			}
			step.Status = mcRuntime.StepFailed
			step.Error = err.Error()
			step.CompletedAt = time.Now().UTC()
			step.Checkpoint = fmt.Sprintf("failed:%s:%s", step.ID, step.CompletedAt.Format(time.RFC3339Nano))
			execCtx.Plan.Status = mcRuntime.StepFailed
			execCtx.Plan.UpdatedAt = step.CompletedAt
			execCtx.Plan.Summary = fmt.Sprintf("Plan failed at %s", step.Title)
			a.checkpointPlan(execCtx)
			a.emitRuntimeEvent(execCtx, "workflow.step", "step_failed", "failed", map[string]any{
				"step_id": step.ID,
				"title":   step.Title,
				"index":   i + 1,
			}, err)
			return "", err
		}

		step.Status = mcRuntime.StepCompleted
		step.Result = truncate(result, 400)
		step.CompletedAt = time.Now().UTC()
		step.Checkpoint = fmt.Sprintf("completed:%s:%s", step.ID, step.CompletedAt.Format(time.RFC3339Nano))
		execCtx.Plan.UpdatedAt = step.CompletedAt
		a.checkpointPlan(execCtx)
		a.emitRuntimeEvent(execCtx, "workflow.step", "step_completed", "completed", map[string]any{
			"step_id": step.ID,
			"title":   step.Title,
			"index":   i + 1,
		}, nil)
		results = append(results, fmt.Sprintf("%s: %s", step.Title, truncate(result, 200)))
	}

	execCtx.Plan.Status = mcRuntime.StepCompleted
	execCtx.Plan.UpdatedAt = time.Now().UTC()
	execCtx.Plan.Summary = fmt.Sprintf("Completed %d planned steps", len(execCtx.Plan.Steps))
	a.checkpointPlan(execCtx)

	if execCtx.Mode == mcRuntime.ModePlanExecuteVerify {
		if err := a.verifyPlannedExecution(ctx, execCtx, userInput, results); err != nil {
			return "", err
		}
	}

	if len(results) == 0 {
		return "", nil
	}
	return strings.Join(results, "\n\n"), nil
}

func (a *Agent) skipPlanStep(execCtx *mcRuntime.ExecutionContext, step *mcRuntime.TaskStep, index int, reason string, err error) {
	now := time.Now().UTC()
	step.Status = mcRuntime.StepSkipped
	if err != nil {
		step.Error = err.Error()
	}
	step.CompletedAt = now
	step.Checkpoint = fmt.Sprintf("skipped:%s:%s", step.ID, now.Format(time.RFC3339Nano))
	if execCtx != nil && execCtx.Plan != nil {
		execCtx.Plan.UpdatedAt = now
	}
	a.checkpointPlan(execCtx)
	a.emitRuntimeEvent(execCtx, "workflow.step", "step_skipped", "skipped", map[string]any{
		"step_id": step.ID,
		"title":   step.Title,
		"index":   index,
		"reason":  reason,
		"policy":  step.FailureStrategy,
	}, err)
}

func (a *Agent) executePlanStepWithRetry(ctx context.Context, execCtx *mcRuntime.ExecutionContext, prompt string, step mcRuntime.TaskStep, index int) (string, error) {
	attempts := 1
	if stepRetryEnabled(step) {
		attempts = 2
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := a.processDirectExecution(ctx, execCtx, prompt)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt >= attempts || !mcRuntime.Retryable(err) {
			break
		}
		a.emitRuntimeEvent(execCtx, "workflow.step", "step_failed", "retrying", map[string]any{
			"step_id": step.ID,
			"title":   step.Title,
			"index":   index,
			"attempt": attempt,
			"policy":  step.RetryPolicy,
		}, err)
	}
	return "", lastErr
}

func validateStepDependencies(plan *mcRuntime.TaskPlan, step mcRuntime.TaskStep) error {
	if plan == nil || len(step.DependsOn) == 0 {
		return nil
	}
	statusByID := make(map[string]mcRuntime.StepStatus, len(plan.Steps))
	for _, item := range plan.Steps {
		statusByID[item.ID] = item.Status
	}
	for _, depID := range step.DependsOn {
		status, ok := statusByID[depID]
		if !ok {
			return mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "step %s depends on unknown step %s", step.ID, depID)
		}
		if status != mcRuntime.StepCompleted {
			return mcRuntime.Errorf(mcRuntime.CodeInvalidInput, "step %s dependency %s is %s", step.ID, depID, status)
		}
	}
	return nil
}

func stepRetryEnabled(step mcRuntime.TaskStep) bool {
	policy := strings.ToLower(strings.TrimSpace(step.RetryPolicy))
	return strings.Contains(policy, "retry") && policy != "none" && policy != "no-retry"
}

func stepFailureStrategyAllowsSkip(step mcRuntime.TaskStep) bool {
	strategy := strings.ToLower(strings.TrimSpace(step.FailureStrategy))
	switch strategy {
	case "skip", "skip-step", "continue", "continue-plan", "best-effort":
		return true
	default:
		return false
	}
}

type plannerResponse struct {
	Steps []plannerStep `json:"steps"`
}

type plannerStep struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Prompt          string   `json:"prompt"`
	DependsOn       []string `json:"depends_on"`
	RetryPolicy     string   `json:"retry_policy"`
	FailureStrategy string   `json:"failure_strategy"`
}

func (a *Agent) buildStructuredPlan(ctx context.Context, execCtx *mcRuntime.ExecutionContext, userInput string) *mcRuntime.TaskPlan {
	plan := mcRuntime.BuildTaskPlan(execCtx, userInput)
	response, err := a.Brain.Think(ctx,
		[]brain.Message{{Role: "user", Content: planningPrompt(userInput)}},
		"You are a precise task planner. Return only JSON.",
		nil,
	)
	if err != nil {
		slog.Warn("Structured planning failed; falling back to heuristic plan", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
		return plan
	}
	a.recordBrainUsage(execCtx, response)

	steps, err := parsePlannerSteps(response.Text)
	if err != nil {
		slog.Warn("Structured planning parse failed; falling back to heuristic plan", append([]any{"error", err}, mcRuntime.LogAttrs(execCtx)...)...)
		return plan
	}
	plan.Steps = steps
	return plan
}

func planningPrompt(goal string) string {
	return fmt.Sprintf(
		"Create a concise execution plan for this goal.\n\n"+
			"Goal:\n%s\n\n"+
			"Return JSON only in this shape:\n"+
			"{\"steps\":[{\"id\":\"step-1\",\"title\":\"...\",\"prompt\":\"...\",\"depends_on\":[],\"retry_policy\":\"retry-on-transient\",\"failure_strategy\":\"fail-plan\"}]}\n\n"+
			"Rules:\n"+
			"- Use 2-6 steps.\n"+
			"- Each prompt must be directly executable by an agent.\n"+
			"- Use stable step IDs like step-1, step-2.\n"+
			"- depends_on must reference earlier step IDs only.",
		strings.TrimSpace(goal),
	)
}

func parsePlannerSteps(text string) ([]mcRuntime.TaskStep, error) {
	raw := strings.TrimSpace(text)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("planner response did not contain JSON object")
	}
	raw = raw[start : end+1]

	var parsed plannerResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Steps) == 0 {
		return nil, fmt.Errorf("planner returned no steps")
	}
	if len(parsed.Steps) > 12 {
		return nil, fmt.Errorf("planner returned too many steps: %d", len(parsed.Steps))
	}

	seen := make(map[string]bool, len(parsed.Steps))
	steps := make([]mcRuntime.TaskStep, 0, len(parsed.Steps))
	for i, item := range parsed.Steps {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("step-%d", i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate step id: %s", id)
		}
		for _, dep := range item.DependsOn {
			if dep = strings.TrimSpace(dep); dep == "" || !seen[dep] {
				return nil, fmt.Errorf("invalid dependency %q for step %s", dep, id)
			}
		}
		title := strings.TrimSpace(item.Title)
		prompt := strings.TrimSpace(item.Prompt)
		if title == "" || prompt == "" {
			return nil, fmt.Errorf("step %s missing title or prompt", id)
		}
		seen[id] = true
		steps = append(steps, mcRuntime.TaskStep{
			ID:              id,
			Title:           title,
			Prompt:          prompt,
			DependsOn:       item.DependsOn,
			RetryPolicy:     strings.TrimSpace(item.RetryPolicy),
			FailureStrategy: strings.TrimSpace(item.FailureStrategy),
			Status:          mcRuntime.StepPending,
		})
	}
	return steps, nil
}

func (a *Agent) verifyPlannedExecution(ctx context.Context, execCtx *mcRuntime.ExecutionContext, goal string, results []string) error {
	return a.verifyPlannedExecutionAttempt(ctx, execCtx, goal, results, false)
}

func (a *Agent) verifyPlannedExecutionAttempt(ctx context.Context, execCtx *mcRuntime.ExecutionContext, goal string, results []string, recovered bool) error {
	gateID := "quality-gate"
	if recovered {
		gateID = "quality-gate-2"
	}
	step := mcRuntime.TaskStep{
		ID:        gateID,
		Title:     "Quality gate",
		Prompt:    "Verify the completed task against the original goal and planned step outputs.",
		Status:    mcRuntime.StepRunning,
		StartedAt: time.Now().UTC(),
	}
	execCtx.Plan.Steps = append(execCtx.Plan.Steps, step)
	execCtx.Plan.Status = mcRuntime.StepRunning
	execCtx.Plan.UpdatedAt = step.StartedAt
	a.checkpointPlan(execCtx)
	a.emitRuntimeEvent(execCtx, "workflow.verification", "verifying", "verifying", map[string]any{
		"step_id": step.ID,
		"title":   step.Title,
	}, nil)

	response, err := a.Brain.Think(ctx,
		[]brain.Message{{Role: "user", Content: a.verificationPrompt(goal, results)}},
		"You are a strict task verifier. Start your response with PASS or FAIL.",
		nil,
	)
	if err != nil {
		return a.failVerification(execCtx, err.Error())
	}
	a.recordBrainUsage(execCtx, response)

	last := &execCtx.Plan.Steps[len(execCtx.Plan.Steps)-1]
	last.Result = truncate(strings.TrimSpace(response.Text), 400)
	last.CompletedAt = time.Now().UTC()
	execCtx.Plan.UpdatedAt = last.CompletedAt
	if verificationFailed(response.Text) {
		last.Status = mcRuntime.StepFailed
		execCtx.Plan.Status = mcRuntime.StepFailed
		execCtx.Plan.Summary = "Plan verification failed"
		a.checkpointPlan(execCtx)
		err := mcRuntime.Errorf(mcRuntime.CodeVerificationFailed, "plan verification failed: %s", truncate(strings.TrimSpace(response.Text), 200))
		a.emitRuntimeEvent(execCtx, "workflow.verification", "step_failed", "failed", map[string]any{"step_id": last.ID}, err)
		if !recovered {
			if recoveredResults, recoverErr := a.recoverVerificationFailure(ctx, execCtx, goal, results, response.Text); recoverErr == nil {
				return a.verifyPlannedExecutionAttempt(ctx, execCtx, goal, recoveredResults, true)
			}
		}
		return err
	}

	last.Status = mcRuntime.StepCompleted
	execCtx.Plan.Status = mcRuntime.StepCompleted
	execCtx.Plan.Summary = fmt.Sprintf("Completed %d planned steps and passed verification", len(execCtx.Plan.Steps)-1)
	a.checkpointPlan(execCtx)
	a.emitRuntimeEvent(execCtx, "workflow.verification", "step_completed", "completed", map[string]any{"step_id": last.ID}, nil)
	return nil
}

func (a *Agent) recoverVerificationFailure(ctx context.Context, execCtx *mcRuntime.ExecutionContext, goal string, results []string, verificationText string) ([]string, error) {
	if execCtx == nil || execCtx.Plan == nil {
		return nil, mcRuntime.Errorf(mcRuntime.CodeInternal, "missing plan for verification recovery")
	}
	now := time.Now().UTC()
	step := mcRuntime.TaskStep{
		ID:              fmt.Sprintf("replan-%d", countReplanSteps(execCtx.Plan)+1),
		Title:           "Correct verification failure",
		Prompt:          correctiveStepPrompt(goal, results, verificationText),
		RetryPolicy:     "retry-on-transient",
		FailureStrategy: "fail-plan",
		Status:          mcRuntime.StepRunning,
		StartedAt:       now,
	}
	execCtx.Plan.Steps = append(execCtx.Plan.Steps, step)
	execCtx.Plan.Status = mcRuntime.StepRunning
	execCtx.Plan.Summary = "Replanning after verification failure"
	execCtx.Plan.UpdatedAt = now
	a.checkpointPlan(execCtx)
	a.emitRuntimeEvent(execCtx, "workflow.replan", "replanning", "planning", map[string]any{
		"step_id": step.ID,
		"reason":  truncate(strings.TrimSpace(verificationText), 200),
	}, nil)

	index := len(execCtx.Plan.Steps) - 1
	current := &execCtx.Plan.Steps[index]
	result, err := a.executePlanStepWithRetry(ctx, execCtx, current.Prompt, *current, index+1)
	if err != nil {
		current.Status = mcRuntime.StepFailed
		current.Error = err.Error()
		current.CompletedAt = time.Now().UTC()
		current.Checkpoint = fmt.Sprintf("failed:%s:%s", current.ID, current.CompletedAt.Format(time.RFC3339Nano))
		execCtx.Plan.Status = mcRuntime.StepFailed
		execCtx.Plan.Summary = "Replan corrective step failed"
		execCtx.Plan.UpdatedAt = current.CompletedAt
		a.checkpointPlan(execCtx)
		a.emitRuntimeEvent(execCtx, "workflow.replan", "step_failed", "failed", map[string]any{"step_id": current.ID}, err)
		return nil, err
	}

	current.Status = mcRuntime.StepCompleted
	current.Result = truncate(result, 400)
	current.CompletedAt = time.Now().UTC()
	current.Checkpoint = fmt.Sprintf("completed:%s:%s", current.ID, current.CompletedAt.Format(time.RFC3339Nano))
	execCtx.Plan.UpdatedAt = current.CompletedAt
	a.checkpointPlan(execCtx)
	a.emitRuntimeEvent(execCtx, "workflow.replan", "step_completed", "completed", map[string]any{"step_id": current.ID}, nil)

	recoveredResults := append([]string{}, results...)
	recoveredResults = append(recoveredResults, fmt.Sprintf("%s: %s", current.Title, truncate(result, 200)))
	return recoveredResults, nil
}

func countReplanSteps(plan *mcRuntime.TaskPlan) int {
	if plan == nil {
		return 0
	}
	count := 0
	for _, step := range plan.Steps {
		if strings.HasPrefix(step.ID, "replan-") {
			count++
		}
	}
	return count
}

func correctiveStepPrompt(goal string, results []string, verificationText string) string {
	return fmt.Sprintf(
		"Correct the failed verification for this task.\n\nOriginal goal:\n%s\n\nCompleted outputs:\n%s\n\nVerification failure:\n%s\n\nReturn the concrete corrected output or action result.",
		strings.TrimSpace(goal),
		strings.TrimSpace(strings.Join(results, "\n\n")),
		strings.TrimSpace(verificationText),
	)
}

func (a *Agent) failVerification(execCtx *mcRuntime.ExecutionContext, message string) error {
	now := time.Now().UTC()
	last := &execCtx.Plan.Steps[len(execCtx.Plan.Steps)-1]
	last.Status = mcRuntime.StepFailed
	last.Error = message
	last.CompletedAt = now
	execCtx.Plan.Status = mcRuntime.StepFailed
	execCtx.Plan.Summary = "Plan verification failed"
	execCtx.Plan.UpdatedAt = now
	a.checkpointPlan(execCtx)
	err := mcRuntime.Errorf(mcRuntime.CodeVerificationFailed, "plan verification failed: %s", message)
	a.emitRuntimeEvent(execCtx, "workflow.verification", "step_failed", "failed", map[string]any{"step_id": last.ID}, err)
	return err
}

func (a *Agent) recordBrainUsage(execCtx *mcRuntime.ExecutionContext, response *brain.BrainResponse) {
	if execCtx == nil || response == nil {
		return
	}
	execCtx.Stats.BrainCalls++
	execCtx.Stats.InputTokens += response.Usage.InputTokens
	execCtx.Stats.OutputTokens += response.Usage.OutputTokens
}

// logBrainResponse logs the LLM response in educational tree format.
func (a *Agent) logBrainResponse(response *brain.BrainResponse) {
	if response.StopReason == "tool_use" {
		mclog.Tree(0, false, fmt.Sprintf("✓ 响应: stop_reason=%s (LLM决定调用工具)", response.StopReason))
		mclog.Tree(0, false, fmt.Sprintf("Token 用量: %d 输入 → %d 输出",
			response.Usage.InputTokens, response.Usage.OutputTokens))
		mclog.Tree(0, true, fmt.Sprintf("工具调用: %d 个", len(response.ToolCalls)))
	} else {
		mclog.Tree(0, false, fmt.Sprintf("✓ 响应完成 stop_reason=%s", response.StopReason))
		mclog.Tree(0, false, fmt.Sprintf("Token 用量: %d 输入 → %d 输出",
			response.Usage.InputTokens, response.Usage.OutputTokens))
		mclog.Tree(0, true, "无工具调用 → 直接返回文本")
	}
}

func (a *Agent) reflect(ctx context.Context) {
	history := a.Context.History()
	if len(history) == 0 {
		return
	}

	start := len(history) - 6
	if start < 0 {
		start = 0
	}
	recent := history[start:]

	var parts []string
	for _, m := range recent {
		if m.Role == "user" || m.Role == "assistant" {
			content := m.Content
			if len(content) > 300 {
				content = content[:300]
			}
			parts = append(parts, fmt.Sprintf("[%s] %s", m.Role, content))
		}
	}

	if len(parts) == 0 {
		return
	}

	recentText := strings.Join(parts, "\n")
	reflectionPrompt := fmt.Sprintf(
		"Inspect the transcript below and decide whether it contains durable context "+
			"that should help future turns. Preserve only stable information such as "+
			"user preferences, decisions, reusable facts, or recurring work patterns.\n\n"+
			"Return up to three short bullets. If there is no durable context, return MEMORY_SKIP.\n\n%s", recentText,
	)

	response, err := a.Brain.Think(ctx,
		[]brain.Message{{Role: "user", Content: reflectionPrompt}},
		"You maintain concise long-term notes for an assistant runtime.",
		nil,
	)
	if err != nil {
		slog.Warn("Reflection failed", "error", err)
		return
	}

	if !strings.Contains(response.Text, "MEMORY_SKIP") {
		a.Memory.AppendMemory(fmt.Sprintf("[Reflection] %s", strings.TrimSpace(response.Text)))
		slog.Info("Reflection recorded", append([]any{"text", response.Text[:min(100, len(response.Text))]}, mcRuntime.LogAttrs(mcRuntime.FromContext(ctx))...)...)
	} else {
		slog.Debug("Reflection: nothing to remember")
	}
}

// Spawn creates a child agent for a subtask.
func (a *Agent) Spawn(ctx context.Context, task, agentID string) (string, error) {
	if a.SpawnDepth >= a.AgentDef.MaxSpawnDepth {
		return fmt.Sprintf("Error: Max spawn depth (%d) reached", a.AgentDef.MaxSpawnDepth), nil
	}
	execCtx := mcRuntime.FromContext(ctx)
	if execCtx != nil && a.AgentDef.MaxSubagents > 0 && execCtx.Stats.SpawnCalls >= a.AgentDef.MaxSubagents {
		return fmt.Sprintf("Error: Max subagents per task (%d) reached", a.AgentDef.MaxSubagents), nil
	}

	allowed := a.AgentDef.SubagentsAllow
	if !containsStr(allowed, "*") && !containsStr(allowed, agentID) {
		return fmt.Sprintf("Error: Not allowed to spawn agent '%s'", agentID), nil
	}

	slog.Info("Spawning child agent", append([]any{"child_agent_id", agentID, "depth", a.SpawnDepth + 1}, mcRuntime.LogAttrs(mcRuntime.FromContext(ctx))...)...)
	if execCtx != nil {
		execCtx.Stats.SpawnCalls++
		a.recordChildTask(execCtx, childTaskID(execCtx, execCtx.Stats.SpawnCalls), agentID, task, mcRuntime.StepRunning, "", "")
	}

	childDef, err := a.Config.GetAgent(agentID)
	if err != nil {
		a.finishLatestChildTask(execCtx, mcRuntime.StepFailed, "", err.Error())
		return fmt.Sprintf("Error: %v", err), nil
	}
	childWorkspace, err := a.Config.AgentWorkspace(agentID)
	if err != nil {
		a.finishLatestChildTask(execCtx, mcRuntime.StepFailed, "", err.Error())
		return fmt.Sprintf("Error: %v", err), nil
	}

	child, err := NewAgent(childDef, childWorkspace, a.Config, a.SpawnDepth+1)
	if err != nil {
		a.finishLatestChildTask(execCtx, mcRuntime.StepFailed, "", err.Error())
		return fmt.Sprintf("Error creating child: %v", err), nil
	}

	result, err := child.ProcessMessage(ctx, task)
	if err != nil {
		a.finishLatestChildTask(execCtx, mcRuntime.StepFailed, "", err.Error())
		return fmt.Sprintf("Error: child agent failed: %v", err), nil
	}

	a.finishLatestChildTask(execCtx, mcRuntime.StepCompleted, result, "")
	return fmt.Sprintf("[Agent %s completed]\n%s", agentID, result), nil
}

func (a *Agent) recordChildTask(execCtx *mcRuntime.ExecutionContext, id, agentID, prompt string, status mcRuntime.StepStatus, result, errText string) {
	if execCtx == nil || execCtx.Plan == nil {
		return
	}
	now := time.Now().UTC()
	execCtx.Plan.ChildTasks = append(execCtx.Plan.ChildTasks, mcRuntime.ChildTask{
		ID:        id,
		AgentID:   agentID,
		Prompt:    strings.TrimSpace(prompt),
		Status:    status,
		Result:    truncate(result, 400),
		Error:     errText,
		StartedAt: now,
	})
	execCtx.Plan.UpdatedAt = now
	a.checkpointPlan(execCtx)
}

func (a *Agent) finishLatestChildTask(execCtx *mcRuntime.ExecutionContext, status mcRuntime.StepStatus, result, errText string) {
	if execCtx == nil || execCtx.Plan == nil || len(execCtx.Plan.ChildTasks) == 0 {
		return
	}
	now := time.Now().UTC()
	child := &execCtx.Plan.ChildTasks[len(execCtx.Plan.ChildTasks)-1]
	child.Status = status
	child.Result = truncate(result, 400)
	child.Error = errText
	child.CompletedAt = now
	execCtx.Plan.UpdatedAt = now
	a.checkpointPlan(execCtx)
}

func childTaskID(execCtx *mcRuntime.ExecutionContext, index int) string {
	if execCtx == nil || execCtx.IDs.TaskID == "" {
		return fmt.Sprintf("child-%d", index)
	}
	return fmt.Sprintf("%s-child-%d", execCtx.IDs.TaskID, index)
}

func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func (a *Agent) planStepPrompt(goal string, step mcRuntime.TaskStep, index, total int) string {
	return fmt.Sprintf(
		"Original goal:\n%s\n\nCurrent plan step (%d/%d): %s\n%s",
		strings.TrimSpace(goal),
		index,
		total,
		step.Title,
		strings.TrimSpace(step.Prompt),
	)
}

func (a *Agent) verificationPrompt(goal string, results []string) string {
	return fmt.Sprintf(
		"Verification task:\n"+
			"Original goal:\n%s\n\n"+
			"Completed step outputs:\n%s\n\n"+
			"Return PASS if the outputs satisfy the goal. Return FAIL: <reason> if there is a missing, unsafe, or incorrect result.",
		strings.TrimSpace(goal),
		strings.TrimSpace(strings.Join(results, "\n\n")),
	)
}

func verificationFailed(text string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(text))
	return strings.HasPrefix(normalized, "FAIL") || strings.Contains(normalized, "VERIFICATION_FAILED")
}

func (a *Agent) checkpointPlan(execCtx *mcRuntime.ExecutionContext) {
	if execCtx == nil || execCtx.OnPlanUpdate == nil {
		return
	}
	execCtx.OnPlanUpdate()
}

func (a *Agent) emitRuntimeEvent(execCtx *mcRuntime.ExecutionContext, span, event, status string, metadata map[string]any, err error) {
	if execCtx == nil || execCtx.OnRuntimeEvent == nil {
		return
	}
	execCtx.OnRuntimeEvent(span, event, status, metadata, err)
}
