// Package hands executes policy-bound tools for workspace tasks.
package hands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	mclog "go-nanoclaw/internal/log"
	"go-nanoclaw/internal/memory"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

type ToolStatus string

const (
	ToolStatusOK               ToolStatus = "ok"
	ToolStatusError            ToolStatus = "error"
	ToolStatusTimeout          ToolStatus = "timeout"
	ToolStatusDenied           ToolStatus = "denied"
	ToolStatusApprovalRequired ToolStatus = "approval_required"
)

type ToolResult struct {
	Name     string
	Status   ToolStatus
	Output   string
	Err      string
	Metadata map[string]any
}

// Hands owns tool schemas, policy checks, execution, and audit records.
type Hands struct {
	workspace   string
	memory      *memory.Memory
	policies    config.ToolPolicyConfig
	agentID     string
	store       store.Store
	customTools map[string]customTool
}

// New creates a tool runner.
func New(workspace string, mem *memory.Memory, policies config.ToolPolicyConfig) *Hands {
	return &Hands{
		workspace:   workspace,
		memory:      mem,
		policies:    policies,
		customTools: make(map[string]customTool),
	}
}

func (h *Hands) SetStore(agentID string, st store.Store) {
	h.agentID = agentID
	h.store = st
}

// RegisterTool registers a custom tool.
func (h *Hands) RegisterTool(name string, fn ToolFunc, schema brain.ToolSchema) {
	h.customTools[name] = customTool{handler: fn, schema: schema}
}

