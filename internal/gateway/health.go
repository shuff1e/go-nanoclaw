package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"sync/atomic"
	"time"
)

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

	// Check LLM API reachability for each agent's provider
	llmChecks := gw.checkLLMReachability()
	if len(llmChecks) > 0 {
		checks["llm_api"] = llmChecks
		for _, ok := range llmChecks {
			if !ok {
				status = "degraded"
			}
		}
	}

	return HealthStatus{
		Status:    status,
		Checks:    checks,
		Timestamp: time.Now().Format(time.RFC3339),
	}
}

func (gw *Gateway) checkLLMReachability() map[string]bool {
	results := make(map[string]bool)
	checked := make(map[string]bool)

	for _, def := range gw.Config.Agents {
		provider := def.Brain.Provider
		if checked[provider] {
			continue
		}
		checked[provider] = true

		baseURL := def.Brain.ResolveBaseURL()
		var healthURL string
		switch provider {
		case "anthropic":
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}
			healthURL = baseURL + "/v1/messages"
		case "openai":
			if baseURL == "" {
				baseURL = "https://api.openai.com/v1"
			}
			healthURL = baseURL + "/models"
		default:
			continue
		}

		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequest("GET", healthURL, nil)
		if err != nil {
			results[provider] = false
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			results[provider] = false
			continue
		}
		resp.Body.Close()

		// Accept any non-5xx response as "reachable"
		results[provider] = resp.StatusCode < 500
	}

	return results
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
