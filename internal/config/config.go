// Package config manages YAML configuration loading from ~/.nanoclaw/config.yaml.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	DefaultConfigDir = filepath.Join(os.Getenv("HOME"), ".nanoclaw")
	DefaultWorkspace = "workspace"
)

var ProviderDefaults = map[string]map[string]string{
	"anthropic": {
		"env_var":  "ANTHROPIC_API_KEY",
		"base_url": "",
		"model":    "claude-sonnet-4-20250514",
	},
	"openai": {
		"env_var":  "OPENAI_API_KEY",
		"base_url": "",
		"model":    "gpt-4o",
	},
}

type BrainConfig struct {
	Provider    string  `yaml:"provider"`
	Model       string  `yaml:"model"`
	APIKey      string  `yaml:"api_key"`
	BaseURL     string  `yaml:"base_url"`
	MaxTokens   int     `yaml:"max_tokens"`
	Temperature float64 `yaml:"temperature"`
}

func (b *BrainConfig) ResolveAPIKey() (string, error) {
	if b.APIKey != "" {
		return b.APIKey, nil
	}
	defaults, ok := ProviderDefaults[b.Provider]
	envVar := fmt.Sprintf("%s_API_KEY", b.Provider)
	if ok {
		if ev, exists := defaults["env_var"]; exists && ev != "" {
			envVar = ev
		}
	}
	key := os.Getenv(envVar)
	if key == "" {
		return "", fmt.Errorf("no API key: set %s or config brain.api_key", envVar)
	}
	return key, nil
}

func (b *BrainConfig) ResolveBaseURL() string {
	if b.BaseURL != "" {
		return b.BaseURL
	}
	if defaults, ok := ProviderDefaults[b.Provider]; ok {
		return defaults["base_url"]
	}
	return ""
}

type HeartbeatConfig struct {
	Enabled          bool   `yaml:"enabled"`
	IntervalMinutes  int    `yaml:"interval_minutes"`
	ActiveHoursStart string `yaml:"active_hours_start"`
	ActiveHoursEnd   string `yaml:"active_hours_end"`
}

type CronJobConfig struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"`
	Prompt   string `yaml:"prompt"`
	AgentID  string `yaml:"agent_id"`
}

type ToolPolicyConfig struct {
	FileWriteEnabled   bool     `yaml:"file_write_enabled"`
	FileWriteAllowlist []string `yaml:"file_write_allowlist"`
	ShellEnabled       bool     `yaml:"shell_enabled"`
	ShellAllowlist     []string `yaml:"shell_allowlist"`
	HTTPEnabled        bool     `yaml:"http_enabled"`
	HTTPAllowlist      []string `yaml:"http_allowlist"`
	ApprovalRequired   []string `yaml:"approval_required"`
	ApprovalTimeoutMin int      `yaml:"approval_timeout_minutes"`
}

type AgentDef struct {
	ID             string           `yaml:"-"`
	Workspace      string           `yaml:"workspace"`
	Brain          BrainConfig      `yaml:"brain"`
	Heartbeat      HeartbeatConfig  `yaml:"heartbeat"`
	CronJobs       []CronJobConfig  `yaml:"cron"`
	AllowedTools   []string         `yaml:"allowed_tools"`
	ToolPolicies   ToolPolicyConfig `yaml:"tool_policies"`
	SubagentsAllow []string         `yaml:"subagents_allow"`
	MaxSpawnDepth  int              `yaml:"max_spawn_depth"`
	MaxSubagents   int              `yaml:"max_subagents_per_task"`
}

type Config struct {
	ConfigDir          string               `yaml:"-"`
	ConfigVersion      string               `yaml:"config_version"`
	Workspace          string               `yaml:"workspace"`
	APIKeys            []string             `yaml:"api_keys"`
	RateLimitPerMin    int                  `yaml:"rate_limit_per_minute"`
	Agents             map[string]*AgentDef `yaml:"agents"`
	MaxContextChars    int                  `yaml:"max_context_chars"`
	BootstrapMaxChars  int                  `yaml:"bootstrap_max_chars"`
	MaxWallClockSec    int                  `yaml:"max_wall_clock_seconds"`
	MaxToolRounds      int                  `yaml:"max_tool_rounds"`
	MaxToolCalls       int                  `yaml:"max_tool_calls"`
	MaxToolOutputBytes int                  `yaml:"max_tool_output_bytes"`
}

