package hands

import (
	"context"

	"go-nanoclaw/internal/brain"
)

// ToolFunc is the function signature for tool handlers.
type ToolFunc func(ctx context.Context, args map[string]any) (string, error)

// customTool holds a registered custom tool.
type customTool struct {
	handler ToolFunc
	schema  brain.ToolSchema
}
