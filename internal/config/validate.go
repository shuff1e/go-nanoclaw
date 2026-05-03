package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Validate checks the configuration for common errors and returns all issues found.
func (cfg *Config) Validate() []string {
	var issues []string

	if cfg.MaxContextChars <= 0 {
		issues = append(issues, "max_context_chars must be positive")
	}
	if cfg.MaxToolRounds <= 0 {
		issues = append(issues, "max_tool_rounds must be positive")
	}
	if cfg.MaxToolCalls <= 0 {
		issues = append(issues, "max_tool_calls must be positive")
	}
	if cfg.MaxWallClockSec <= 0 {
		issues = append(issues, "max_wall_clock_seconds must be positive")
	}

	if len(cfg.Agents) == 0 {
		issues = append(issues, "at least one agent must be defined")
	}

	for id, def := range cfg.Agents {
		prefix := fmt.Sprintf("agent[%q]", id)
		issues = append(issues, validateAgentDef(prefix, def)...)
	}

	return issues
}

func validateAgentDef(prefix string, def *AgentDef) []string {
	var issues []string

	if def.Brain.Provider == "" {
		issues = append(issues, prefix+".brain.provider is required")
	} else {
		valid := map[string]bool{"anthropic": true, "openai": true}
		if !valid[def.Brain.Provider] {
			issues = append(issues, prefix+".brain.provider must be 'anthropic' or 'openai'")
		}
	}

	if def.Brain.Model == "" {
		issues = append(issues, prefix+".brain.model is required")
	}
	if def.Brain.MaxTokens <= 0 {
		issues = append(issues, prefix+".brain.max_tokens must be positive")
	}
	if def.Brain.Temperature < 0 || def.Brain.Temperature > 2 {
		issues = append(issues, prefix+".brain.temperature must be between 0 and 2")
	}

	if def.Heartbeat.Enabled {
		if def.Heartbeat.IntervalMinutes <= 0 {
			issues = append(issues, prefix+".heartbeat.interval_minutes must be positive when enabled")
		}
		if !isValidTimeHHMM(def.Heartbeat.ActiveHoursStart) {
			issues = append(issues, prefix+".heartbeat.active_hours_start must be HH:MM format")
		}
		if !isValidTimeHHMM(def.Heartbeat.ActiveHoursEnd) {
			issues = append(issues, prefix+".heartbeat.active_hours_end must be HH:MM format")
		}
	}

	if def.MaxSpawnDepth < 0 {
		issues = append(issues, prefix+".max_spawn_depth must be non-negative")
	}
	if def.MaxSubagents < 0 {
		issues = append(issues, prefix+".max_subagents_per_task must be non-negative")
	}

	for i, cj := range def.CronJobs {
		cjPrefix := fmt.Sprintf("%s.cron[%d]", prefix, i)
		if cj.Name == "" {
			issues = append(issues, cjPrefix+".name is required")
		}
		if cj.Schedule == "" {
			issues = append(issues, cjPrefix+".schedule is required")
		}
		if cj.Prompt == "" {
			issues = append(issues, cjPrefix+".prompt is required")
		}
	}

	return issues
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func isValidTimeHHMM(s string) bool {
	return hhmmRe.MatchString(s)
}

// ValidateOrDie validates the config and exits if there are issues.
// Call this after Load() to fail-fast on bad configuration.
func (cfg *Config) ValidateOrDie() {
	issues := cfg.Validate()
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Configuration errors:\n")
	for _, issue := range issues {
		fmt.Fprintf(os.Stderr, "  - %s\n", issue)
	}
	os.Exit(1)
}

// APIKeyWarning returns a warning if no API key is resolvable for any agent.
func (cfg *Config) APIKeyWarning() string {
	var missing []string
	for id, def := range cfg.Agents {
		if _, err := def.Brain.ResolveAPIKey(); err != nil {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("no API key found for agents: %s", strings.Join(missing, ", "))
}
