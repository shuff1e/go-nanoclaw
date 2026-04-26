// Package router dispatches incoming messages to appropriate skill and agent handlers.
package router

import (
	"fmt"
	"log/slog"
	"strings"

	mclog "go-nanoclaw/internal/log"
	"go-nanoclaw/internal/skills"
)

// RouteResult contains the result of routing a message.
type RouteResult struct {
	MatchedSkills     []skills.Skill
	ExtraSystemPrompt string
	ExtraTools        []string
	TargetAgent       string
}

// Router routes messages to skills and agents.
type Router struct {
	Skills        *skills.Registry
	agentBindings map[string][]string // pattern → agent_ids
}

// NewRouter creates a new Router.
func NewRouter(skillRegistry *skills.Registry) *Router {
	return &Router{
		Skills:        skillRegistry,
		agentBindings: make(map[string][]string),
	}
}

// AddAgentBinding binds message patterns to a specific agent.
func (r *Router) AddAgentBinding(agentID string, patterns []string) {
	for _, pattern := range patterns {
		r.agentBindings[pattern] = append(r.agentBindings[pattern], agentID)
	}
}

// Route processes a message and returns matching skills and target agent.
func (r *Router) Route(text, sourceAgent string) RouteResult {
	if mclog.Verbosity() >= 2 {
		mclog.SubStep("Router.Route", "source_agent", sourceAgent, "input_len", len(text))
	}
	matched := r.Skills.Match(text)

	var extraPrompts []string
	extraToolsSet := make(map[string]bool)

	for _, skill := range matched {
		if skill.Prompt != "" {
			extraPrompts = append(extraPrompts,
				fmt.Sprintf("### Skill: %s\n\n%s", skill.Name, skill.Prompt))
		}
		for _, tool := range skill.Tools {
			extraToolsSet[tool] = true
		}
	}

	var extraTools []string
	for tool := range extraToolsSet {
		extraTools = append(extraTools, tool)
	}

	target := r.resolveAgent(text, sourceAgent)

	if len(matched) > 0 {
		names := make([]string, len(matched))
		for i, s := range matched {
			names[i] = s.Name
		}
		slog.Info("Matched skills",
			"count", len(matched),
			"skills", names,
			"agent", target,
		)
	}

	return RouteResult{
		MatchedSkills:     matched,
		ExtraSystemPrompt: strings.Join(extraPrompts, "\n\n---\n\n"),
		ExtraTools:        extraTools,
		TargetAgent:       target,
	}
}

func (r *Router) resolveAgent(text, source string) string {
	for pattern, agents := range r.agentBindings {
		if strings.Contains(strings.ToLower(text), strings.ToLower(pattern)) {
			return agents[0]
		}
	}
	return source
}
