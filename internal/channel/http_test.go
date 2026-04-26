package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/cron"
	"go-nanoclaw/internal/gateway"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

type fakeBrain struct {
	response *brain.BrainResponse
}

func (f *fakeBrain) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	return f.response, nil
}

func TestHTTPInputReturnsRequestID(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	reqBody := []byte(`{"text":"hello","agent_id":"main","session_id":"s-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["request_id"] == "" {
		t.Fatalf("expected request_id in response, got %v", resp)
	}
	if resp["response"] != "ok" {
		t.Fatalf("expected response 'ok', got %q", resp["response"])
	}
}

func TestHTTPInputUsesProvidedSessionID(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	var gotSessionID string

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	original := agentInstance.Brain
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		if execCtx := mcRuntime.FromContext(ctx); execCtx != nil {
			gotSessionID = execCtx.IDs.SessionID
		}
		return original.Think(ctx, messages, systemPrompt, tools)
	})

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	reqBody := []byte(`{"text":"hello","agent_id":"main","session_id":"custom-session"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotSessionID != "custom-session" {
		t.Fatalf("expected custom session id, got %q", gotSessionID)
	}
}

func TestHTTPInputAcceptsPlanExecuteMode(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	reqBody := []byte(`{"text":"Analyze this. Then implement it.","agent_id":"main","session_id":"s-plan","mode":"plan_execute"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	tasks, err := gw.TaskViews("main", 10, gateway.ExecutionLogFilter{RequestID: requestIDFromResponse(t, rec.Body.Bytes())})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Mode != "plan_execute" || tasks[0].Plan == nil {
		t.Fatalf("expected planned task view, got %+v", tasks)
	}
}

func TestHTTPAsyncTasksEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	reqBody := []byte(`{"text":"hello async","agent_id":"main","session_id":"s-async","max_retries":2}`)
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"attempt":1`) {
		t.Fatalf("expected attempt=1 in async response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"max_retries":2`) {
		t.Fatalf("expected max_retries=2 in async response, got %s", rec.Body.String())
	}
	requestID := requestIDFromResponse(t, rec.Body.Bytes())
	if requestID == "" {
		t.Fatalf("expected request id in async response, got %s", rec.Body.String())
	}

	var tasks []gateway.TaskView
	for i := 0; i < 20; i++ {
		tasks, err = gw.TaskViews("main", 10, gateway.ExecutionLogFilter{RequestID: requestID})
		if err != nil {
			t.Fatalf("task views: %v", err)
		}
		if len(tasks) == 1 && (tasks[0].Status == "completed" || tasks[0].Status == "failed" || tasks[0].Status == "cancelled") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("expected completed async task, got %+v", tasks)
	}
}

func TestHTTPAsyncTasksAutoRetryPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	stepCallCount := 0
	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		if len(messages) == 1 && strings.Contains(messages[0].Content, "Create a concise execution plan") {
			return &brain.BrainResponse{Text: "ok", StopReason: "end_turn"}, nil
		}
		stepCallCount++
		if stepCallCount == 2 {
			return nil, mcRuntime.Errorf(mcRuntime.CodeBrainFailed, "forced failure")
		}
		return &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		}, nil
	})

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	reqBody := []byte(`{"text":"Analyze this. Then implement it.","agent_id":"main","mode":"plan_execute","max_retries":1}`)
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	originalRequestID := requestIDFromResponse(t, rec.Body.Bytes())

	var tasks []gateway.TaskView
	for i := 0; i < 80; i++ {
		tasks, err = gw.TaskViews("main", 10, gateway.ExecutionLogFilter{})
		if err != nil {
			t.Fatalf("task views: %v", err)
		}
		var foundCompletedRetry bool
		for _, task := range tasks {
			if task.RetryOfRequestID == originalRequestID && task.Status == "completed" && task.Attempt == 2 {
				foundCompletedRetry = true
				if task.MaxRetries != 1 || task.Plan == nil || task.Plan.MaxRetries != 1 {
					t.Fatalf("expected max_retries on retried task, got %+v", task)
				}
				break
			}
		}
		if foundCompletedRetry {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected automatic retry to produce completed attempt 2, got %+v", tasks)
}

func TestHTTPRetryTaskEndpointResumesFailedPlan(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	stepCallCount := 0
	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		if len(messages) == 1 && strings.Contains(messages[0].Content, "Create a concise execution plan") {
			return &brain.BrainResponse{Text: "ok", StopReason: "end_turn"}, nil
		}
		stepCallCount++
		if stepCallCount == 2 {
			return nil, mcRuntime.Errorf(mcRuntime.CodeBrainFailed, "forced failure")
		}
		return &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		}, nil
	})

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	firstReq := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"Analyze this. Then implement it.","mode":"plan_execute"}`)))
	firstReq.Header.Set("X-API-Key", "secret")
	firstRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(firstRec, firstReq)

	if firstRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected first run to fail, got %d: %s", firstRec.Code, firstRec.Body.String())
	}

	plans, err := gw.PlanRecords("main", 10)
	if err != nil || len(plans) == 0 {
		t.Fatalf("expected persisted failed plan, got %v %+v", err, plans)
	}
	failedPlan := plans[len(plans)-1]
	if len(failedPlan.Steps) < 2 || failedPlan.Steps[0].Status != "completed" || failedPlan.Steps[1].Status != "failed" {
		t.Fatalf("expected failed checkpointed plan, got %+v", failedPlan)
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/tasks/retry", bytes.NewReader([]byte(`{"agent_id":"main","request_id":"`+failedPlan.RequestID+`"}`)))
	retryReq.Header.Set("X-API-Key", "secret")
	retryRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(retryRec, retryReq)

	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("expected retry 202, got %d: %s", retryRec.Code, retryRec.Body.String())
	}
	if !strings.Contains(retryRec.Body.String(), `"attempt":2`) {
		t.Fatalf("expected retry attempt=2, got %s", retryRec.Body.String())
	}
	retryRequestID := requestIDFromResponse(t, retryRec.Body.Bytes())
	if retryRequestID == "" {
		t.Fatalf("expected retry request id, got %s", retryRec.Body.String())
	}

	var tasks []gateway.TaskView
	for i := 0; i < 30; i++ {
		tasks, err = gw.TaskViews("main", 10, gateway.ExecutionLogFilter{RequestID: retryRequestID})
		if err != nil {
			t.Fatalf("task views: %v", err)
		}
		if len(tasks) == 1 && tasks[0].Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" || tasks[0].Plan == nil {
		t.Fatalf("expected completed retried task, got %+v", tasks)
	}
	if tasks[0].Attempt != 2 || tasks[0].RetryOfRequestID != failedPlan.RequestID || tasks[0].RetryOfTaskID != failedPlan.TaskID {
		t.Fatalf("expected retry lineage on task view, got %+v", tasks[0])
	}
	if len(tasks[0].Plan.Steps) < 2 || tasks[0].Plan.Steps[0].Status != "completed" || tasks[0].Plan.Steps[1].Status != "completed" {
		t.Fatalf("expected resumed completed plan, got %+v", tasks[0].Plan)
	}
	if tasks[0].Plan.Attempt != 2 || tasks[0].Plan.RetryOfRequestID != failedPlan.RequestID || tasks[0].Plan.RetryOfTaskID != failedPlan.TaskID {
		t.Fatalf("expected retry lineage on plan, got %+v", tasks[0].Plan)
	}
}

func TestHTTPTasksSummaryIncludesRetryStats(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	now := time.Now().UTC().Truncate(time.Second)
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r1",
		TaskID:    "t1",
		Status:    "completed",
		Source:    "http",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r2",
		TaskID:    "t2",
		Status:    "completed",
		Source:    "http",
		StartedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("save retry execution log: %v", err)
	}
	if err := gw.Store().SavePlan("main", store.PlanRecord{
		AgentID:          "main",
		RequestID:        "r2",
		TaskID:           "t2",
		RetryOfRequestID: "r1",
		RetryOfTaskID:    "t1",
		Attempt:          2,
		MaxRetries:       1,
		Mode:             "plan_execute",
		Goal:             "retry",
		Status:           "completed",
		GeneratedAt:      now,
		UpdatedAt:        now.Add(time.Second),
	}); err != nil {
		t.Fatalf("save retry plan: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/tasks?agent_id=main", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tasks 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"retried_total":1`) || !strings.Contains(body, `"auto_retryable":1`) || !strings.Contains(body, `"by_attempt":{"1":1,"2":1}`) {
		t.Fatalf("expected retry summary fields, got %s", body)
	}
}

