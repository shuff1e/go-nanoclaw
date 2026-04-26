// Package skills loads and matches skill definitions from Markdown+YAML files.
package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Trigger defines when a skill should activate.
type Trigger struct {
	Type    string // "keyword" | "regex" | "always"
	Pattern string
	re      *regexp.Regexp
}

// Matches checks if the trigger matches the input text.
func (t *Trigger) Matches(text string) bool {
	switch t.Type {
	case "always":
		return true
	case "keyword":
		return strings.Contains(strings.ToLower(text), strings.ToLower(t.Pattern))
	case "regex":
		if t.re == nil {
			var err error
			t.re, err = regexp.Compile("(?i)" + t.Pattern)
			if err != nil {
				return false
			}
		}
		return t.re.MatchString(text)
	}
	return false
}

// Skill represents a loaded skill definition.
type Skill struct {
	Name        string
	Description string
	Triggers    []Trigger
	Tools       []string
	Prompt      string
	SourcePath  string
}

// Matches checks if any of the skill's triggers match the input.
func (s *Skill) Matches(text string) bool {
	for i := range s.Triggers {
		if s.Triggers[i].Matches(text) {
			return true
		}
	}
	return false
}

// Registry loads and matches skills from a workspace/skills/ directory.
type Registry struct {
	skills []Skill
}

// NewRegistry creates a new empty skill registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// LoadFromDirectory loads all .md skill files from a directory.
func (r *Registry) LoadFromDirectory(skillsDir string) int {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}

	// Sort entries by name
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	count := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(skillsDir, e.Name())
		skill, err := r.parseSkillFile(path)
		if err != nil {
			slog.Warn("Failed to parse skill", "path", e.Name(), "error", err)
			continue
		}
		r.skills = append(r.skills, *skill)
		count++
		slog.Info("Loaded skill", "name", skill.Name, "triggers", len(skill.Triggers))
	}
	return count
}

// Match returns all skills whose triggers match the input text.
func (r *Registry) Match(text string) []Skill {
	var matched []Skill
	for _, s := range r.skills {
		if s.Matches(text) {
			matched = append(matched, s)
		}
	}
	return matched
}

// BuildIndex builds a compact index of all available skills.
func (r *Registry) BuildIndex() string {
	if len(r.skills) == 0 {
		return ""
	}
	lines := []string{"## Workspace Skill Index"}
	for _, s := range r.skills {
		var triggers []string
		for _, t := range s.Triggers {
			if t.Type != "always" {
				triggers = append(triggers, t.Pattern)
			}
		}
		triggerInfo := ""
		if len(triggers) > 0 {
			triggerInfo = fmt.Sprintf(" (triggers: %s)", strings.Join(triggers, ", "))
		}
		lines = append(lines, fmt.Sprintf("- **%s**: %s%s", s.Name, s.Description, triggerInfo))
	}
	return strings.Join(lines, "\n")
}

// ListSkills returns names and descriptions of all loaded skills.
func (r *Registry) ListSkills() []map[string]string {
	result := make([]map[string]string, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, map[string]string{
			"name":        s.Name,
			"description": s.Description,
		})
	}
	return result
}

// GetSkill finds a skill by name.
func (r *Registry) GetSkill(name string) *Skill {
	for i := range r.skills {
		if r.skills[i].Name == name {
			return &r.skills[i]
		}
	}
	return nil
}

func (r *Registry) parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fm, body, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	var triggers []Trigger
	for _, t := range fm.Triggers {
		switch v := t.(type) {
		case string:
			triggers = append(triggers, Trigger{Type: "keyword", Pattern: v})
		case map[string]any:
			typ, _ := v["type"].(string)
			pattern, _ := v["pattern"].(string)
			if typ == "" {
				typ = "keyword"
			}
			triggers = append(triggers, Trigger{Type: typ, Pattern: pattern})
		}
	}

	var tools []string
	switch v := fm.Tools.(type) {
	case string:
		tools = []string{v}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				tools = append(tools, s)
			}
		}
	}

	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return &Skill{
		Name:        name,
		Description: fm.Description,
		Triggers:    triggers,
		Tools:       tools,
		Prompt:      body,
		SourcePath:  path,
	}, nil
}
