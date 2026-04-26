package hands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/memory"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

func TestExecuteStructuredUnknownTool(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "missing_tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if result.Status != ToolStatusError {
		t.Fatalf("expected error status, got %s", result.Status)
	}
	if result.Name != "missing_tool" {
		t.Fatalf("expected tool name to be preserved, got %q", result.Name)
	}
}

func TestBuiltinToolSchemasKeepCompatibleNamesWithoutLegacyDescriptions(t *testing.T) {
	wantNames := map[string]bool{
		"run_command":          false,
		"read_workspace_file":  false,
		"write_workspace_file": false,
		"list_workspace":       false,
		"fetch_url":            false,
		"remember_note":        false,
		"read_note":            false,
	}
	retiredPhrases := []string{
		"Execute a shell command and return stdout/stderr. Use for system operations, git, etc.",
		"Read the contents of a file.",
		"Write content to a file. Creates parent directories if needed.",
		"List files and directories at a given path.",
		"Make an HTTP GET request and return the response body.",
		"Append a learning or note to persistent MEMORY.md.",
		"Read a file from the workspace directory.",
	}

	for _, schema := range BuiltinToolSchemas {
		if _, ok := wantNames[schema.Name]; ok {
			wantNames[schema.Name] = true
		}
		for _, phrase := range retiredPhrases {
			if strings.Contains(schema.Description, phrase) {
				t.Fatalf("tool %s still uses retired description %q", schema.Name, phrase)
			}
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Fatalf("compatible tool name %s missing from schema list", name)
		}
	}
}

func TestExecuteStructuredTruncatesCustomToolOutputByBudget(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})
	h.RegisterTool("large", func(ctx context.Context, args map[string]any) (string, error) {
		return strings.Repeat("x", 32), nil
	}, brain.ToolSchema{Name: "large"})

	execCtx := mcRuntime.NewExecution("main", "s-1", "test")
	execCtx.Budget.MaxToolOutputBytes = 10
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)

	result, err := h.ExecuteStructured(ctx, "large", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Output, "[...truncated...]") {
		t.Fatalf("expected truncated output, got %q", result.Output)
	}
}

func TestShellExecDeniedByPolicy(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		ShellEnabled:   true,
		ShellAllowlist: []string{"git status"},
	})

	result, err := h.ExecuteStructured(context.Background(), "run_command", map[string]any{"command": "rm -rf /tmp/x"})
	if err == nil {
		t.Fatal("expected policy denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
}

func TestToolApprovalRequiredSkipsExecution(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		ShellAllowlist:   []string{"echo"},
		HTTPEnabled:      true,
		ApprovalRequired: []string{"run_command"},
	})

	result, err := h.ExecuteStructured(context.Background(), "run_command", map[string]any{"command": "echo should-not-run"})
	if err != nil {
		t.Fatalf("approval-required tool should not return execution error: %v", err)
	}
	if result.Status != ToolStatusApprovalRequired {
		t.Fatalf("expected approval_required status, got %s", result.Status)
	}
	if !strings.Contains(result.Output, "Approval required") || !strings.Contains(result.Output, "should-not-run") {
		t.Fatalf("expected approval proposal output, got %q", result.Output)
	}
}

func TestRetiredToolAliasDoesNotExecutePrimaryTool(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		ShellAllowlist:   []string{"echo"},
	})

	retiredName := "shell" + "_" + "exec"
	result, err := h.ExecuteStructured(context.Background(), retiredName, map[string]any{"command": "echo alias-ok"})
	if err == nil {
		t.Fatal("expected retired tool name to fail")
	}
	if result.Status != ToolStatusError || result.Name != retiredName {
		t.Fatalf("expected retired tool name to be rejected, got %+v", result)
	}
}

func TestRetiredToolNamesDoNotExposePrimarySchemas(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{})

	retiredNames := []string{
		"shell" + "_" + "exec",
		"file" + "_" + "read",
		"file" + "_" + "write",
		"file" + "_" + "list",
		"http" + "_" + "get",
		"memory" + "_" + "append",
		"memory" + "_" + "read",
	}
	for _, name := range retiredNames {
		schemas := h.GetToolSchemas([]string{name})
		if len(schemas) != 0 {
			t.Fatalf("expected retired tool name %s to expose no schemas, got %+v", name, schemas)
		}
	}
}

