package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-nanoclaw/internal/gateway"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

// HTTPChannel provides HTTP API endpoints for NanoClaw.
type HTTPChannel struct {
	Gateway      *gateway.Gateway
	DefaultAgent string
	Host         string
	Port         int
	TLSCertFile  string
	TLSKeyFile   string
	server       *http.Server
	rateMu       sync.Mutex
	rateBuckets  map[string]rateBucket
}

type rateBucket struct {
	WindowStart time.Time
	Count       int
}

type inputRequest struct {
	Text       string `json:"text"`
	AgentID    string `json:"agent_id"`
	SessionID  string `json:"session_id"`
	Mode       string `json:"mode"`
	MaxRetries int    `json:"max_retries"`
}

type cancelTaskRequest struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
}

type retryTaskRequest struct {
	AgentID    string `json:"agent_id"`
	RequestID  string `json:"request_id"`
	TaskID     string `json:"task_id"`
	MaxRetries *int   `json:"max_retries"`
}

type decideApprovalRequest struct {
	AgentID    string `json:"agent_id"`
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	DecidedBy  string `json:"decided_by"`
}

// NewHTTPChannel creates a new HTTPChannel.
func NewHTTPChannel(gw *gateway.Gateway, defaultAgent, host string, port int) *HTTPChannel {
	return &HTTPChannel{
		Gateway:      gw,
		DefaultAgent: defaultAgent,
		Host:         host,
		Port:         port,
	}
}

// WithTLS sets TLS certificate and key files for HTTPS.
func (h *HTTPChannel) WithTLS(certFile, keyFile string) *HTTPChannel {
	h.TLSCertFile = certFile
	h.TLSKeyFile = keyFile
	return h
}