func TestHTTPInputRejectsInvalidMode(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello","mode":"weird"}`)))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"invalid_input"`) {
		t.Fatalf("expected structured invalid_input error, got %s", rec.Body.String())
	}
}

func TestHTTPInputRejectsBlankText(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"   "}`)))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"invalid_input"`) || !strings.Contains(rec.Body.String(), `"message":"missing 'text'"`) {
		t.Fatalf("expected structured error body, got %s", rec.Body.String())
	}
}

func TestHTTPCancelTaskEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	requestIDCh := make(chan string, 1)
	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = brainFunc(func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
		if execCtx := mcRuntime.FromContext(ctx); execCtx != nil {
			select {
			case requestIDCh <- execCtx.IDs.RequestID:
			default:
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	inputDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello"}`)))
		req.Header.Set("X-API-Key", "secret")
		rec := httptest.NewRecorder()
		ch.Handler().ServeHTTP(rec, req)
		inputDone <- rec
	}()

	requestID := <-requestIDCh
	cancelReq := httptest.NewRequest(http.MethodPost, "/tasks/cancel", bytes.NewReader([]byte(`{"request_id":"`+requestID+`"}`)))
	cancelReq.Header.Set("X-API-Key", "secret")
	cancelRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusOK {
		t.Fatalf("expected cancel 200, got %d: %s", cancelRec.Code, cancelRec.Body.String())
	}
	if !strings.Contains(cancelRec.Body.String(), `"status":"cancelling"`) {
		t.Fatalf("expected cancelling body, got %s", cancelRec.Body.String())
	}

	inputRec := <-inputDone
	if inputRec.Code != http.StatusConflict {
		t.Fatalf("expected cancelled input 409, got %d: %s", inputRec.Code, inputRec.Body.String())
	}
	if !strings.Contains(inputRec.Body.String(), `"code":"cancelled"`) {
		t.Fatalf("expected cancelled error payload, got %s", inputRec.Body.String())
	}
}

func TestHTTPInputRequiresAPIKeyWhenConfigured(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello"}`)))
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTPInputAcceptsBearerAPIKey(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello"}`)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPRateLimitProtectedEndpoints(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	cfg.RateLimitPerMin = 1
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	firstReq := httptest.NewRequest(http.MethodGet, "/sessions?agent_id=main", nil)
	firstReq.Header.Set("X-API-Key", "secret")
	firstRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first request 200, got %d: %s", firstRec.Code, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/sessions?agent_id=main", nil)
	secondReq.Header.Set("X-API-Key", "secret")
	secondRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d: %s", secondRec.Code, secondRec.Body.String())
	}
	if secondRec.Header().Get("Retry-After") == "" || secondRec.Header().Get("X-RateLimit-Limit") != "1" {
		t.Fatalf("expected rate limit headers, got %#v", secondRec.Header())
	}
}

