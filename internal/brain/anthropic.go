package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go-nanoclaw/internal/config"
	mclog "go-nanoclaw/internal/log"
)

// AnthropicBrain implements Brain using the Anthropic Messages API.
type AnthropicBrain struct {
	cfg    config.BrainConfig
	client *http.Client
}

// NewAnthropicBrain creates a new AnthropicBrain.
func NewAnthropicBrain(cfg config.BrainConfig) *AnthropicBrain {
	return &AnthropicBrain{
		cfg:    cfg,
		client: &http.Client{},
	}
}

func (b *AnthropicBrain) Think(ctx context.Context, messages []Message, systemPrompt string, tools []ToolSchema) (*BrainResponse, error) {
	if mclog.Verbosity() >= 2 {
		mclog.Step("AnthropicBrain.Think",
			"model", b.cfg.Model,
			"messages", len(messages),
			"tools", len(tools),
			"system_prompt_len", len(systemPrompt),
		)
	}

	apiKey, err := b.cfg.ResolveAPIKey()
	if err != nil {
		return nil, err
	}
	baseURL := b.cfg.ResolveBaseURL()
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	apiMessages := toAnthropicMessages(messages)
	body := map[string]any{
		"model":      b.cfg.Model,
		"max_tokens": b.cfg.MaxTokens,
		"system":     systemPrompt,
		"messages":   apiMessages,
	}
	if len(tools) > 0 {
		body["tools"] = toAnthropicTools(tools)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Pretty-print for trace
	prettyBody, _ := json.MarshalIndent(body, "", "  ")

	url := baseURL + "/v1/messages"
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}
	mclog.TraceHTTPRequest("POST", url, headers, string(prettyBody))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if mclog.Verbosity() >= 2 {
		mclog.SubStep("Sending request", "url", url)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	mclog.TraceHTTPResponse(resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var textParts []string
	var toolCalls []ToolCall
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}

	brainResp := &BrainResponse{
		Text:       joinStrings(textParts, "\n"),
		ToolCalls:  toolCalls,
		StopReason: result.StopReason,
		Usage: Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		},
	}

	if mclog.Verbosity() >= 2 {
		mclog.Result("AnthropicBrain.Think complete",
			"stop_reason", brainResp.StopReason,
			"tool_calls", len(brainResp.ToolCalls),
			"response_len", len(brainResp.Text),
			"tokens", fmt.Sprintf("%d->%d", brainResp.Usage.InputTokens, brainResp.Usage.OutputTokens),
		)
	}

	return brainResp, nil
}

// Anthropic API types

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

func toAnthropicMessages(messages []Message) []map[string]any {
	var result []map[string]any
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			result = append(result, map[string]any{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			var content []map[string]any
			if msg.Content != "" {
				content = append(content, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": tc.Arguments,
				})
			}
			result = append(result, map[string]any{
				"role":    "assistant",
				"content": content,
			})
		case "tool_result":
			result = append(result, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolCallID,
						"content":     msg.Content,
					},
				},
			})
		}
	}
	return result
}

func toAnthropicTools(tools []ToolSchema) []map[string]any {
	result := make([]map[string]any, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result[i] = map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": params,
		}
	}
	return result
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}