func TestToolApprovalRequiredPersistsAudit(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
		ApprovalRequired: []string{"*"},
	})
	st := store.NewFSStore(baseDir)
	h.SetStore("main", st)

	execCtx := mcRuntime.NewExecution("main", "session-1", "test")
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)
	_, err := h.ExecuteStructured(ctx, "write_workspace_file", map[string]any{"path": "x.txt", "content": "x"})
	if err != nil {
		t.Fatalf("approval-required tool should not return execution error: %v", err)
	}

	logs, err := st.LoadToolAuditLogs("main", 10)
	if err != nil {
		t.Fatalf("load tool audit logs: %v", err)
	}
	if len(logs) != 1 || logs[0].Status != string(ToolStatusApprovalRequired) || logs[0].ToolName != "write_workspace_file" {
		t.Fatalf("expected approval audit log, got %+v", logs)
	}
	approvals, err := st.LoadApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("load approval records: %v", err)
	}
	if len(approvals) != 1 || approvals[0].Status != "pending" || approvals[0].ToolName != "write_workspace_file" {
		t.Fatalf("expected pending approval record, got %+v", approvals)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file not to be written, stat err=%v", err)
	}
}

func TestFileWriteDeniedByPolicy(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: false,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{"path": "a.txt", "content": "x"})
	if err == nil {
		t.Fatal("expected policy denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
}

func TestFileWriteDeniedByPathAllowlist(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled:   true,
		FileWriteAllowlist: []string{"notes"},
		ShellEnabled:       true,
		HTTPEnabled:        true,
	})

	result, err := h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{
		"path":    "logs/output.txt",
		"content": "x",
	})
	if err == nil {
		t.Fatal("expected path policy denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
}

func TestFileWriteAllowedByPathAllowlist(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled:   true,
		FileWriteAllowlist: []string{"notes"},
		ShellEnabled:       true,
		HTTPEnabled:        true,
	})

	result, err := h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{
		"path":    "notes/today.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %s", result.Status)
	}
	data, readErr := os.ReadFile(filepath.Join(baseDir, "notes", "today.txt"))
	if readErr != nil {
		t.Fatalf("read written file: %v", readErr)
	}
	if string(data) != "hello" {
		t.Fatalf("expected file content hello, got %q", string(data))
	}
}

func TestFileReadDeniedWhenSymlinkEscapesWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(baseDir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "read_workspace_file", map[string]any{"path": "link.txt"})
	if err == nil {
		t.Fatal("expected symlink escape denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
}

func TestFileWriteDeniedWhenTargetSymlinkEscapesWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(baseDir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{
		"path":    "link.txt",
		"content": "changed",
	})
	if err == nil {
		t.Fatal("expected symlink escape denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
	data, readErr := os.ReadFile(outsideFile)
	if readErr != nil {
		t.Fatalf("read outside file: %v", readErr)
	}
	if string(data) != "secret" {
		t.Fatalf("outside file was modified: %q", string(data))
	}
}

func TestFileWriteDeniedWhenParentSymlinkEscapesWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(baseDir, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{
		"path":    "outside/new.txt",
		"content": "changed",
	})
	if err == nil {
		t.Fatal("expected parent symlink escape denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside file not to be created, stat err=%v", statErr)
	}
}

func TestFileToolsAllowSymlinkInsideWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	targetDir := filepath.Join(baseDir, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("create target dir: %v", err)
	}
	targetFile := filepath.Join(targetDir, "note.txt")
	if err := os.WriteFile(targetFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	if err := os.Symlink(targetFile, filepath.Join(baseDir, "file-link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(baseDir, "dir-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	readResult, err := h.ExecuteStructured(context.Background(), "read_workspace_file", map[string]any{"path": "file-link.txt"})
	if err != nil {
		t.Fatalf("read symlink inside workspace: %v", err)
	}
	if readResult.Output != "hello" {
		t.Fatalf("expected hello, got %q", readResult.Output)
	}

	listResult, err := h.ExecuteStructured(context.Background(), "list_workspace", map[string]any{"path": "dir-link"})
	if err != nil {
		t.Fatalf("list symlink dir inside workspace: %v", err)
	}
	if !strings.Contains(listResult.Output, "note.txt") {
		t.Fatalf("expected note.txt in listing, got %q", listResult.Output)
	}

	_, err = h.ExecuteStructured(context.Background(), "write_workspace_file", map[string]any{
		"path":    "file-link.txt",
		"content": "updated",
	})
	if err != nil {
		t.Fatalf("write symlink inside workspace: %v", err)
	}
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read target file: %v", err)
	}
	if string(data) != "updated" {
		t.Fatalf("expected updated target, got %q", string(data))
	}
}

func TestHTTPGetDeniedPrivateHost(t *testing.T) {
	h := New(t.TempDir(), nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})

	result, err := h.ExecuteStructured(context.Background(), "fetch_url", map[string]any{"url": "http://localhost/test"})
	if err == nil {
		t.Fatal("expected policy denial")
	}
	if result.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %s", result.Status)
	}
}

