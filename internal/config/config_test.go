package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolPolicyConfigFileWritePathAllowed(t *testing.T) {
	policy := ToolPolicyConfig{
		FileWriteEnabled:   true,
		FileWriteAllowlist: []string{"notes", "reports/daily"},
	}

	tests := []struct {
		path string
		want bool
	}{
		{path: "notes/today.txt", want: true},
		{path: "reports/daily/2026-04-24.md", want: true},
		{path: "reports/weekly/2026-04-24.md", want: false},
		{path: "scratch.txt", want: false},
	}

	for _, tt := range tests {
		if got := policy.FileWritePathAllowed(tt.path); got != tt.want {
			t.Fatalf("path %q: expected %v, got %v", tt.path, tt.want, got)
		}
	}
}

func TestToolPolicyConfigShellAllowed(t *testing.T) {
	policy := ToolPolicyConfig{
		ShellEnabled:   true,
		ShellAllowlist: []string{"git status", "go test"},
	}

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "exact command", command: "git status", want: true},
		{name: "allowed command with args", command: "go test ./...", want: true},
		{name: "unknown command", command: "rm -rf /tmp/x", want: false},
		{name: "prefix spoof", command: "git statusx", want: false},
		{name: "semicolon chaining", command: "git status; rm -rf /tmp/x", want: false},
		{name: "and chaining", command: "go test ./... && rm -rf /tmp/x", want: false},
		{name: "pipe chaining", command: "git status | cat", want: false},
		{name: "redirection", command: "git status > out.txt", want: false},
		{name: "command substitution", command: "git status $(rm -rf /tmp/x)", want: false},
	}

	for _, tt := range tests {
		if got := policy.ShellAllowed(tt.command); got != tt.want {
			t.Fatalf("%s: ShellAllowed(%q) expected %v, got %v", tt.name, tt.command, tt.want, got)
		}
	}
}

func TestConfigIsAPIKeyAllowed(t *testing.T) {
	cfg := &Config{APIKeys: []string{"secret-1", "secret-2"}}

	if !cfg.IsAPIKeyAllowed("secret-1") {
		t.Fatal("expected configured key to be allowed")
	}
	if cfg.IsAPIKeyAllowed("nope") {
		t.Fatal("expected unknown key to be denied")
	}
	if cfg.IsAPIKeyAllowed("") {
		t.Fatal("expected empty key to be denied")
	}
}

func TestConfigIsAPIKeyAllowedFromEnvRef(t *testing.T) {
	t.Setenv("NANOCLAW_TEST_API_KEY", "env-secret")
	cfg := &Config{APIKeys: []string{"env:NANOCLAW_TEST_API_KEY"}}

	if !cfg.IsAPIKeyAllowed("env-secret") {
		t.Fatal("expected env referenced key to be allowed")
	}
	if cfg.IsAPIKeyAllowed("NANOCLAW_TEST_API_KEY") {
		t.Fatal("expected env var name not to be accepted as key")
	}
}

func TestLoadRateLimitPerMinute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("rate_limit_per_minute: 42\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RateLimitPerMin != 42 {
		t.Fatalf("expected rate limit 42, got %d", cfg.RateLimitPerMin)
	}
}

func TestLoadExecutionBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("max_wall_clock_seconds: 30\nmax_tool_rounds: 3\nmax_tool_calls: 5\nmax_tool_output_bytes: 1024\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.MaxWallClockSec != 30 || cfg.MaxToolRounds != 3 || cfg.MaxToolCalls != 5 || cfg.MaxToolOutputBytes != 1024 {
		t.Fatalf("unexpected execution budget: %+v", cfg)
	}
}

func TestLoadMaxSubagentsPerTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("agents:\n  main:\n    max_subagents_per_task: 2\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Agents["main"].MaxSubagents != 2 {
		t.Fatalf("expected max subagents 2, got %d", cfg.Agents["main"].MaxSubagents)
	}
}

func TestLoadToolApprovalRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("agents:\n  main:\n    tool_policies:\n      approval_required:\n        - run_command\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	got := cfg.Agents["main"].ToolPolicies.ApprovalRequired
	if len(got) != 1 || got[0] != "run_command" {
		t.Fatalf("expected approval_required run_command, got %+v", got)
	}
}

func TestLoadToolApprovalTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("agents:\n  main:\n    tool_policies:\n      approval_timeout_minutes: 15\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Agents["main"].ToolPolicies.ApprovalTimeoutMin != 15 {
		t.Fatalf("expected approval timeout 15, got %d", cfg.Agents["main"].ToolPolicies.ApprovalTimeoutMin)
	}
}

func TestConfigFingerprintRedactsSecrets(t *testing.T) {
	cfg := NewConfig()
	cfg.ConfigVersion = "v1"
	cfg.APIKeys = []string{"key-a"}
	cfg.Agents["main"].Brain.APIKey = "model-key-a"

	fp1 := cfg.Fingerprint()
	if fp1 == "" {
		t.Fatal("expected fingerprint")
	}

	cfg.APIKeys = []string{"key-b"}
	cfg.Agents["main"].Brain.APIKey = "model-key-b"
	fp2 := cfg.Fingerprint()
	if fp2 != fp1 {
		t.Fatalf("expected fingerprint to ignore secret values, got %q and %q", fp1, fp2)
	}

	cfg.ConfigVersion = "v2"
	fp3 := cfg.Fingerprint()
	if fp3 == fp1 {
		t.Fatalf("expected fingerprint to change with config version")
	}
}