func NewConfig() *Config {
	return &Config{
		ConfigDir:          DefaultConfigDir,
		Workspace:          DefaultWorkspace,
		Agents:             map[string]*AgentDef{"main": newDefaultAgentDef("main")},
		MaxContextChars:    100000,
		BootstrapMaxChars:  20000,
		MaxWallClockSec:    120,
		MaxToolRounds:      10,
		MaxToolCalls:       32,
		MaxToolOutputBytes: 20000,
	}
}

func newDefaultAgentDef(id string) *AgentDef {
	return &AgentDef{
		ID: id,
		Brain: BrainConfig{
			Provider:    "anthropic",
			Model:       "claude-sonnet-4-20250514",
			MaxTokens:   4096,
			Temperature: 0.7,
		},
		Heartbeat: HeartbeatConfig{
			Enabled:          true,
			IntervalMinutes:  30,
			ActiveHoursStart: "08:00",
			ActiveHoursEnd:   "23:00",
		},
		AllowedTools: []string{"*"},
		ToolPolicies: ToolPolicyConfig{
			FileWriteEnabled:   true,
			FileWriteAllowlist: nil,
			ShellEnabled:       true,
			HTTPEnabled:        true,
		},
		SubagentsAllow: []string{"*"},
		MaxSpawnDepth:  1,
		MaxSubagents:   4,
	}
}

func Load(configPath string) (*Config, error) {
	if configPath == "" {
		configPath = filepath.Join(DefaultConfigDir, "config.yaml")
	}
	cfg := NewConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.apply(&raw)
	return cfg, nil
}

type rawConfig struct {
	ConfigVersion      string                    `yaml:"config_version"`
	Workspace          string                    `yaml:"workspace"`
	APIKeys            []string                  `yaml:"api_keys"`
	RateLimitPerMin    int                       `yaml:"rate_limit_per_minute"`
	MaxContextChars    int                       `yaml:"max_context_chars"`
	BootstrapMaxChars  int                       `yaml:"bootstrap_max_chars"`
	MaxWallClockSec    int                       `yaml:"max_wall_clock_seconds"`
	MaxToolRounds      int                       `yaml:"max_tool_rounds"`
	MaxToolCalls       int                       `yaml:"max_tool_calls"`
	MaxToolOutputBytes int                       `yaml:"max_tool_output_bytes"`
	Agents             map[string]map[string]any `yaml:"agents"`
}