func TestHTTPRateLimitSkipsHealthAndMetrics(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	cfg.RateLimitPerMin = 1
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	for _, path := range []string{"/health", "/health/live", "/metrics"} {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			ch.Handler().ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("expected %s to skip rate limit, got %d", path, rec.Code)
			}
		}
	}
}

func TestHTTPLivenessAndReadiness(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	liveReq := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	liveRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(liveRec, liveReq)
	if liveRec.Code != http.StatusOK {
		t.Fatalf("expected liveness 200, got %d", liveRec.Code)
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	readyRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readiness 503 before start, got %d", readyRec.Code)
	}

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	readyRec = httptest.NewRecorder()
	ch.Handler().ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("expected readiness 200 after start, got %d", readyRec.Code)
	}
}

func TestHTTPToolAuditEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	inputReq := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello"}`)))
	inputReq.Header.Set("X-API-Key", "secret")
	inputRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(inputRec, inputReq)
	if inputRec.Code != http.StatusOK {
		t.Fatalf("expected input 200, got %d: %s", inputRec.Code, inputRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/tool-audit?agent_id=main", nil)
	auditReq.Header.Set("X-API-Key", "secret")
	auditRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("expected tool audit 200, got %d: %s", auditRec.Code, auditRec.Body.String())
	}
	if !bytes.Contains(auditRec.Body.Bytes(), []byte(`"items"`)) {
		t.Fatalf("expected items in tool audit response, got %s", auditRec.Body.String())
	}
}

