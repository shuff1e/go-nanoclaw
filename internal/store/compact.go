package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CompactionStats reports what was removed during compaction.
type CompactionStats struct {
	FilesProcessed int
	RecordsRemoved int
	BytesSaved     int64
}

// Compact removes expired memory records and trims JSONL files to maxAge.
func (s *FSStore) Compact(maxAge time.Duration, maxRecordsPerFile int) (*CompactionStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := &CompactionStats{}
	cutoff := time.Now().Add(-maxAge)

	dirs := []string{"sessions", "executions", "tool-audit", "approvals", "traces", "plans"}
	for _, dir := range dirs {
		dirPath := filepath.Join(s.baseDir, dir)
		if err := s.compactJSONLDir(dirPath, cutoff, maxRecordsPerFile, stats); err != nil {
			return stats, fmt.Errorf("compact %s: %w", dir, err)
		}
	}

	// Compact memory separately (removes expired + trims)
	memDir := filepath.Join(s.baseDir, "memory")
	if err := s.compactMemoryDir(memDir, maxRecordsPerFile, stats); err != nil {
		return stats, fmt.Errorf("compact memory: %w", err)
	}

	return stats, nil
}

func (s *FSStore) compactJSONLDir(dirPath string, cutoff time.Time, maxRecords int, stats *CompactionStats) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dirPath, entry.Name())
		if err := s.compactJSONLFile(path, cutoff, maxRecords, stats); err != nil {
			return fmt.Errorf("compact %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *FSStore) compactJSONLFile(path string, cutoff time.Time, maxRecords int, stats *CompactionStats) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var kept []string
	removed := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try to parse timestamp for age-based filtering
		var ts struct {
			StartedAt time.Time `json:"started_at,omitempty"`
			At        time.Time `json:"at,omitempty"`
			OccurredAt time.Time `json:"occurred_at,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &ts); err == nil {
			t := ts.StartedAt
			if t.IsZero() {
				t = ts.At
			}
			if t.IsZero() {
				t = ts.OccurredAt
			}
			if !t.IsZero() && t.Before(cutoff) {
				removed++
				continue
			}
		}

		kept = append(kept, line)
	}

	// Trim to max records (keep most recent)
	if maxRecords > 0 && len(kept) > maxRecords {
		dropped := kept[:len(kept)-maxRecords]
		removed += len(dropped)
		kept = kept[len(kept)-maxRecords:]
	}

	if removed == 0 {
		return nil
	}

	stats.FilesProcessed++
	stats.RecordsRemoved += removed

	newData := strings.Join(kept, "\n") + "\n"
	info, _ := os.Stat(path)
	if info != nil {
		stats.BytesSaved += info.Size() - int64(len(newData))
	}

	return os.WriteFile(path, []byte(newData), 0644)
}

func (s *FSStore) compactMemoryDir(dirPath string, maxRecords int, stats *CompactionStats) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dirPath, entry.Name())
		if err := s.compactMemoryFile(path, now, maxRecords, stats); err != nil {
			return fmt.Errorf("compact memory %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *FSStore) compactMemoryFile(path string, now time.Time, maxRecords int, stats *CompactionStats) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var kept []string
	removed := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var record MemoryRecord
		if err := json.Unmarshal([]byte(line), &record); err == nil {
			// Remove expired records
			if !record.ExpiresAt.IsZero() && record.ExpiresAt.Before(now) {
				removed++
				continue
			}
		}

		kept = append(kept, line)
	}

	if maxRecords > 0 && len(kept) > maxRecords {
		dropped := kept[:len(kept)-maxRecords]
		removed += len(dropped)
		kept = kept[len(kept)-maxRecords:]
	}

	if removed == 0 {
		return nil
	}

	stats.FilesProcessed++
	stats.RecordsRemoved += removed

	newData := strings.Join(kept, "\n") + "\n"
	info, _ := os.Stat(path)
	if info != nil {
		stats.BytesSaved += info.Size() - int64(len(newData))
	}

	return os.WriteFile(path, []byte(newData), 0644)
}
