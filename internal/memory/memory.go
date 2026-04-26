// Package memory manages workspace Markdown bootstrap files.
package memory

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go-nanoclaw/internal/store"
)

var BootstrapFiles = []string{
	"STARTUP.md",
	"HEARTBEAT.md",
	"IDENTITY.md",
	"CAPABILITIES.md",
	"SOUL.md",
}

// Memory manages the workspace directory and its Markdown bootstrap files.
type Memory struct {
	WorkspaceDir    string
	MaxCharsPerFile int
	agentID         string
	store           store.Store
}

// New creates a new Memory and ensures the workspace directory exists.
func New(workspaceDir string, maxCharsPerFile int) *Memory {
	if maxCharsPerFile <= 0 {
		maxCharsPerFile = 20000
	}
	m := &Memory{
		WorkspaceDir:    workspaceDir,
		MaxCharsPerFile: maxCharsPerFile,
	}
	m.ensureWorkspace()
	return m
}

func (m *Memory) ensureWorkspace() {
	os.MkdirAll(m.WorkspaceDir, 0755)
	os.MkdirAll(filepath.Join(m.WorkspaceDir, "skills"), 0755)
}

func (m *Memory) SetStore(agentID string, st store.Store) {
	m.agentID = agentID
	m.store = st
}

// ReadFile reads a file from the workspace.
func (m *Memory) ReadFile(filename string) (string, error) {
	path := filepath.Join(m.WorkspaceDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	content := string(data)
	if len(content) > m.MaxCharsPerFile {
		slog.Warn("File exceeds max chars, truncating", "filename", filename, "max", m.MaxCharsPerFile)
		content = content[:m.MaxCharsPerFile] + "\n\n[...truncated...]"
	}
	return content, nil
}

// WriteFile writes a file to the workspace.
func (m *Memory) WriteFile(filename, content string) error {
	path := filepath.Join(m.WorkspaceDir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	slog.Debug("Wrote file", "filename", filename, "chars", len(content))
	return os.WriteFile(path, []byte(content), 0644)
}

// AppendMemory appends a learning to MEMORY.md.
func (m *Memory) AppendMemory(entry string) error {
	timestamp := time.Now().Format("2006-01-02 15:04")
	line := fmt.Sprintf("\n- [%s] %s\n", timestamp, entry)
	path := filepath.Join(m.WorkspaceDir, "MEMORY.md")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("# Memory\n"+line), 0644); err != nil {
			return err
		}
		return m.appendStructured("notes", entry, "remember_note", 1, time.Time{})
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	if err != nil {
		return err
	}
	return m.appendStructured("notes", entry, "remember_note", 1, time.Time{})
}

func (m *Memory) AppendCategorizedMemory(category, content, source string) error {
	return m.AppendCategorizedMemoryWithMetadata(category, content, source, 1, time.Time{})
}

func (m *Memory) AppendCategorizedMemoryWithMetadata(category, content, source string, confidence float64, expiresAt time.Time) error {
	timestamp := time.Now().Format("2006-01-02 15:04")
	line := fmt.Sprintf("\n- [%s] %s\n", timestamp, content)
	path := filepath.Join(m.WorkspaceDir, "MEMORY.md")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("# Memory\n"+line), 0644); err != nil {
			return err
		}
		return m.appendStructured(category, content, source, confidence, expiresAt)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return m.appendStructured(category, content, source, confidence, expiresAt)
}

// AssembleBootstrap assembles bootstrap context from workspace files.
func (m *Memory) AssembleBootstrap(firstRun bool) string {
	var parts []string
	for _, filename := range BootstrapFiles {
		if filename == "STARTUP.md" && !firstRun {
			continue
		}
		content, err := m.ReadFile(filename)
		if err != nil || content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("## [%s]\n\n%s", filename, content))
	}
	memoryContent, err := m.ReadFile("MEMORY.md")
	if err == nil && memoryContent != "" {
		parts = append(parts, fmt.Sprintf("## [MEMORY.md]\n\n%s", memoryContent))
	}
	if m.store != nil && m.agentID != "" {
		records, err := m.store.LoadMemoryRecords(m.agentID, 20)
		if err == nil && len(records) > 0 {
			if rendered := m.renderStructuredMemory(records); rendered != "" {
				parts = append(parts, rendered)
			}
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// ListBootstrapFiles returns the names of bootstrap files that actually exist in the workspace.
func (m *Memory) ListBootstrapFiles(firstRun bool) []string {
	var names []string
	for _, filename := range BootstrapFiles {
		if filename == "STARTUP.md" && !firstRun {
			continue
		}
		path := filepath.Join(m.WorkspaceDir, filename)
		if _, err := os.Stat(path); err == nil {
			names = append(names, filename)
		}
	}
	memPath := filepath.Join(m.WorkspaceDir, "MEMORY.md")
	if _, err := os.Stat(memPath); err == nil {
		names = append(names, "MEMORY.md")
	}
	return names
}

// ListSkills returns paths of skill files in the workspace.
func (m *Memory) ListSkills() []string {
	skillsDir := filepath.Join(m.WorkspaceDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}

	var paths []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".yaml") {
			paths = append(paths, filepath.Join(skillsDir, name))
		}
	}
	sort.Strings(paths)
	return paths
}