func TestHTTPToolAuditJSONLExport(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)
	if err := gw.Store().SaveToolAuditLog(store.ToolAuditLog{
		AgentID:    "main",
		ToolName:   "run_command",
		Status:     "denied",
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save tool audit: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/tool-audit?agent_id=main&format=jsonl", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tool audit jsonl 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("expected ndjson content type, got %q", got)
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.Contains(body, `"tool_name":"run_command"`) || strings.Contains(body, `"items"`) {
		t.Fatalf("expected raw jsonl audit line, got %s", body)
	}
}

func TestHTTPApprovalsEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Store().SaveApprovalRecord("main", store.ApprovalRecord{
		ApprovalID:        "approval-1",
		AgentID:           "main",
		RequestID:         "req-1",
		SessionID:         "session-1",
		TaskID:            "task-1",
		ToolName:          "write_workspace_file",
		Arguments:         map[string]any{"path": "approved.txt", "password": "raw-secret"},
		ArgumentsRedacted: map[string]any{"path": "approved.txt", "password": "[REDACTED]"},
		Status:            "pending",
		RequestedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save approval record: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/approvals?agent_id=main&status=pending", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected approvals 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"approval_id":"approval-1"`) || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("expected pending approval response, got %s", body)
	}
	if strings.Contains(body, "raw-secret") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("expected public approval response to use redacted arguments, got %s", body)
	}
}

func TestHTTPApprovalsJSONLExportRedactsArguments(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Store().SaveApprovalRecord("main", store.ApprovalRecord{
		ApprovalID:        "approval-1",
		AgentID:           "main",
		RequestID:         "req-1",
		ToolName:          "write_workspace_file",
		Arguments:         map[string]any{"password": "raw-secret"},
		ArgumentsRedacted: map[string]any{"password": "[REDACTED]"},
		Status:            "pending",
		RequestedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save approval record: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/approvals?agent_id=main&format=jsonl", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected approvals jsonl 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw-secret") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("expected approval jsonl export to redact arguments, got %s", body)
	}
}

func TestHTTPApprovalDecisionEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Store().SaveApprovalRecord("main", store.ApprovalRecord{
		ApprovalID:  "approval-1",
		AgentID:     "main",
		RequestID:   "req-1",
		SessionID:   "session-1",
		TaskID:      "task-1",
		ToolName:    "write_workspace_file",
		Arguments:   map[string]any{"path": "approved.txt", "content": "approved content"},
		Status:      "pending",
		RequestedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save approval record: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodPost, "/approvals/decide", bytes.NewReader([]byte(`{
		"agent_id":"main",
		"approval_id":"approval-1",
		"decision":"approved",
		"decided_by":"tester"
	}`)))
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected approval decision 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"approved"`) || !strings.Contains(body, `"decided_by":"tester"`) {
		t.Fatalf("expected approved decision response, got %s", body)
	}

	records, err := gw.ApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("approval records: %v", err)
	}
	if len(records) != 2 || records[1].Status != "approved" {
		t.Fatalf("expected appended approval decision, got %+v", records)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Workspace, "approved.txt"))
	if err != nil {
		t.Fatalf("expected approved tool to write file: %v", err)
	}
	if string(data) != "approved content" {
		t.Fatalf("unexpected approved file content: %q", string(data))
	}
}

func TestHTTPApprovalRejectMarksTaskFailed(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Store().SaveApprovalRecord("main", store.ApprovalRecord{
		ApprovalID:  "approval-1",
		AgentID:     "main",
		RequestID:   "req-1",
		SessionID:   "session-1",
		TaskID:      "task-1",
		ToolName:    "run_command",
		Status:      "pending",
		RequestedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save approval record: %v", err)
	}
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "req-1",
		SessionID: "session-1",
		TaskID:    "task-1",
		Source:    "http",
		Status:    "awaiting_approval",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save awaiting approval execution: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodPost, "/approvals/decide", bytes.NewReader([]byte(`{
		"agent_id":"main",
		"approval_id":"approval-1",
		"decision":"rejected",
		"decided_by":"tester"
	}`)))
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected approval rejection 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"rejected"`) {
		t.Fatalf("expected rejected response, got %s", rec.Body.String())
	}

	tasks, err := gw.TaskViews("main", 10, gateway.ExecutionLogFilter{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("task views: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("expected rejected approval to fail task, got %+v", tasks)
	}
}

func TestHTTPAsyncTaskAwaitsApprovalForProtectedTool(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.Agents["main"].ToolPolicies.ApprovalRequired = []string{"run_command"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			ToolCalls: []brain.ToolCall{{
				ID:        "tool-1",
				Name:      "run_command",
				Arguments: map[string]any{"command": "echo hi"},
			}},
			StopReason: "tool_use",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader([]byte(`{"text":"run shell","agent_id":"main"}`)))
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	requestID := requestIDFromResponse(t, rec.Body.Bytes())

	var tasks []gateway.TaskView
	for i := 0; i < 30; i++ {
		tasks, err = gw.TaskViews("main", 10, gateway.ExecutionLogFilter{RequestID: requestID})
		if err != nil {
			t.Fatalf("task views: %v", err)
		}
		if len(tasks) == 1 && tasks[0].Status == "awaiting_approval" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(tasks) != 1 || tasks[0].Status != "awaiting_approval" {
		t.Fatalf("expected awaiting approval task, got %+v", tasks)
	}

	approvals, err := gw.ApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("approval records: %v", err)
	}
	if len(approvals) != 1 || approvals[0].Status != "pending" || approvals[0].ToolName != "run_command" {
		t.Fatalf("expected pending shell approval, got %+v", approvals)
	}
}

func TestHTTPTracesEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Store().SaveTraceEvent("main", store.TraceEvent{
		TraceID:   "trace-1",
		RequestID: "req-1",
		AgentID:   "main",
		Span:      "gateway",
		Event:     "completed",
		Status:    "completed",
		At:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save trace event: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/traces?agent_id=main&trace_id=trace-1", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected traces 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"trace_id":"trace-1"`) || !strings.Contains(body, `"event":"completed"`) {
		t.Fatalf("expected trace event response, got %s", body)
	}
}

func TestHTTPMetricsEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	gw := gateway.NewGateway(cfg)
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:      "main",
		RequestID:    "r-metrics",
		Status:       "completed",
		Provider:     "anthropic",
		Model:        "claude-test",
		InputTokens:  10,
		OutputTokens: 5,
		BrainCalls:   1,
		ToolCalls:    2,
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected metrics 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nanoclaw_gateway_up") {
		t.Fatalf("expected metrics body, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `nanoclaw_input_tokens_total{agent_id="main",provider="anthropic",model="claude-test"} 10`) {
		t.Fatalf("expected token metric, got %s", rec.Body.String())
	}
}

