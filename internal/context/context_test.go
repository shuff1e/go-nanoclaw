package context

import (
	"strings"
	"testing"

	"go-nanoclaw/internal/brain"
)

func TestNeedsCompactionUsesReductionPolicy(t *testing.T) {
	cm := NewManager(1000)
	cm.AddMessage(brain.Message{Role: "user", Content: strings.Repeat("a", 610)})
	if cm.NeedsCompaction() {
		t.Fatal("610 chars should stay below the default reduction threshold")
	}
	cm.AddMessage(brain.Message{Role: "assistant", Content: strings.Repeat("b", 20)})
	if !cm.NeedsCompaction() {
		t.Fatal("630 chars should reach the default reduction threshold")
	}
}

func TestCompactUsesPolicyBoundaryAndKeepsRecentState(t *testing.T) {
	cm := NewManager(1200)
	cm.reductionPolicy.MinRecentChars = 220
	cm.reductionPolicy.MaxMessageSnippetChars = 80
	cm.AddMessage(brain.Message{Role: "user", Content: strings.Repeat("old-a", 90)})
	cm.AddMessage(brain.Message{Role: "assistant", Content: "first reply"})
	cm.AddMessage(brain.Message{Role: "assistant", Content: "tool requested", ToolCalls: []brain.ToolCall{{ID: "tc-1", Name: "read_note"}}})
	cm.AddMessage(brain.Message{Role: "tool_result", ToolCallID: "tc-1", Content: "tool output"})
	cm.AddMessage(brain.Message{Role: "assistant", Content: strings.Repeat("recent", 40)})
	cm.AddMessage(brain.Message{Role: "user", Content: "latest request"})

	summary, err := cm.Compact(func(messages []brain.Message, systemPrompt string) (*brain.BrainResponse, error) {
		if len(messages) != 1 || !strings.Contains(messages[0].Content, "carry-forward note") {
			t.Fatalf("unexpected reduction prompt: %+v", messages)
		}
		return &brain.BrainResponse{Text: "summary note", StopReason: "end_turn"}, nil
	}, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "summary note" {
		t.Fatalf("unexpected summary %q", summary)
	}
	if cm.compactedSummary != "summary note" {
		t.Fatalf("summary not recorded: %q", cm.compactedSummary)
	}
	if len(cm.history) == 0 || cm.history[0].Role == "tool_result" {
		t.Fatalf("recent history starts at an invalid boundary: %+v", cm.history)
	}
	if len(cm.history) >= 4 {
		t.Fatalf("expected policy-based reduction to keep fewer than a fixed four-message suffix, got %d", len(cm.history))
	}
}

func TestBuildDropsToolResultsWithoutVisibleRequest(t *testing.T) {
	cm := NewManager(1000)
	cm.AddMessage(brain.Message{Role: "tool_result", Content: "orphan"})
	cm.AddMessage(brain.Message{Role: "user", Content: "hello"})

	window := cm.Build("bootstrap", "", "")
	if len(window.Messages) != 1 {
		t.Fatalf("expected only user message after sanitizing, got %+v", window.Messages)
	}
	if window.Messages[0].Content != "hello" {
		t.Fatalf("unexpected message content: %+v", window.Messages)
	}
}
