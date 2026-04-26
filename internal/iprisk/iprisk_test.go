package iprisk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryHasNoLegacySourceMarkers(t *testing.T) {
	root := repoRoot(t)
	blocked := []string{
		"Mini" + "Claw",
		"spawn" + "_agent",
		"sessions" + "_spawn",
	}
	allowed := map[string]bool{
		filepath.Join(root, "docs", "ip-risk-review-plan.md"): true,
	}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".codex", "bin", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if allowed[path] || !isTextCandidate(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, marker := range blocked {
			if strings.Contains(content, marker) {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("legacy source marker %q found in %s", marker, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("go.mod not found")
		}
		dir = next
	}
}

func isTextCandidate(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".md", ".yaml", ".yml", ".json", ".txt", ".mod", ".sum":
		return true
	default:
		return false
	}
}