func (cfg *Config) apply(raw *rawConfig) {
	if raw.ConfigVersion != "" {
		cfg.ConfigVersion = raw.ConfigVersion
	}
	if raw.Workspace != "" {
		cfg.Workspace = ExpandHome(raw.Workspace)
	}
	if len(raw.APIKeys) > 0 {
		cfg.APIKeys = raw.APIKeys
	}
	if raw.RateLimitPerMin > 0 {
		cfg.RateLimitPerMin = raw.RateLimitPerMin
	}
	if raw.MaxContextChars > 0 {
		cfg.MaxContextChars = raw.MaxContextChars
	}
	if raw.BootstrapMaxChars > 0 {
		cfg.BootstrapMaxChars = raw.BootstrapMaxChars
	}
	if raw.MaxWallClockSec > 0 {
		cfg.MaxWallClockSec = raw.MaxWallClockSec
	}
	if raw.MaxToolRounds > 0 {
		cfg.MaxToolRounds = raw.MaxToolRounds
	}
	if raw.MaxToolCalls > 0 {
		cfg.MaxToolCalls = raw.MaxToolCalls
	}
	if raw.MaxToolOutputBytes > 0 {
		cfg.MaxToolOutputBytes = raw.MaxToolOutputBytes
	}

	for agentID, agentRaw := range raw.Agents {
		def := newDefaultAgentDef(agentID)

		if ws, ok := agentRaw["workspace"].(string); ok {
			def.Workspace = ws
		}

		if brainRaw, ok := agentRaw["brain"].(map[string]any); ok {
			provider := getStr(brainRaw, "provider", "anthropic")
			defaults := ProviderDefaults[provider]
			defaultModel := "claude-sonnet-4-20250514"
			if defaults != nil {
				if m, ok := defaults["model"]; ok {
					defaultModel = m
				}
			}
			def.Brain = BrainConfig{
				Provider:    provider,
				Model:       getStr(brainRaw, "model", defaultModel),
				APIKey:      getStr(brainRaw, "api_key", ""),
				BaseURL:     getStr(brainRaw, "base_url", ""),
				MaxTokens:   getInt(brainRaw, "max_tokens", 4096),
				Temperature: getFloat(brainRaw, "temperature", 0.7),
			}
		}

		if hbRaw, ok := agentRaw["heartbeat"].(map[string]any); ok {
			def.Heartbeat = HeartbeatConfig{
				Enabled:          getBool(hbRaw, "enabled", true),
				IntervalMinutes:  getInt(hbRaw, "interval_minutes", 30),
				ActiveHoursStart: getStr(hbRaw, "active_hours_start", "08:00"),
				ActiveHoursEnd:   getStr(hbRaw, "active_hours_end", "23:00"),
			}
		}

		if cronRaw, ok := agentRaw["cron"].([]any); ok {
			for _, item := range cronRaw {
				if cj, ok := item.(map[string]any); ok {
					def.CronJobs = append(def.CronJobs, CronJobConfig{
						Name:     getStr(cj, "name", ""),
						Schedule: getStr(cj, "schedule", ""),
						Prompt:   getStr(cj, "prompt", ""),
						AgentID:  agentID,
					})
				}
			}
		}

		if at, ok := agentRaw["allowed_tools"].([]any); ok {
			def.AllowedTools = toStringSlice(at)
		}
		if tpRaw, ok := agentRaw["tool_policies"].(map[string]any); ok {
			def.ToolPolicies = ToolPolicyConfig{
				FileWriteEnabled:   getBool(tpRaw, "file_write_enabled", true),
				FileWriteAllowlist: toStringSliceValue(tpRaw["file_write_allowlist"]),
				ShellEnabled:       getBool(tpRaw, "shell_enabled", true),
				ShellAllowlist:     toStringSliceValue(tpRaw["shell_allowlist"]),
				HTTPEnabled:        getBool(tpRaw, "http_enabled", true),
				HTTPAllowlist:      toStringSliceValue(tpRaw["http_allowlist"]),
				ApprovalRequired:   toStringSliceValue(tpRaw["approval_required"]),
				ApprovalTimeoutMin: getInt(tpRaw, "approval_timeout_minutes", 0),
			}
		}
		if sa, ok := agentRaw["subagents_allow"].([]any); ok {
			def.SubagentsAllow = toStringSlice(sa)
		}
		if msd, ok := agentRaw["max_spawn_depth"]; ok {
			if v, ok := msd.(int); ok {
				def.MaxSpawnDepth = v
			}
		}
		if msa, ok := agentRaw["max_subagents_per_task"]; ok {
			if v, ok := msa.(int); ok {
				def.MaxSubagents = v
			}
		}

		cfg.Agents[agentID] = def
	}
}

func (cfg *Config) GetAgent(agentID string) (*AgentDef, error) {
	def, ok := cfg.Agents[agentID]
	if !ok {
		return nil, fmt.Errorf("agent '%s' not defined in config", agentID)
	}
	return def, nil
}

func (cfg *Config) AgentWorkspace(agentID string) (string, error) {
	def, err := cfg.GetAgent(agentID)
	if err != nil {
		return "", err
	}
	if def.Workspace != "" {
		return ExpandHome(def.Workspace), nil
	}
	if agentID == "main" {
		return ExpandHome(cfg.Workspace), nil
	}
	parent := filepath.Dir(ExpandHome(cfg.Workspace))
	return filepath.Join(parent, "workspace-"+agentID), nil
}