// GetToolSchemas returns tool schemas filtered by the allowed list.
func (h *Hands) GetToolSchemas(allowed []string) []brain.ToolSchema {
	var allSchemas []brain.ToolSchema
	allSchemas = append(allSchemas, BuiltinToolSchemas...)
	for _, ct := range h.customTools {
		allSchemas = append(allSchemas, ct.schema)
	}

	if len(allowed) == 0 || containsStr(allowed, "*") {
		return allSchemas
	}

	var filtered []brain.ToolSchema
	for _, s := range allSchemas {
		if containsStr(allowed, s.Name) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// Execute runs a tool by name with the given arguments.
func (h *Hands) Execute(ctx context.Context, toolName string, arguments map[string]any) string {
	result, _ := h.ExecuteStructured(ctx, toolName, arguments)
	return result.AsMessage()
}

func (r ToolResult) AsMessage() string {
	if r.Output != "" {
		return r.Output
	}
	if r.Err != "" {
		return "Error: " + r.Err
	}
	return ""
}

// ExecuteStructured runs a tool and returns a structured result.
func (h *Hands) ExecuteStructured(ctx context.Context, toolName string, arguments map[string]any) (ToolResult, error) {
	return h.executeStructured(ctx, toolName, arguments, true)
}

// ExecuteApproved runs a tool after an external approval decision.
func (h *Hands) ExecuteApproved(ctx context.Context, toolName string, arguments map[string]any) (ToolResult, error) {
	return h.executeStructured(ctx, toolName, arguments, false)
}

func (h *Hands) executeStructured(ctx context.Context, toolName string, arguments map[string]any, enforceApproval bool) (ToolResult, error) {
	requestedTool := toolName
	argsJSON, _ := json.Marshal(arguments)
	truncated := string(argsJSON)
	if len(truncated) > 200 {
		truncated = truncated[:200]
	}
	if mclog.Verbosity() >= 2 {
		mclog.SubStep(fmt.Sprintf("ToolRunner.Execute: %s", toolName), "args", truncated)
	}
	if enforceApproval && h.approvalRequired(requestedTool) {
		toolResult := ToolResult{
			Name:   requestedTool,
			Status: ToolStatusApprovalRequired,
			Output: fmt.Sprintf("Approval required before executing tool %q with arguments: %s", toolName, truncated),
			Metadata: map[string]any{
				"approval_required": true,
			},
		}
		h.persistApproval(ctx, requestedTool, arguments)
		h.auditTool(ctx, requestedTool, arguments, toolResult, nil)
		return toolResult, nil
	}

	if ct, ok := h.customTools[toolName]; ok {
		result, err := ct.handler(ctx, arguments)
		if err != nil {
			slog.Error("Custom tool failed", append([]any{"name", toolName, "error", err}, mcRuntime.LogAttrs(mcRuntime.FromContext(ctx))...)...)
			toolResult := h.newToolResult(ctx, toolName, "", err)
			h.auditTool(ctx, toolName, arguments, toolResult, err)
			return toolResult, err
		}
		toolResult := ToolResult{Name: toolName, Status: ToolStatusOK, Output: mcRuntime.TruncateToolOutput(ctx, result, 20000), Metadata: map[string]any{}}
		h.auditTool(ctx, toolName, arguments, toolResult, nil)
		return toolResult, nil
	}

	var output string
	var err error
	switch toolName {
	case "run_command":
		output, err = h.toolShellExec(ctx, arguments)
	case "read_workspace_file":
		output, err = h.toolFileRead(arguments)
	case "write_workspace_file":
		output, err = h.toolFileWrite(arguments)
	case "list_workspace":
		output, err = h.toolFileList(arguments)
	case "fetch_url":
		output, err = h.toolHTTPGet(ctx, arguments)
	case "remember_note":
		output, err = h.toolMemoryAppend(arguments)
	case "read_note":
		output, err = h.toolMemoryRead(arguments)
	default:
		err = mcRuntime.Errorf(mcRuntime.CodeToolFailed, "unknown tool '%s'", toolName)
	}
	if err != nil {
		toolResult := h.newToolResult(ctx, toolName, output, err)
		h.auditTool(ctx, toolName, arguments, toolResult, err)
		return toolResult, err
	}
	toolResult := ToolResult{Name: toolName, Status: ToolStatusOK, Output: output, Metadata: map[string]any{}}
	h.auditTool(ctx, toolName, arguments, toolResult, nil)
	return toolResult, nil
}

func (h *Hands) toolShellExec(ctx context.Context, args map[string]any) (string, error) {
	command, _ := args["command"].(string)
	if !h.policies.ShellAllowed(command) {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolDenied, "shell command denied by policy")
	}
	timeout := 30
	if t, ok := args["timeout"]; ok {
		switch v := t.(type) {
		case float64:
			timeout = int(v)
		case int:
			timeout = v
		}
	}

	if execCtx := mcRuntime.FromContext(ctx); execCtx != nil && execCtx.Budget.MaxWallClock > 0 {
		remaining := time.Until(execCtx.Deadline)
		if remaining > 0 && remaining < time.Duration(timeout)*time.Second {
			timeout = int(remaining / time.Second)
			if timeout < 1 {
				timeout = 1
			}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = h.workspace

	output, err := cmd.CombinedOutput()
	result := string(output)

	if ctx.Err() == context.DeadlineExceeded {
		return "", mcRuntime.Errorf(mcRuntime.CodeTimeout, "command timed out after %ds", timeout)
	}
	if ctx.Err() == context.Canceled {
		return "", mcRuntime.Errorf(mcRuntime.CodeCancelled, "command cancelled")
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "shell exec failed")
		}
	}

	result = strings.TrimSpace(result)
	result += fmt.Sprintf("\n[exit code: %d]", exitCode)
	return result, nil
}

func (h *Hands) toolFileRead(args map[string]any) (string, error) {
	pathStr, _ := args["path"].(string)
	path, err := h.resolvePath(pathStr)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve path")
	}
	if err := h.ensureExistingPathInWorkspace(path); err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", mcRuntime.Errorf(mcRuntime.CodeToolFailed, "file not found: %s", pathStr)
		}
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "read file")
	}

	content := string(data)
	if len(content) > 50000 {
		content = content[:50000] + "\n\n[...truncated at 50000 chars...]"
	}
	return content, nil
}

