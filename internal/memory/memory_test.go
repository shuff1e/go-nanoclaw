package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-nanoclaw/internal/store"
)

func TestStructuredMemoryFiltersExpiredRecords(t *testing.T) {
	st := store.NewFSStore(t.TempDir())
	m := New(t.TempDir(), 20000)
	m.SetStore("main", st)

	now := time.Now().UTC()
	if err := st.SaveMemoryRecord("main", store.MemoryRecord{
		AgentID:    "main",
		Category:   "facts",
		Content:    "old fact",
		Source:     "test",
		Confidence: 0.8,
		ExpiresAt:  now.Add(-time.Hour),
		RecordedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("save expired memory: %v", err)
	}
	if err := st.SaveMemoryRecord("main", store.MemoryRecord{
		AgentID:    "main",
		Category:   "facts",
		Content:    "current fact",
		Source:     "test",
		Confidence: 0.7,
		ExpiresAt:  now.Add(24 * time.Hour),
		RecordedAt: now,
	}); err != nil {
		t.Fatalf("save current memory: %v", err)
	}

	bootstrap := m.AssembleBootstrap(false)
	if strings.Contains(bootstrap, "old fact") {
		t.Fatalf("expected expired memory filtered out, got %s", bootstrap)
	}
	if !strings.Contains(bootstrap, "current fact") || !strings.Contains(bootstrap, "confidence=0.70") {
		t.Fatalf("expected current memory with metadata, got %s", bootstrap)
	}
}

func TestStructuredMemoryOmitsEmptyExpiredSection(t *testing.T) {
	st := store.NewFSStore(t.TempDir())
	m := New(t.TempDir(), 20000)
	m.SetStore("main", st)

	now := time.Now().UTC()
	if err := st.SaveMemoryRecord("main", store.MemoryRecord{
		AgentID:    "main",
		Category:   "facts",
		Content:    "expired only",
		ExpiresAt:  now.Add(-time.Hour),
		RecordedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("save expired memory: %v", err)
	}

	bootstrap := m.AssembleBootstrap(false)
	if strings.Contains(bootstrap, "STRUCTURED_MEMORY") || strings.Contains(bootstrap, "expired only") {
		t.Fatalf("expected expired-only structured memory omitted, got %s", bootstrap)
	}
}

func TestCreateDefaultsUsesNanoClawWording(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, 20000)
	m.CreateDefaults()

	for _, name := range []string{"SOUL.md", "IDENTITY.md", "HEARTBEAT.md", "CAPABILITIES.md", "STARTUP.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read default %s: %v", name, err)
		}
		text := string(data)
		for _, retired := range []string{
			"Open" + "Claw",
			"Mini" + "Claw",
			"helpful AI assistant " + "powered by",
			"Learning project " + "based",
			"Check if there are any pending tasks or reminders.",
		} {
			if strings.Contains(text, retired) {
				t.Fatalf("%s contains retired wording %q: %s", name, retired, text)
			}
		}
	}
}

func TestAssembleBootstrapUsesHeartbeatWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, 20000)
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("native rules"), 0644); err != nil {
		t.Fatalf("write soul file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("heartbeat rules"), 0644); err != nil {
		t.Fatalf("write heartbeat file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("identity rules"), 0644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OPER"+"ATING.md"), []byte("retired workspace contract"), 0644); err != nil {
		t.Fatalf("write retired workspace file: %v", err)
	}

	bootstrap := m.AssembleBootstrap(false)
	for _, want := range []string{"native rules", "heartbeat rules", "identity rules"} {
		if !strings.Contains(bootstrap, want) {
			t.Fatalf("expected bootstrap content %q, got %s", want, bootstrap)
		}
	}
	if strings.Contains(bootstrap, "retired workspace contract") {
		t.Fatalf("expected retired workspace files to be ignored, got %s", bootstrap)
	}
}
