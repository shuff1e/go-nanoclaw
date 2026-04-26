package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"go-nanoclaw/internal/config"
	mclog "go-nanoclaw/internal/log"
)

// OpenAIBrain implements Brain using the OpenAI Chat Completions API.
// Also works with OpenAI-compatible APIs when base_url is configured.
type OpenAIBrain struct {
	cfg    config.BrainConfig
	client *http.Client
}

// NewOpenAIBrain creates a new OpenAIBrain.
func NewOpenAIBrain(cfg config.BrainConfig) *OpenAIBrain {
	return &OpenAIBrain{
		cfg:    cfg,
		client: &http.Client{},
	}
}

func (b *OpenAIBrain) Think(ctx context.Context, messages []Message, systemPrompt string, tools []ToolSchema) (*BrainResponse, error) {
	if mclog.Verbosity() >= 2 {
		mclog.Step("OpenAIBrain.Think",
			"provider", b.cfg.Provider,
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
		baseURL = "https://api.openai.com/v1"
	}

	apiMessages := []map[string]any{
		{"role": "system", "content": systemPrompt},
	}
	apiMessages = append(apiMessages, toOpenAIMessages(messages)...)

	body := map[string]any{
		"model":       b.cfg.Model,
		"max_tokens":  b.cfg.MaxTokens,
		"temperature": b.cfg.Temperature,
		"messages":    apiMessages,
	}
	if len(tools) > 0 {
		body["tools"] = toOpenAITools(tools)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Pretty-print for trace
	prettyBody, _ := json.MarshalIndent(body, "", "  ")

	url := baseURL + "/chat/completions"
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
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
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	mclog.TraceHTTPResponse(resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openai API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := result.Choices[0]
	var toolCalls []ToolCall
	for _, tc := range choice.Message.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			slog.Warn("Malformed tool arguments from LLM", "raw", tc.Function.Arguments)
			args = map[string]any{}
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	stopReason := "end_turn"
	if len(toolCalls) > 0 {
		stopReason = "tool_use"
	}

	var usage Usage
	if result.Usage != nil {
		usage = Usage{
			InputTokens:  result.Usage.PromptTokens,
			OutputTokens: result.Usage.CompletionTokens,
		}
	}

	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}

	brainResp := &BrainResponse{
		Text:       content,
		ToolCalls:  toolCalls,
		StopReason: stopReason,
		Usage:      usage,
	}

	if mclog.Verbosity() >= 2 {
		mclog.Result("OpenAIBrain.Think complete",
			"stop_reason", brainResp.StopReason,
			"tool_calls", len(brainResp.ToolCalls),
			"response_len", len(brainResp.Text),
			"tokens", fmt.Sprintf("%d->%d", brainResp.Usage.InputTokens, brainResp.Usage.OutputTokens),
		)
	}

	return brainResp, nil
}

// OpenAI API types

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIMessage struct {
	Content   *string          `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func toOpenAIMessages(messages []Message) []map[string]any {
	var result []map[string]any
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			result = append(result, map[string]any{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			entry := map[string]any{
				"role":    "assistant",
				"content": msg.Content,
			}
			if len(msg.ToolCalls) > 0 {
				var tcs []map[string]any
				for _, tc := range msg.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Arguments)
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": string(argsJSON),
						},
					})
				}
				entry["tool_calls"] = tcs
			}
			result = append(result, entry)
		case "tool_result":
			result = append(result, map[string]any{
				"role":         "tool",
				"tool_call_id": msg.ToolCallID,
				"content":      msg.Content,
			})
		}
	}
	return result
}

func toOpenAITools(tools []ToolSchema) []map[string]any {
	result := make([]map[string]any, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		}
	}
	return result
}
