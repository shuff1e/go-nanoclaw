// Package context manages the context window for each LLM call.
package context

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"go-nanoclaw/internal/brain"
	mclog "go-nanoclaw/internal/log"
)

const charsPerTokenEstimate = 4

type ReductionPolicy struct {
	TriggerRatio           float64
	MinRecentChars         int
	MaxSummarySourceChars  int
	MaxMessageSnippetChars int
}

func DefaultReductionPolicy() ReductionPolicy {
	return ReductionPolicy{
		TriggerRatio:           0.62,
		MinRecentChars:         3200,
		MaxSummarySourceChars:  24000,
		MaxMessageSnippetChars: 420,
	}
}

// Window holds the assembled context for an LLM call.
type Window struct {
	SystemPrompt string
	Messages     []brain.Message
	TotalChars   int
}

// EstimateTokens estimates the token count based on character count.
func (w *Window) EstimateTokens() int {
	total := len(w.SystemPrompt)
	for _, m := range w.Messages {
		total += len(m.Content)
	}
	w.TotalChars = total
	return total / charsPerTokenEstimate
}

// Manager assembles and manages the context window for each LLM call.
type Manager struct {
	mu               sync.Mutex
	maxContextChars  int
	history          []brain.Message
	compactedSummary string
	reductionPolicy  ReductionPolicy
}

// NewManager creates a new context Manager.
func NewManager(maxContextChars int) *Manager {
	if maxContextChars <= 0 {
		maxContextChars = 100000
	}
	return &Manager{
		maxContextChars: maxContextChars,
		reductionPolicy: DefaultReductionPolicy(),
	}
}

// History returns a copy of the current history.
func (cm *Manager) History() []brain.Message {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	result := make([]brain.Message, len(cm.history))
	copy(result, cm.history)
	return result
}

// HistoryLen returns the number of messages in the history.
func (cm *Manager) HistoryLen() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return len(cm.history)
}

// AddMessage adds a message to the history.
func (cm *Manager) AddMessage(msg brain.Message) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.history = append(cm.history, msg)
}

// Build assembles the context window.
func (cm *Manager) Build(bootstrapPrompt, skillPrompt, skillIndex string) Window {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if mclog.Verbosity() >= 2 {
		mclog.SubStep("ContextManager.Build",
			"history_len", len(cm.history),
			"has_compacted_summary", cm.compactedSummary != "",
			"bootstrap_len", len(bootstrapPrompt),
			"skill_prompt_len", len(skillPrompt),
		)
	}

	var systemParts []string
	systemParts = append(systemParts, bootstrapPrompt)
	if skillIndex != "" {
		systemParts = append(systemParts, skillIndex)
	}
	if skillPrompt != "" {
		systemParts = append(systemParts, skillPrompt)
	}
	systemPrompt := strings.Join(systemParts, "\n\n---\n\n")

	messages := cm.sanitizeMessages(cm.history)
	if cm.compactedSummary != "" {
		summaryMsg := brain.Message{
			Role:    "user",
			Content: fmt.Sprintf("[Prior workspace context]\n%s", cm.compactedSummary),
		}
		messages = append([]brain.Message{summaryMsg}, messages...)
	}

	window := Window{
		SystemPrompt: systemPrompt,
		Messages:     messages,
	}

	if window.EstimateTokens()*charsPerTokenEstimate > cm.maxContextChars {
		slog.Warn("Context reduction threshold reached",
			"tokens", window.EstimateTokens(),
			"chars", window.TotalChars,
			"max", cm.maxContextChars,
		)
	}

	return window
}

// NeedsCompaction checks if the context needs compaction.
func (cm *Manager) NeedsCompaction() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.historyCharsLocked() >= cm.reductionTriggerCharsLocked()
}

// Compact reduces older conversation state into a short handoff note.
type ThinkFunc func(messages []brain.Message, systemPrompt string) (*brain.BrainResponse, error)