// Start begins serving HTTP endpoints.
func (h *HTTPChannel) Start(ctx context.Context) error {
	h.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", h.Host, h.Port),
		Handler: h.Handler(),
	}

	addr := h.server.Addr
	scheme := "http"
	if h.TLSCertFile != "" && h.TLSKeyFile != "" {
		scheme = "https"
	}
	slog.Info("HTTP server starting", "addr", addr, "tls", scheme == "https")
	fmt.Printf("Health endpoint: %s://%s/health\n", scheme, addr)
	fmt.Printf("Input endpoint:  POST %s://%s/input\n", scheme, addr)

	errCh := make(chan error, 1)
	go func() {
		var err error
		if h.TLSCertFile != "" && h.TLSKeyFile != "" {
			err = h.server.ListenAndServeTLS(h.TLSCertFile, h.TLSKeyFile)
		} else {
			err = h.server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		return h.server.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

func (h *HTTPChannel) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.Gateway.Health())
	})

	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, http.StatusOK, h.Gateway.Liveness())
	})

	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		if h.Gateway.Readiness().Status != "ready" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, h.Gateway.Readiness())
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(h.Gateway.Metrics()))
	})

	mux.HandleFunc("GET /executions", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		filter := gateway.ExecutionLogFilter{
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			Source:    strings.TrimSpace(r.URL.Query().Get("source")),
			Since:     queryTime(r, "since"),
			Until:     queryTime(r, "until"),
		}
		logs, err := h.Gateway.FilteredExecutionLogs(agentID, limit, filter)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"limit":    limit,
			"filter":   filter,
			"summary":  h.Gateway.SummarizeExecutionLogs(logs),
			"items":    logs,
		})
	})

	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		filter := gateway.ExecutionLogFilter{
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			Source:    strings.TrimSpace(r.URL.Query().Get("source")),
			Since:     queryTime(r, "since"),
			Until:     queryTime(r, "until"),
		}
		tasks, err := h.Gateway.TaskViews(agentID, limit, filter)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"limit":    limit,
			"filter":   filter,
			"summary":  h.Gateway.SummarizeTaskViews(tasks),
			"items":    tasks,
		})
	})

	mux.HandleFunc("GET /tasks/stream", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
		taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
		if requestID == "" && taskID == "" {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "missing 'request_id' or 'task_id'")
			return
		}
		h.streamTask(w, r, agentID, requestID, taskID)
	})

	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}

		var body inputRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "", "invalid_json", "invalid JSON")
			return
		}
		if strings.TrimSpace(body.Text) == "" {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "missing 'text'")
			return
		}
		agentID := body.AgentID
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		mode, ok := mcRuntime.ParseExecutionMode(body.Mode)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "invalid 'mode'")
			return
		}
		sessionID := body.SessionID
		if sessionID == "" {
			sessionID = agentID
		}

		result, err := h.Gateway.HandleInputModeAsyncDetailed(r.Context(), body.Text, agentID, sessionID, "http", mode, max(body.MaxRetries, 0))
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})

	mux.HandleFunc("GET /tool-audit", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		filter := gateway.ToolAuditLogFilter{
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			TraceID:   strings.TrimSpace(r.URL.Query().Get("trace_id")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			ToolName:  strings.TrimSpace(r.URL.Query().Get("tool_name")),
			Since:     queryTime(r, "since"),
			Until:     queryTime(r, "until"),
		}
		logs, err := h.Gateway.FilteredToolAuditLogs(agentID, limit, filter)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		if wantsJSONL(r) {
			writeJSONL(w, logs)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"limit":    limit,
			"filter":   filter,
			"summary":  h.Gateway.SummarizeToolAuditLogs(logs),
			"items":    logs,
		})
	})

	mux.HandleFunc("GET /approvals", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		filter := gateway.ApprovalRecordFilter{
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			TraceID:   strings.TrimSpace(r.URL.Query().Get("trace_id")),
			TaskID:    strings.TrimSpace(r.URL.Query().Get("task_id")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			ToolName:  strings.TrimSpace(r.URL.Query().Get("tool_name")),
			Since:     queryTime(r, "since"),
			Until:     queryTime(r, "until"),
		}
		records, err := h.Gateway.FilteredApprovalRecords(agentID, limit, filter)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		publicRecords := publicApprovalRecords(records)
		if wantsJSONL(r) {
			writeJSONL(w, publicRecords)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"limit":    limit,
			"filter":   filter,
			"items":    publicRecords,
		})
	})

	mux.HandleFunc("POST /approvals/decide", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		var body decideApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "", "invalid_json", "invalid JSON")
			return
		}
		agentID := strings.TrimSpace(body.AgentID)
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		record, err := h.Gateway.DecideApproval(agentID, strings.TrimSpace(body.ApprovalID), strings.TrimSpace(body.Decision), strings.TrimSpace(body.DecidedBy))
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, publicApprovalRecord(*record))
	})

	mux.HandleFunc("GET /traces", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 100)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		filter := gateway.TraceEventFilter{
			TraceID:   strings.TrimSpace(r.URL.Query().Get("trace_id")),
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			Span:      strings.TrimSpace(r.URL.Query().Get("span")),
			Event:     strings.TrimSpace(r.URL.Query().Get("event")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			Since:     queryTime(r, "since"),
			Until:     queryTime(r, "until"),
		}
		events, err := h.Gateway.TraceEvents(agentID, limit, filter)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"limit":    limit,
			"filter":   filter,
			"items":    events,
		})
	})

	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		entries, err := h.Gateway.SessionEntries(agentID, limit)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "limit": limit, "items": entries})
	})

	mux.HandleFunc("GET /memory", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		limit := queryLimit(r, 50)
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		records, err := h.Gateway.MemoryRecords(agentID, limit)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "limit": limit, "items": records})
	})

	mux.HandleFunc("GET /cron", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		jobs, err := h.Gateway.CronJobs(agentID)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "items": jobs})
	})

	mux.HandleFunc("POST /input", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}

		var body inputRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "", "invalid_json", "invalid JSON")
			return
		}
		if strings.TrimSpace(body.Text) == "" {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "missing 'text'")
			return
		}
		agentID := body.AgentID
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		mode, ok := mcRuntime.ParseExecutionMode(body.Mode)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "invalid 'mode'")
			return
		}
		sessionID := body.SessionID
		if sessionID == "" {
			sessionID = agentID
		}

		result, err := h.Gateway.HandleInputModeDetailed(r.Context(), body.Text, agentID, sessionID, "http", mode)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"agent_id":   result.AgentID,
			"request_id": result.RequestID,
			"task_id":    result.TaskID,
			"response":   result.Response,
		})
	})

	mux.HandleFunc("POST /tasks/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		var body cancelTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "", "invalid_json", "invalid JSON")
			return
		}
		if strings.TrimSpace(body.RequestID) == "" && strings.TrimSpace(body.TaskID) == "" {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "missing 'request_id' or 'task_id'")
			return
		}
		if err := h.Gateway.CancelTask(strings.TrimSpace(body.RequestID), strings.TrimSpace(body.TaskID)); err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"request_id": body.RequestID,
			"task_id":    body.TaskID,
			"status":     "cancelling",
		})
	})

	mux.HandleFunc("POST /tasks/retry", func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "", "unauthorized", "unauthorized")
			return
		}
		var body retryTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "", "invalid_json", "invalid JSON")
			return
		}
		if strings.TrimSpace(body.RequestID) == "" && strings.TrimSpace(body.TaskID) == "" {
			writeAPIError(w, http.StatusBadRequest, "", string(mcRuntime.CodeInvalidInput), "missing 'request_id' or 'task_id'")
			return
		}
		agentID := strings.TrimSpace(body.AgentID)
		if agentID == "" {
			agentID = h.DefaultAgent
		}
		override := -1
		if body.MaxRetries != nil {
			override = *body.MaxRetries
		}
		result, err := h.Gateway.RetryTaskAsync(r.Context(), agentID, strings.TrimSpace(body.RequestID), strings.TrimSpace(body.TaskID), override)
		if err != nil {
			writeRuntimeError(w, "", err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})
	return h.rateLimit(mux)
}

