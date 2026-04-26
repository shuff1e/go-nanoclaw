package agent

import (
	"sync"

	"go-nanoclaw/internal/config"
)

// Orchestrator manages multiple agents and their lifecycles.
type Orchestrator struct {
	mu     sync.RWMutex
	Config *config.Config
	agents map[string]*Agent
}

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(cfg *config.Config) *Orchestrator {
	return &Orchestrator{
		Config: cfg,
		agents: make(map[string]*Agent),
	}
}

// GetOrCreateAgent returns an existing agent or creates a new one.
func (o *Orchestrator) GetOrCreateAgent(agentID string) (*Agent, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if a, ok := o.agents[agentID]; ok {
		return a, nil
	}

	agentDef, err := o.Config.GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	workspace, err := o.Config.AgentWorkspace(agentID)
	if err != nil {
		return nil, err
	}

	a, err := NewAgent(agentDef, workspace, o.Config, 0)
	if err != nil {
		return nil, err
	}

	o.agents[agentID] = a
	return a, nil
}

// ListAgents returns the IDs of all configured agents.
func (o *Orchestrator) ListAgents() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()

	result := make([]string, 0, len(o.Config.Agents))
	for id := range o.Config.Agents {
		result = append(result, id)
	}
	return result
}