func (h *Hands) toolFileWrite(args map[string]any) (string, error) {
	pathStr, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if !h.policies.FileWriteEnabled {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolDenied, "file write denied by policy")
	}

	path, err := h.resolvePath(pathStr)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve path")
	}
	if err := h.ensureWritePathInWorkspace(path); err != nil {
		return "", err
	}
	relPath, err := filepath.Rel(filepath.Clean(h.workspace), path)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "derive relative path")
	}
	if !h.policies.FileWritePathAllowed(relPath) {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolDenied, "file write path denied by policy")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "create parent directory")
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "write file")
	}

	return fmt.Sprintf("Written %d chars to %s", len(content), pathStr), nil
}

func (h *Hands) toolFileList(args map[string]any) (string, error) {
	pathStr, _ := args["path"].(string)
	if pathStr == "" {
		pathStr = "."
	}

	path, err := h.resolvePath(pathStr)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve path")
	}
	if err := h.ensureExistingPathInWorkspace(path); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", mcRuntime.Errorf(mcRuntime.CodeToolFailed, "path not found: %s", pathStr)
		}
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "list path")
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	if len(entries) == 0 {
		return "(empty directory)", nil
	}

	var lines []string
	for _, e := range entries {
		prefix := "[file]"
		if e.IsDir() {
			prefix = "[dir] "
		}
		lines = append(lines, fmt.Sprintf("%s %s", prefix, e.Name()))
	}
	return strings.Join(lines, "\n"), nil
}

func (h *Hands) toolHTTPGet(ctx context.Context, args map[string]any) (string, error) {
	url, _ := args["url"].(string)
	if err := h.validateHTTPURL(url); err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "too many redirects")
			}
			if err := h.validateHTTPURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "build http request")
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", mcRuntime.Wrap(mcRuntime.CodeCancelled, err, "http get cancelled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", mcRuntime.Wrap(mcRuntime.CodeTimeout, err, "http get timed out")
		}
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "http get failed")
	}
	defer resp.Body.Close()

	limit := int64(mcRuntime.MaxToolOutputBytes(ctx, 20000))
	if limit <= 0 {
		limit = 20000
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "read http response")
	}

	text := string(body)
	if int64(len(body)) > limit {
		text = text[:limit] + "\n\n[...truncated...]"
	}
	return fmt.Sprintf("[HTTP %d]\n%s", resp.StatusCode, text), nil
}

func (h *Hands) toolMemoryAppend(args map[string]any) (string, error) {
	entry, _ := args["entry"].(string)
	if h.memory == nil {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolFailed, "memory not initialized")
	}
	category, _ := args["category"].(string)
	if category == "" {
		category = "notes"
	}
	confidence := 1.0
	if raw, ok := args["confidence"]; ok {
		switch v := raw.(type) {
		case float64:
			confidence = v
		case int:
			confidence = float64(v)
		}
	}
	var expiresAt time.Time
	if raw, ok := args["ttl_days"]; ok {
		var ttlDays int
		switch v := raw.(type) {
		case float64:
			ttlDays = int(v)
		case int:
			ttlDays = v
		}
		if ttlDays > 0 {
			expiresAt = time.Now().UTC().Add(time.Duration(ttlDays) * 24 * time.Hour)
		}
	}
	if err := h.memory.AppendCategorizedMemoryWithMetadata(category, entry, "remember_note", confidence, expiresAt); err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "append memory")
	}
	truncated := entry
	if len(truncated) > 100 {
		truncated = truncated[:100]
	}
	return fmt.Sprintf("Recorded note: %s", truncated), nil
}

func (h *Hands) toolMemoryRead(args map[string]any) (string, error) {
	filename, _ := args["filename"].(string)
	if h.memory == nil {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolFailed, "memory not initialized")
	}
	content, err := h.memory.ReadFile(filename)
	if err != nil {
		return "", mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "read memory file")
	}
	if content == "" {
		return "", mcRuntime.Errorf(mcRuntime.CodeToolFailed, "file not found: %s", filename)
	}
	return content, nil
}