func (cm *Manager) Compact(think ThinkFunc, bootstrapPrompt string) (string, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if len(cm.history) < 4 {
		return "Nothing to compact", nil
	}

	split := cm.selectReductionSplit()
	if split <= 0 {
		return "Nothing to compact", nil
	}
	oldMessages := cm.history[:split]
	recentMessages := make([]brain.Message, len(cm.history[split:]))
	copy(recentMessages, cm.history[split:])

	if len(oldMessages) == 0 {
		return "Nothing to compact", nil
	}

	var oldParts []string
	sourceChars := 0
	for _, m := range oldMessages {
		if m.Role == "user" || m.Role == "assistant" {
			content := m.Content
			if len(content) > cm.reductionPolicy.MaxMessageSnippetChars {
				content = content[:cm.reductionPolicy.MaxMessageSnippetChars]
			}
			next := fmt.Sprintf("[%s] %s", m.Role, content)
			if sourceChars+len(next) > cm.reductionPolicy.MaxSummarySourceChars {
				break
			}
			oldParts = append(oldParts, next)
			sourceChars += len(next)
		}
	}
	oldText := strings.Join(oldParts, "\n")

	summaryPrompt := fmt.Sprintf(
		"Create a concise carry-forward note for the older messages below. "+
			"Preserve durable facts, decisions, constraints, and unfinished work. "+
			"Do not include casual filler or tool transcripts unless they affect future behavior.\n\n%s", oldText,
	)

	response, err := think(
		[]brain.Message{{Role: "user", Content: summaryPrompt}},
		"You maintain compact operational notes for a workspace runtime.",
	)
	if err != nil {
		return "", err
	}

	cm.compactedSummary = response.Text
	cm.history = recentMessages

	slog.Info("Compacted messages",
		"old_count", len(oldMessages),
		"summary_chars", len(response.Text),
	)
	return response.Text, nil
}

func (cm *Manager) historyCharsLocked() int {
	total := 0
	for _, m := range cm.history {
		total += len(m.Content)
	}
	return total
}

func (cm *Manager) reductionTriggerCharsLocked() int {
	ratio := cm.reductionPolicy.TriggerRatio
	if ratio <= 0 || ratio >= 1 {
		ratio = DefaultReductionPolicy().TriggerRatio
	}
	return int(float64(cm.maxContextChars) * ratio)
}

func (cm *Manager) selectReductionSplit() int {
	if len(cm.history) < 3 {
		return 0
	}
	minRecentChars := cm.reductionPolicy.MinRecentChars
	if minRecentChars <= 0 {
		minRecentChars = DefaultReductionPolicy().MinRecentChars
	}

	recentChars := 0
	for i := len(cm.history) - 1; i > 0; i-- {
		recentChars += len(cm.history[i].Content)
		if recentChars < minRecentChars {
			continue
		}
		if cm.isReductionBoundary(i) {
			return i
		}
	}

	for i := len(cm.history) / 2; i > 0; i-- {
		if cm.isReductionBoundary(i) {
			return i
		}
	}
	return 0
}

func (cm *Manager) isReductionBoundary(index int) bool {
	if index <= 0 || index >= len(cm.history) {
		return false
	}
	current := cm.history[index]
	previous := cm.history[index-1]
	if current.Role == "tool_result" {
		return false
	}
	if previous.Role == "assistant" && len(previous.ToolCalls) > 0 {
		return false
	}
	return true
}

func (cm *Manager) sanitizeMessages(messages []brain.Message) []brain.Message {
	if len(messages) == 0 {
		return messages
	}
	var result []brain.Message
	hasPendingToolCalls := false
	for _, msg := range messages {
		if msg.Role == "tool_result" {
			if !hasPendingToolCalls {
				continue
			}
			result = append(result, msg)
		} else {
			result = append(result, msg)
			hasPendingToolCalls = msg.Role == "assistant" && len(msg.ToolCalls) > 0
		}
	}
	return result
}

// Clear resets the context.
func (cm *Manager) Clear() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.history = nil
	cm.compactedSummary = ""
}