func TestHTTPExecutionsEndpointFiltersBySessionID(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	for _, sessionID := range []string{"s-1", "s-2"} {
		body := []byte(`{"text":"hello","agent_id":"main","session_id":"` + sessionID + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader(body))
		req.Header.Set("X-API-Key", "secret")
		rec := httptest.NewRecorder()
		ch.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected input 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/executions?agent_id=main&session_id=s-1", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected executions 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"session_id":"s-1"`) || strings.Contains(rec.Body.String(), `"session_id":"s-2"`) {
		t.Fatalf("expected only filtered session in response, got %s", rec.Body.String())
	}
}

func TestHTTPExecutionsEndpointIncludesSummaryAndTimeFilter(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	now := time.Now().UTC().Truncate(time.Second)
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "older",
		SessionID: "s-1",
		Status:    "failed",
		Source:    "http",
		StartedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("save old execution: %v", err)
	}
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "newer",
		SessionID: "s-1",
		Status:    "completed",
		Source:    "cli",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("save new execution: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(
		http.MethodGet,
		"/executions?agent_id=main&request_id=newer&since="+now.Add(-30*time.Minute).Format(time.RFC3339),
		nil,
	)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected executions 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"request_id":"newer"`) || strings.Contains(body, `"request_id":"older"`) {
		t.Fatalf("expected only newer execution in body, got %s", body)
	}
	if !strings.Contains(body, `"summary"`) || !strings.Contains(body, `"completed":1`) {
		t.Fatalf("expected summary in body, got %s", body)
	}
}

func TestHTTPTasksEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	now := time.Now().UTC().Truncate(time.Second)
	for _, log := range []store.ExecutionLog{
		{AgentID: "main", RequestID: "r1", TaskID: "t1", SessionID: "s1", Source: "http", Status: "started", StartedAt: now},
		{AgentID: "main", RequestID: "r1", TaskID: "t1", SessionID: "s1", Source: "http", Status: "completed", StartedAt: now.Add(1 * time.Second)},
		{AgentID: "main", RequestID: "r2", TaskID: "t2", SessionID: "s2", Source: "cli", Status: "started", StartedAt: now.Add(2 * time.Second)},
		{AgentID: "main", RequestID: "r2", TaskID: "t2", SessionID: "s2", Source: "cli", Status: "failed", Error: "boom", StartedAt: now.Add(3 * time.Second)},
	} {
		if err := gw.Store().SaveExecutionLog(log); err != nil {
			t.Fatalf("save execution log: %v", err)
		}
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/tasks?agent_id=main&status=failed", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tasks 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"request_id":"r2"`) || strings.Contains(body, `"request_id":"r1"`) {
		t.Fatalf("expected only failed task view in body, got %s", body)
	}
	if !strings.Contains(body, `"status":"failed"`) || !strings.Contains(body, `"event_count":2`) {
		t.Fatalf("expected aggregated task fields, got %s", body)
	}
	if !strings.Contains(body, `"summary"`) || !strings.Contains(body, `"failed":1`) {
		t.Fatalf("expected task summary in body, got %s", body)
	}
}