func (cfg *Config) SaveDefault() (string, error) {
	if err := os.MkdirAll(cfg.ConfigDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(cfg.ConfigDir, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	defaultCfg := map[string]any{
		"config_version":         cfg.ConfigVersion,
		"workspace":              cfg.Workspace,
		"api_keys":               cfg.APIKeys,
		"rate_limit_per_minute":  cfg.RateLimitPerMin,
		"max_wall_clock_seconds": cfg.MaxWallClockSec,
		"max_tool_rounds":        cfg.MaxToolRounds,
		"max_tool_calls":         cfg.MaxToolCalls,
		"max_tool_output_bytes":  cfg.MaxToolOutputBytes,
		"agents": map[string]any{
			"main": map[string]any{
				"brain": map[string]any{
					"provider": "anthropic",
					"model":    "claude-sonnet-4-20250514",
				},
				"tool_policies": map[string]any{
					"file_write_enabled":       true,
					"file_write_allowlist":     []string{"."},
					"shell_enabled":            true,
					"http_enabled":             true,
					"approval_required":        []string{},
					"approval_timeout_minutes": 0,
				},
			},
		},
	}
	data, err := yaml.Marshal(defaultCfg)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func (cfg *Config) Fingerprint() string {
	if cfg == nil {
		return ""
	}
	snapshot := cfg.redactedSnapshot()
	data, err := yaml.Marshal(snapshot)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

func (cfg *Config) redactedSnapshot() map[string]any {
	agents := make(map[string]any, len(cfg.Agents))
	for id, def := range cfg.Agents {
		agents[id] = map[string]any{
			"workspace":              def.Workspace,
			"brain":                  redactedBrain(def.Brain),
			"heartbeat":              def.Heartbeat,
			"cron":                   def.CronJobs,
			"allowed_tools":          def.AllowedTools,
			"tool_policies":          def.ToolPolicies,
			"subagents_allow":        def.SubagentsAllow,
			"max_spawn_depth":        def.MaxSpawnDepth,
			"max_subagents_per_task": def.MaxSubagents,
		}
	}
	return map[string]any{
		"config_version":         cfg.ConfigVersion,
		"workspace":              cfg.Workspace,
		"api_keys_configured":    len(cfg.APIKeys),
		"rate_limit_per_minute":  cfg.RateLimitPerMin,
		"max_context_chars":      cfg.MaxContextChars,
		"bootstrap_max_chars":    cfg.BootstrapMaxChars,
		"max_wall_clock_seconds": cfg.MaxWallClockSec,
		"max_tool_rounds":        cfg.MaxToolRounds,
		"max_tool_calls":         cfg.MaxToolCalls,
		"max_tool_output_bytes":  cfg.MaxToolOutputBytes,
		"agents":                 agents,
	}
}

func redactedBrain(brain BrainConfig) map[string]any {
	return map[string]any{
		"provider":    brain.Provider,
		"model":       brain.Model,
		"api_key_set": brain.APIKey != "",
		"base_url":    brain.BaseURL,
		"max_tokens":  brain.MaxTokens,
		"temperature": brain.Temperature,
	}
}

// Helpers

// ExpandHome expands ~ to the user's home directory.
func ExpandHome(path string) string {
	if len(path) > 1 && path[0] == '~' && path[1] == '/' {
		return filepath.Join(os.Getenv("HOME"), path[2:])
	}
	return path
}

func getStr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func getInt(m map[string]any, key string, def int) int {
	if v, ok := m[key].(int); ok {
		return v
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return def
}

func getFloat(m map[string]any, key string, def float64) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	if v, ok := m[key].(int); ok {
		return float64(v)
	}
	return def
}

func getBool(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func toStringSlice(items []any) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func toStringSliceValue(v any) []string {
	switch items := v.(type) {
	case []any:
		return toStringSlice(items)
	case []string:
		return items
	default:
		return nil
	}
}

func (p ToolPolicyConfig) ShellAllowed(command string) bool {
	if !p.ShellEnabled {
		return false
	}
	if len(p.ShellAllowlist) == 0 {
		return true
	}
	command = strings.TrimSpace(command)
	if hasShellControlOperator(command) {
		return false
	}
	for _, allowed := range p.ShellAllowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" && shellCommandMatchesAllowed(command, allowed) {
			return true
		}
	}
	return false
}

func shellCommandMatchesAllowed(command, allowed string) bool {
	if command == allowed {
		return true
	}
	return strings.HasPrefix(command, allowed+" ")
}

func hasShellControlOperator(command string) bool {
	if strings.ContainsAny(command, "\n\r|&;<>`") {
		return true
	}
	return strings.Contains(command, "$(")
}

func (p ToolPolicyConfig) FileWritePathAllowed(relPath string) bool {
	if !p.FileWriteEnabled {
		return false
	}
	if len(p.FileWriteAllowlist) == 0 {
		return true
	}
	relPath = filepath.Clean(strings.TrimSpace(relPath))
	for _, allowed := range p.FileWriteAllowlist {
		allowed = filepath.Clean(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if allowed == "." || relPath == allowed || strings.HasPrefix(relPath, allowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (cfg *Config) IsAPIKeyAllowed(key string) bool {
	if len(cfg.APIKeys) == 0 {
		return true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, allowed := range cfg.APIKeys {
		resolved := resolveSecretRef(strings.TrimSpace(allowed))
		if resolved == "" {
			continue
		}
		if key == resolved {
			return true
		}
	}
	return false
}

func resolveSecretRef(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "env:") {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(value, "env:"))))
	}
	return value
}
