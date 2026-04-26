package hands

import "go-nanoclaw/internal/brain"

// BuiltinToolSchemas defines the built-in tool schemas.
var BuiltinToolSchemas = []brain.ToolSchema{
	{
		Name:        "run_command",
		Description: "Run an approved command from the workspace and report its output, errors, and exit status.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Command line to run under the workspace policy"},
				"timeout": map[string]any{"type": "integer", "description": "Maximum runtime in seconds; defaults to 30", "default": 30},
			},
			"required": []string{"command"},
		},
	},
	{
		Name:        "read_workspace_file",
		Description: "Load text from a workspace file after path-safety checks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Workspace-relative file path to load"},
			},
			"required": []string{"path"},
		},
	},
	{
		Name:        "write_workspace_file",
		Description: "Store text in an allowed workspace path, creating folders when necessary.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Workspace-relative destination path"},
				"content": map[string]any{"type": "string", "description": "Text payload to save"},
			},
			"required": []string{"path", "content"},
		},
	},
	{
		Name:        "list_workspace",
		Description: "Inspect the immediate entries under a workspace directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Workspace-relative directory; uses the workspace root when omitted", "default": "."},
			},
		},
	},
	{
		Name:        "fetch_url",
		Description: "Fetch an allowed HTTP(S) URL and return the bounded response body.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "HTTP or HTTPS URL permitted by policy"},
			},
			"required": []string{"url"},
		},
	},
	{
		Name:        "remember_note",
		Description: "Record durable context for future turns in markdown and structured memory storage.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entry":    map[string]any{"type": "string", "description": "Information to preserve"},
				"category": map[string]any{"type": "string", "description": "Optional bucket: profile, facts, preferences, or notes"},
				"confidence": map[string]any{
					"type":        "number",
					"description": "Optional certainty value between 0.0 and 1.0",
				},
				"ttl_days": map[string]any{
					"type":        "integer",
					"description": "Optional lifetime in days before the record is ignored",
				},
			},
			"required": []string{"entry"},
		},
	},
	{
		Name:        "read_note",
		Description: "Open a memory or workspace note by filename.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{"type": "string", "description": "Workspace-relative note path such as MEMORY.md or skills/example.md"},
			},
			"required": []string{"filename"},
		},
	},
}