func (h *Hands) resolvePath(pathStr string) (string, error) {
	var resolved string
	if filepath.IsAbs(pathStr) {
		resolved = filepath.Clean(pathStr)
	} else {
		resolved = filepath.Clean(filepath.Join(h.workspace, pathStr))
	}

	ws := filepath.Clean(h.workspace)
	rel, err := filepath.Rel(ws, resolved)
	if err != nil || !pathWithinRel(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", pathStr)
	}
	return resolved, nil
}

func (h *Hands) ensureExistingPathInWorkspace(path string) error {
	realWorkspace, err := h.realWorkspace()
	if err != nil {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve workspace")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve symlink")
	}
	if !pathWithin(realWorkspace, realPath) {
		return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "path escapes workspace through symlink")
	}
	return nil
}

func (h *Hands) ensureWritePathInWorkspace(path string) error {
	realWorkspace, err := h.realWorkspace()
	if err != nil {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve workspace")
	}

	parent := filepath.Dir(path)
	realParent, err := realPathForPossiblyMissing(parent)
	if err != nil {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve parent symlink")
	}
	if !pathWithin(realWorkspace, realParent) {
		return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "file write path escapes workspace through symlink")
	}

	if _, err := os.Lstat(path); err == nil {
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "resolve target symlink")
		}
		if !pathWithin(realWorkspace, realPath) {
			return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "file write target escapes workspace through symlink")
		}
	} else if !os.IsNotExist(err) {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "stat write target")
	}

	return nil
}

func (h *Hands) realWorkspace() (string, error) {
	ws := filepath.Clean(h.workspace)
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		if os.IsNotExist(err) {
			return ws, nil
		}
		return "", err
	}
	return filepath.Clean(real), nil
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	return err == nil && pathWithinRel(rel)
}