func (h *HTTPChannel) streamTask(w http.ResponseWriter, r *http.Request, agentID, requestID, taskID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	send := func() bool {
		limit := 1
		filter := gateway.ExecutionLogFilter{RequestID: requestID}
		if requestID == "" {
			limit = 50
		}
		tasks, err := h.Gateway.TaskViews(agentID, limit, filter)
		if err != nil {
			writeSSE(w, "error", map[string]string{"error": err.Error()})
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		if taskID != "" {
			tasks = filterTaskID(tasks, taskID)
		}
		if len(tasks) == 0 {
			writeSSE(w, "task", map[string]string{"status": "pending"})
			if flusher != nil {
				flusher.Flush()
			}
			return false
		}
		writeSSE(w, "task", tasks[0])
		if flusher != nil {
			flusher.Flush()
		}
		return terminalTaskStatus(tasks[0].Status)
	}

	if send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if send() {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		payload = []byte(`{"error":"encode_sse"}`)
	}
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

func filterTaskID(tasks []gateway.TaskView, taskID string) []gateway.TaskView {
	if taskID == "" {
		return tasks
	}
	for _, task := range tasks {
		if task.TaskID == taskID {
			return []gateway.TaskView{task}
		}
	}
	return nil
}

func terminalTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "awaiting_approval":
		return true
	default:
		return false
	}
}

func (h *HTTPChannel) authorized(r *http.Request) bool {
	if h.Gateway == nil || h.Gateway.Config == nil {
		return true
	}
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			key = strings.TrimSpace(auth[7:])
		}
	}
	return h.Gateway.Config.IsAPIKeyAllowed(key)
}

