package skills

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter represents the YAML front matter of a skill file.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Triggers    []any  `yaml:"triggers"`
	Tools       any    `yaml:"tools"`
}

// parseFrontmatter parses a Markdown file with YAML front matter.
// Returns the parsed front matter and the body content.
func parseFrontmatter(content string) (*frontmatter, string, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return &frontmatter{}, content, nil
	}

	rest := content[3:]
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return &frontmatter{}, content, nil
	}

	yamlPart := rest[:idx]
	body := strings.TrimSpace(rest[idx+3:])

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return nil, "", err
	}

	return &fm, body, nil
}