func pathWithinRel(rel string) bool {
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func realPathForPossiblyMissing(path string) (string, error) {
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	missing := []string{}
	current := path
	for {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return filepath.Clean(real), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// GetToolDescription returns the description for a tool by name.
func (h *Hands) GetToolDescription(name string) string {
	for _, s := range BuiltinToolSchemas {
		if s.Name == name {
			return s.Description
		}
	}
	if ct, ok := h.customTools[name]; ok {
		return ct.schema.Description
	}
	return ""
}

func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

var bearerTokenPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)

func redactMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	redacted := make(map[string]any, len(input))
	for key, value := range input {
		redacted[key] = redactValue(key, value)
	}
	return redacted
}

func redactValue(key string, value any) any {
	if sensitiveKey(key) {
		return "[REDACTED]"
	}
	switch v := value.(type) {
	case map[string]any:
		return redactMap(v)
	case map[string]string:
		out := make(map[string]any, len(v))
		for nestedKey, nestedValue := range v {
			out[nestedKey] = redactValue(nestedKey, nestedValue)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactValue("", item)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactString(item)
		}
		return out
	case string:
		return redactString(v)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{"api_key", "apikey", "token", "password", "passwd", "secret", "authorization", "cookie", "credential"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactString(value string) string {
	return bearerTokenPattern.ReplaceAllString(value, "Bearer [REDACTED]")
}

func (h *Hands) approvalRequired(toolName string) bool {
	return containsStr(h.policies.ApprovalRequired, "*") ||
		containsStr(h.policies.ApprovalRequired, toolName)
}

func (h *Hands) newToolResult(ctx context.Context, toolName, output string, err error) ToolResult {
	status := ToolStatusError
	switch mcRuntime.CodeOf(err) {
	case mcRuntime.CodeCancelled:
		status = ToolStatusError
	case mcRuntime.CodeTimeout:
		status = ToolStatusTimeout
	case mcRuntime.CodeToolDenied:
		status = ToolStatusDenied
	}
	return ToolResult{
		Name:     toolName,
		Status:   status,
		Output:   mcRuntime.TruncateToolOutput(ctx, output, 20000),
		Err:      err.Error(),
		Metadata: map[string]any{},
	}
}

func (h *Hands) auditTool(ctx context.Context, toolName string, arguments map[string]any, result ToolResult, err error) {
	if h.store == nil {
		return
	}
	execCtx := mcRuntime.FromContext(ctx)
	log := store.ToolAuditLog{
		ToolName:   toolName,
		Status:     string(result.Status),
		Arguments:  redactMap(arguments),
		Output:     mcRuntime.TruncateToolOutput(ctx, result.Output, 1000),
		OccurredAt: time.Now(),
	}
	if execCtx != nil {
		log.TraceID = execCtx.IDs.TraceID
		log.RequestID = execCtx.IDs.RequestID
		log.SessionID = execCtx.IDs.SessionID
		log.TaskID = execCtx.IDs.TaskID
		log.AgentID = execCtx.AgentID
	} else {
		log.AgentID = h.agentID
	}
	if err != nil {
		log.Error = err.Error()
	}
	if saveErr := h.store.SaveToolAuditLog(log); saveErr != nil {
		slog.Error("Persist tool audit failed", append([]any{"tool", toolName, "error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
	h.auditTrace(execCtx, toolName, result, err)
}

func (h *Hands) persistApproval(ctx context.Context, toolName string, arguments map[string]any) {
	if h.store == nil {
		return
	}
	execCtx := mcRuntime.FromContext(ctx)
	now := time.Now().UTC()
	record := store.ApprovalRecord{
		ApprovalID:        fmt.Sprintf("approval-%d", now.UnixNano()),
		ToolName:          toolName,
		Arguments:         arguments,
		ArgumentsRedacted: redactMap(arguments),
		Status:            "pending",
		Reason:            "tool policy requires approval",
		RequestedAt:       now,
	}
	if execCtx != nil {
		record.TraceID = execCtx.IDs.TraceID
		record.RequestID = execCtx.IDs.RequestID
		record.SessionID = execCtx.IDs.SessionID
		record.TaskID = execCtx.IDs.TaskID
		record.AgentID = execCtx.AgentID
	} else {
		record.AgentID = h.agentID
	}
	if saveErr := h.store.SaveApprovalRecord(record.AgentID, record); saveErr != nil {
		slog.Error("Persist approval record failed", append([]any{"tool", toolName, "error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func (h *Hands) auditTrace(execCtx *mcRuntime.ExecutionContext, toolName string, result ToolResult, err error) {
	if execCtx == nil || h.store == nil {
		return
	}
	event := store.TraceEvent{
		TraceID:   execCtx.IDs.TraceID,
		RequestID: execCtx.IDs.RequestID,
		SessionID: execCtx.IDs.SessionID,
		TaskID:    execCtx.IDs.TaskID,
		AgentID:   execCtx.AgentID,
		Source:    execCtx.Source,
		Span:      "tool",
		Event:     toolName,
		Status:    string(result.Status),
		Metadata:  result.Metadata,
		At:        time.Now().UTC(),
	}
	if err != nil {
		event.Error = err.Error()
	}
	if saveErr := h.store.SaveTraceEvent(execCtx.AgentID, event); saveErr != nil {
		slog.Error("Persist tool trace failed", append([]any{"tool", toolName, "error", saveErr}, mcRuntime.LogAttrs(execCtx)...)...)
	}
}

func (h *Hands) validateHTTPURL(rawURL string) error {
	if !h.policies.HTTPEnabled {
		return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "http access denied by policy")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return mcRuntime.Wrap(mcRuntime.CodeToolFailed, err, "invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "unsupported url scheme")
	}
	host := parsed.Hostname()
	if host == "" {
		return mcRuntime.Errorf(mcRuntime.CodeToolFailed, "missing host")
	}
	if isPrivateHost(host) {
		return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "private network targets are denied")
	}
	if len(h.policies.HTTPAllowlist) == 0 {
		return nil
	}
	for _, allowed := range h.policies.HTTPAllowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return mcRuntime.Errorf(mcRuntime.CodeToolDenied, "http host denied by allowlist")
}

func isPrivateHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}