func (h *HTTPChannel) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.rateLimitPerMinute() <= 0 || publicEndpoint(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter, remaining := h.allowRequest(r)
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", h.rateLimitPerMinute()))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			writeAPIError(w, http.StatusTooManyRequests, "", "rate_limited", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *HTTPChannel) allowRequest(r *http.Request) (bool, int, int) {
	limit := h.rateLimitPerMinute()
	if limit <= 0 {
		return true, 0, 0
	}
	now := time.Now()
	identity := h.rateLimitIdentity(r)

	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if h.rateBuckets == nil {
		h.rateBuckets = make(map[string]rateBucket)
	}
	bucket := h.rateBuckets[identity]
	if bucket.WindowStart.IsZero() || now.Sub(bucket.WindowStart) >= time.Minute {
		bucket = rateBucket{WindowStart: now}
	}
	if bucket.Count >= limit {
		retryAfter := int(time.Until(bucket.WindowStart.Add(time.Minute)).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		return false, retryAfter, 0
	}
	bucket.Count++
	h.rateBuckets[identity] = bucket
	return true, 0, max(limit-bucket.Count, 0)
}

func (h *HTTPChannel) rateLimitPerMinute() int {
	if h.Gateway == nil || h.Gateway.Config == nil {
		return 0
	}
	return h.Gateway.Config.RateLimitPerMin
}

// CleanupRateBuckets removes stale rate limit buckets older than 5 minutes.
func (h *HTTPChannel) CleanupRateBuckets() int {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()

	if h.rateBuckets == nil {
		return 0
	}

	cutoff := time.Now().Add(-5 * time.Minute)
	removed := 0
	for key, bucket := range h.rateBuckets {
		if bucket.WindowStart.Before(cutoff) {
			delete(h.rateBuckets, key)
			removed++
		}
	}
	return removed
}

func (h *HTTPChannel) rateLimitIdentity(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return "key:" + key
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		if key := strings.TrimSpace(auth[7:]); key != "" {
			return "key:" + key
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return "remote:" + r.RemoteAddr
	}
	return "remote:" + host
}

func publicEndpoint(path string) bool {
	return path == "/health" || strings.HasPrefix(path, "/health/") || path == "/metrics"
}

// Stop shuts down the HTTP server.
func (h *HTTPChannel) Stop() error {
	if h.server != nil {
		return h.server.Shutdown(context.Background())
	}
	return nil
}

// Send is a no-op for HTTP channel.
func (h *HTTPChannel) Send(message string) error {
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONL(w http.ResponseWriter, items any) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	switch records := items.(type) {
	case []store.ToolAuditLog:
		for _, item := range records {
			_ = encoder.Encode(item)
		}
	case []store.ApprovalRecord:
		for _, item := range records {
			_ = encoder.Encode(item)
		}
	default:
		_ = encoder.Encode(items)
	}
}

func wantsJSONL(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	return format == "jsonl" || format == "ndjson"
}

func publicApprovalRecords(records []store.ApprovalRecord) []store.ApprovalRecord {
	out := make([]store.ApprovalRecord, len(records))
	for i, record := range records {
		out[i] = publicApprovalRecord(record)
	}
	return out
}

func publicApprovalRecord(record store.ApprovalRecord) store.ApprovalRecord {
	if len(record.ArgumentsRedacted) > 0 {
		record.Arguments = record.ArgumentsRedacted
	} else {
		record.Arguments = nil
	}
	return record
}

func writeRuntimeError(w http.ResponseWriter, requestID string, err error) {
	writeAPIError(w, mcRuntime.HTTPStatus(err), requestID, string(mcRuntime.CodeOf(err)), err.Error())
}

func writeAPIError(w http.ResponseWriter, status int, requestID, code, message string) {
	payload := map[string]string{
		"code":    code,
		"message": message,
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	writeJSON(w, status, map[string]any{"error": payload})
}

func queryLimit(r *http.Request, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	var limit int
	if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit <= 0 {
		return fallback
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func queryTime(r *http.Request, key string) time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
