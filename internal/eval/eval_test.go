package eval

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/hands"
	"go-nanoclaw/internal/memory"
	mcRuntime "go-nanoclaw/internal/runtime"
	"go-nanoclaw/internal/store"
)

type safetyFixture struct {
	Name       string         `json:"name"`
	Tool       string         `json:"tool"`
	Arguments  map[string]any `json:"arguments"`
	WantStatus string         `json:"want_status"`
}

type memoryFixture struct {
	Name            string `json:"name"`
	Category        string `json:"category"`
	Content         string `json:"content"`
	Expired         bool   `json:"expired"`
	WantInBootstrap bool   `json:"want_in_bootstrap"`
}

func TestSafetyFixtures(t *testing.T) {
	data, err := os.ReadFile("fixtures/safety.json")
	if err != nil {
		t.Fatalf("read safety fixtures: %v", err)
	}
	var fixtures []safetyFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse safety fixtures: %v", err)
	}

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			baseDir := t.TempDir()
			h := hands.New(baseDir, nil, config.ToolPolicyConfig{
				FileWriteEnabled: true,
				ShellEnabled:     true,
				ShellAllowlist:   []string{"go test"},
				HTTPEnabled:      true,
				ApprovalRequired: []string{"write_workspace_file"},
			})
			h.SetStore("main", store.NewFSStore(baseDir))

			execCtx := mcRuntime.NewExecution("main", "eval", "eval")
			result, _ := h.ExecuteStructured(mcRuntime.WithExecutionContext(context.Background(), execCtx), fixture.Tool, fixture.Arguments)
			if string(result.Status) != fixture.WantStatus {
				t.Fatalf("expected status %s, got %s: %+v", fixture.WantStatus, result.Status, result)
			}
		})
	}
}

func TestMemoryFixtures(t *testing.T) {
	data, err := os.ReadFile("fixtures/memory.json")
	if err != nil {
		t.Fatalf("read memory fixtures: %v", err)
	}
	var fixtures []memoryFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse memory fixtures: %v", err)
	}

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			baseDir := t.TempDir()
			st := store.NewFSStore(baseDir)
			mem := memory.New(t.TempDir(), 20000)
			mem.SetStore("main", st)
			expiresAt := time.Now().UTC().Add(24 * time.Hour)
			if fixture.Expired {
				expiresAt = time.Now().UTC().Add(-time.Hour)
			}
			if err := st.SaveMemoryRecord("main", store.MemoryRecord{
				AgentID:    "main",
				Category:   fixture.Category,
				Content:    fixture.Content,
				Source:     "eval",
				Confidence: 0.75,
				ExpiresAt:  expiresAt,
				RecordedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("save memory: %v", err)
			}

			bootstrap := mem.AssembleBootstrap(false)
			got := strings.Contains(bootstrap, fixture.Content)
			if got != fixture.WantInBootstrap {
				t.Fatalf("expected content presence %v, got %v in bootstrap %q", fixture.WantInBootstrap, got, bootstrap)
			}
		})
	}
}
