// Package testutil provides shared test helpers for go-nanoclaw.
package testutil

import (
	"context"

	"go-nanoclaw/internal/brain"
)

// BrainFunc is an adapter that allows the use of ordinary functions as Brain.
type BrainFunc func(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error)

func (f BrainFunc) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	return f(ctx, messages, systemPrompt, tools)
}

// FakeBrain is a simple Brain implementation for testing that returns a fixed response.
type FakeBrain struct {
	Response *brain.BrainResponse
	Err      error
}

func (f *FakeBrain) Think(ctx context.Context, messages []brain.Message, systemPrompt string, tools []brain.ToolSchema) (*brain.BrainResponse, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Response, nil
}