// CreateDefaults creates default workspace files if they don't exist.
func (m *Memory) CreateDefaults() {
	defaults := map[string]string{
		"SOUL.md": "# Soul\n\n" +
			"You are NanoClaw, a workspace-bound assistant runtime.\n" +
			"Answer directly, verify assumptions, and call out uncertainty before acting.\n\n" +
			"## Operating Surface\n" +
			"- Commands run through run_command and must stay within configured policy.\n" +
			"- Workspace files are accessed with read_workspace_file, write_workspace_file, and list_workspace.\n" +
			"- Network reads use fetch_url and are limited by the HTTP allowlist.\n" +
			"- Durable notes use remember_note and read_note.\n" +
			"- Delegated work uses delegate_task when a bounded subtask is appropriate.\n" +
			"- Scheduled work uses schedule_task and list_schedules.\n\n" +
			"## Scheduling\n" +
			"For reminders or recurring work, create a schedule entry instead of only describing the schedule.\n",
		"IDENTITY.md": "# Identity\n\n" +
			"- Name: NanoClaw\n" +
			"- Role: Workspace automation assistant\n" +
			"- Runtime: Go service with CLI, HTTP, Discord, memory, scheduling, and tool policy controls\n",
		"HEARTBEAT.md": "# Heartbeat\n\n" +
			"Review current reminders, scheduled work, and unresolved workspace notes.\n" +
			"Only report items that need action now. If there is nothing actionable, respond with HEARTBEAT_OK.\n",
		"CAPABILITIES.md": "# Capabilities\n\n" +
			"- Run approved commands.\n" +
			"- Read, write, and list workspace files under policy.\n" +
			"- Fetch approved HTTP(S) resources.\n" +
			"- Preserve durable notes and schedule future work.\n",
		"STARTUP.md": "# Startup\n\nReview the workspace identity and soul before the first response.\n",
		"MEMORY.md":  "# Memory\n",
	}

	for filename, content := range defaults {
		path := filepath.Join(m.WorkspaceDir, filename)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			os.WriteFile(path, []byte(content), 0644)
			slog.Info("Created default file", "filename", filename)
		}
	}
}

func (m *Memory) appendStructured(category, content, source string, confidence float64, expiresAt time.Time) error {
	if m.store == nil || m.agentID == "" {
		return nil
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return m.store.SaveMemoryRecord(m.agentID, store.MemoryRecord{
		AgentID:    m.agentID,
		Category:   category,
		Content:    content,
		Source:     source,
		Confidence: confidence,
		ExpiresAt:  expiresAt,
		RecordedAt: time.Now(),
	})
}

func (m *Memory) renderStructuredMemory(records []store.MemoryRecord) string {
	grouped := map[string][]string{}
	now := time.Now()
	for _, record := range records {
		if !record.ExpiresAt.IsZero() && record.ExpiresAt.Before(now) {
			continue
		}
		item := record.Content
		var attrs []string
		if record.Source != "" {
			attrs = append(attrs, "source="+record.Source)
		}
		if record.Confidence > 0 && record.Confidence < 1 {
			attrs = append(attrs, fmt.Sprintf("confidence=%.2f", record.Confidence))
		}
		if !record.ExpiresAt.IsZero() {
			attrs = append(attrs, "expires="+record.ExpiresAt.Format("2006-01-02"))
		}
		if len(attrs) > 0 {
			item += " (" + strings.Join(attrs, ", ") + ")"
		}
		grouped[record.Category] = append(grouped[record.Category], item)
	}
	order := []string{"profile", "facts", "preferences", "notes"}
	var sections []string
	sections = append(sections, "## [STRUCTURED_MEMORY]")
	for _, category := range order {
		items := grouped[category]
		if len(items) == 0 {
			continue
		}
		sections = append(sections, "### "+strings.ToUpper(category))
		for _, item := range items {
			sections = append(sections, "- "+item)
		}
	}
	if len(sections) == 1 {
		return ""
	}
	return strings.Join(sections, "\n")
}
