package brain

import (
	"context"
	"fmt"

	"go-nanoclaw/internal/config"
)

// Brain is the LLM provider interface.
type Brain interface {
	Think(ctx context.Context, messages []Message, systemPrompt string, tools []ToolSchema) (*BrainResponse, error)
}

// NewBrain creates a Brain implementation based on provider config.
func NewBrain(cfg config.BrainConfig) (Brain, error) {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropicBrain(cfg), nil
	case "openai":
		return NewOpenAIBrain(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}
