package brain

import (
	"context"
	"fmt"
)

// ScriptedBrain is a deterministic Brain for local demos and tests.
// It exercises the same agent loop and tool dispatch path as a real provider.
type ScriptedBrain struct {
	Task string
	Path string
}

func (b *ScriptedBrain) Think(ctx context.Context, messages []Message, systemPrompt string, tools []ToolSchema) (*BrainResponse, error) {
	_ = ctx
	_ = systemPrompt
	_ = tools

	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool_result" {
			observation := messages[i].Content
			if len(observation) > 1200 {
				observation = observation[:1200] + "\n\n[...observation truncated for demo output...]"
			}
			return &BrainResponse{
				Text:       fmt.Sprintf("Final Result:\nI inspected `%s` through the Tool Registry and completed the task.\n\nObservation:\n%s\n\nSummary:\n- Thinking decided that reading the workspace file was the smallest useful action.\n- Acting used `read_workspace_file` through the registry instead of hard-coding file access.\n- The result above was fed back into the loop before producing this answer.", b.Path, observation),
				StopReason: "end_turn",
			}, nil
		}
	}

	task := b.Task
	if task == "" {
		task = "read a workspace file and summarize it"
	}
	path := b.Path
	if path == "" {
		path = "README.md"
	}

	return &BrainResponse{
		Text: fmt.Sprintf("Thinking:\n- Goal: %s\n- Need one reliable observation from the workspace before answering.\n- The minimum useful action is to read `%s`.\n\nActing:\n- Call `read_workspace_file` via the registered tools.", task, path),
		ToolCalls: []ToolCall{
			{
				ID:   "demo-read-workspace-file-1",
				Name: "read_workspace_file",
				Arguments: map[string]any{
					"path": path,
				},
			},
		},
		StopReason: "tool_use",
	}, nil
}