func TestMemoryAppendPersistsStructuredRecord(t *testing.T) {
	baseDir := t.TempDir()
	mem := memory.New(filepath.Join(baseDir, "workspace"), 20000)
	st := store.NewFSStore(baseDir)
	mem.SetStore("main", st)
	h := New(filepath.Join(baseDir, "workspace"), mem, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		HTTPEnabled:      true,
	})
	h.SetStore("main", st)

	_, err := h.ExecuteStructured(context.Background(), "remember_note", map[string]any{
		"entry":      "favorite language is Go",
		"category":   "facts",
		"confidence": 0.75,
		"ttl_days":   7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	records, err := st.LoadMemoryRecords("main", 10)
	if err != nil {
		t.Fatalf("load memory records: %v", err)
	}
	if len(records) == 0 || records[len(records)-1].Category != "facts" || records[len(records)-1].Confidence != 0.75 || records[len(records)-1].ExpiresAt.IsZero() {
		t.Fatalf("expected facts memory record, got %+v", records)
	}
}

func TestToolAuditPersistsLog(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     false,
		HTTPEnabled:      true,
	})
	st := store.NewFSStore(baseDir)
	h.SetStore("main", st)

	execCtx := mcRuntime.NewExecution("main", "session-1", "test")
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)
	_, _ = h.ExecuteStructured(ctx, "run_command", map[string]any{"command": "echo hi"})

	auditPath := filepath.Join(baseDir, "tool-audit", "main.jsonl")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read tool audit: %v", err)
	}
	if !strings.Contains(string(data), "run_command") {
		t.Fatalf("expected tool audit entry, got %s", string(data))
	}
}

func TestToolAuditRedactsSensitiveArguments(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		ShellAllowlist:   []string{"echo"},
		HTTPEnabled:      true,
	})
	st := store.NewFSStore(baseDir)
	h.SetStore("main", st)

	execCtx := mcRuntime.NewExecution("main", "session-1", "test")
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)
	_, err := h.ExecuteStructured(ctx, "run_command", map[string]any{
		"command": "echo hi",
		"api_key": "secret-key",
		"headers": map[string]any{
			"Authorization": "Bearer secret-token",
		},
	})
	if err != nil {
		t.Fatalf("execute shell: %v", err)
	}

	logs, err := st.LoadToolAuditLogs("main", 10)
	if err != nil {
		t.Fatalf("load tool audit logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one audit log, got %+v", logs)
	}
	if logs[0].Arguments["api_key"] != "[REDACTED]" {
		t.Fatalf("expected api_key redacted, got %+v", logs[0].Arguments)
	}
	headers, ok := logs[0].Arguments["headers"].(map[string]any)
	if !ok || headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("expected nested authorization redacted, got %+v", logs[0].Arguments)
	}
}

func TestApprovalRecordKeepsRawArgumentsAndAddsRedactedCopy(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ApprovalRequired: []string{"write_workspace_file"},
	})
	st := store.NewFSStore(baseDir)
	h.SetStore("main", st)

	execCtx := mcRuntime.NewExecution("main", "session-1", "test")
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)
	_, err := h.ExecuteStructured(ctx, "write_workspace_file", map[string]any{
		"path":     "secret.txt",
		"content":  "payload",
		"password": "secret-password",
	})
	if err != nil {
		t.Fatalf("approval-required tool should not return execution error: %v", err)
	}

	records, err := st.LoadApprovalRecords("main", 10)
	if err != nil {
		t.Fatalf("load approval records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one approval record, got %+v", records)
	}
	if records[0].Arguments["password"] != "secret-password" {
		t.Fatalf("expected raw arguments retained for approval execution, got %+v", records[0].Arguments)
	}
	if records[0].ArgumentsRedacted["password"] != "[REDACTED]" {
		t.Fatalf("expected redacted approval arguments, got %+v", records[0].ArgumentsRedacted)
	}
}

func TestToolExecutionPersistsTraceEvent(t *testing.T) {
	baseDir := t.TempDir()
	h := New(baseDir, nil, config.ToolPolicyConfig{
		FileWriteEnabled: true,
		ShellEnabled:     true,
		ShellAllowlist:   []string{"echo"},
		HTTPEnabled:      true,
	})
	st := store.NewFSStore(baseDir)
	h.SetStore("main", st)

	execCtx := mcRuntime.NewExecution("main", "session-1", "test")
	ctx := mcRuntime.WithExecutionContext(context.Background(), execCtx)
	_, err := h.ExecuteStructured(ctx, "run_command", map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("execute shell: %v", err)
	}

	events, err := st.LoadTraceEvents("main", 10)
	if err != nil {
		t.Fatalf("load trace events: %v", err)
	}
	if len(events) != 1 || events[0].TraceID != execCtx.IDs.TraceID || events[0].Span != "tool" || events[0].Event != "run_command" {
		t.Fatalf("expected shell trace event, got %+v", events)
	}
}
