package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-nanoclaw/internal/config"
)

func TestOpenAIBrainIncludesTemperature(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer server.Close()

	b := NewOpenAIBrain(config.BrainConfig{
		Provider:    "openai",
		Model:       "test-model",
		APIKey:      "test-key",
		BaseURL:     server.URL,
		MaxTokens:   123,
		Temperature: 0.42,
	})

	resp, err := b.Think(context.Background(), []Message{{Role: "user", Content: "hello"}}, "system", nil)
	if err != nil {
		t.Fatalf("think: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("unexpected response text: %q", resp.Text)
	}
	if got := requestBody["temperature"]; got != 0.42 {
		t.Fatalf("expected temperature 0.42, got %#v", got)
	}
	if got := requestBody["model"]; got != "test-model" {
		t.Fatalf("expected model test-model, got %#v", got)
	}
}