func TestHTTPTasksStreamEndpointEmitsTerminalTask(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	now := time.Now().UTC()
	if err := gw.Store().SaveExecutionLog(store.ExecutionLog{
		AgentID:   "main",
		RequestID: "r-stream",
		TaskID:    "t-stream",
		SessionID: "s1",
		Source:    "http",
		Status:    "completed",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("save execution log: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/tasks/stream?agent_id=main&request_id=r-stream", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected stream 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected SSE content type, got %q", rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: task") || !strings.Contains(body, `"request_id":"r-stream"`) || !strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("expected completed task SSE, got %s", body)
	}
}

func TestHTTPTasksStreamEndpointRequiresTaskIdentifier(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)
	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)

	req := httptest.NewRequest(http.MethodGet, "/tasks/stream?agent_id=main", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected stream 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPSessionsEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	inputReq := httptest.NewRequest(http.MethodPost, "/input", bytes.NewReader([]byte(`{"text":"hello"}`)))
	inputReq.Header.Set("X-API-Key", "secret")
	inputRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(inputRec, inputReq)
	if inputRec.Code != http.StatusOK {
		t.Fatalf("expected input 200, got %d: %s", inputRec.Code, inputRec.Body.String())
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/sessions?agent_id=main", nil)
	sessionReq.Header.Set("X-API-Key", "secret")
	sessionRec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("expected sessions 200, got %d: %s", sessionRec.Code, sessionRec.Body.String())
	}
	if !strings.Contains(sessionRec.Body.String(), `"assistant":"ok"`) {
		t.Fatalf("expected session response body to include saved assistant output, got %s", sessionRec.Body.String())
	}
}

func TestHTTPMemoryEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Memory.SetStore("main", gw.Store())
	if err := agentInstance.Memory.AppendCategorizedMemory("facts", "likes black coffee", "test"); err != nil {
		t.Fatalf("append memory: %v", err)
	}

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/memory?agent_id=main", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected memory 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"likes black coffee"`) {
		t.Fatalf("expected memory content in body, got %s", rec.Body.String())
	}
}

func TestHTTPCronEndpoint(t *testing.T) {
	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.APIKeys = []string{"secret"}
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer gw.Stop()

	now := time.Now().UTC().Truncate(time.Second)
	gw.AddCronJob(context.Background(), "main", cron.Job{
		Name:     "daily-check",
		Schedule: "09:30",
		Prompt:   "check inbox",
		Enabled:  true,
		LastRun:  &now,
	})

	ch := NewHTTPChannel(gw, "main", "127.0.0.1", 0)
	req := httptest.NewRequest(http.MethodGet, "/cron?agent_id=main", nil)
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()
	ch.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected cron 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"daily-check"`) {
		t.Fatalf("expected cron job in body, got %s", rec.Body.String())
	}
}

func TestHTTPQueryLimit(t *testing.T) {
	tests := []struct {
		name     string
		rawQuery string
		want     int
	}{
		{name: "missing", rawQuery: "", want: 50},
		{name: "valid", rawQuery: "limit=10", want: 10},
		{name: "invalid", rawQuery: "limit=nope", want: 50},
		{name: "negative", rawQuery: "limit=-1", want: 50},
		{name: "capped", rawQuery: "limit=999", want: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/sessions?"+tt.rawQuery, nil)
			if got := queryLimit(req, 50); got != tt.want {
				t.Fatalf("expected limit %d, got %d", tt.want, got)
			}
		})
	}
}

func TestHTTPQueryTime(t *testing.T) {
	ts := "2026-04-24T22:00:00Z"
	req := httptest.NewRequest(http.MethodGet, "/executions?since="+ts, nil)
	got := queryTime(req, "since")
	if got.Format(time.RFC3339) != ts {
		t.Fatalf("expected parsed time %s, got %s", ts, got.Format(time.RFC3339))
	}

	badReq := httptest.NewRequest(http.MethodGet, "/executions?since=nope", nil)
	if bad := queryTime(badReq, "since"); !bad.IsZero() {
		t.Fatalf("expected zero time for invalid input, got %s", bad)
	}
}

type brainFunc func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error)

func (f brainFunc) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	return f(ctx, messages, systemPrompt, tools)
}

func requestIDFromResponse(t *testing.T, body []byte) string {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	requestID, _ := resp["request_id"].(string)
	return requestID
}
