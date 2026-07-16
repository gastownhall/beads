package flatfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// counterState holds the per-prefix sequential ID counters and the
// per-parent hierarchical child-ID counters (the flat-file analog of the
// child_counters table).
type counterState struct {
	Prefixes map[string]int `json:"prefixes"`
	Children map[string]int `json:"children,omitempty"`
}

// nextSequentialID returns the next sequential ID for the given prefix and
// increments the counter atomically. The caller must not hold s.mu.
func (s *FlatFileStore) nextSequentialID(prefix string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readCounter()
	if err != nil {
		return "", err
	}

	// First allocation for a prefix seeds from existing issue files (mirrors
	// issueops.SeedCounterFromExistingIssuesTx) so generated sequential IDs
	// never collide with imported ones.
	if _, ok := state.Prefixes[prefix]; !ok {
		seed, err := s.maxSequentialSuffix(prefix)
		if err != nil {
			return "", err
		}
		state.Prefixes[prefix] = seed
	}

	next := state.Prefixes[prefix] + 1
	state.Prefixes[prefix] = next

	if err := s.writeCounter(state); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s-%d", prefix, next), nil
}

// peekSequentialID returns the last allocated sequential ID for a prefix
// without incrementing. Returns 0 if no IDs have been allocated.
func (s *FlatFileStore) peekSequentialID(prefix string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.readCounter()
	if err != nil {
		return 0, err
	}

	return state.Prefixes[prefix], nil
}

// maxSequentialSuffix returns the highest purely-numeric ID suffix among
// existing issue files for a prefix (0 when there are none). Non-numeric
// suffixes — hash IDs, hierarchical children — are skipped, matching the
// Atoi-style parse in issueops.SeedCounterFromExistingIssuesTx.
func (s *FlatFileStore) maxSequentialSuffix(prefix string) (int, error) {
	entries, err := os.ReadDir(s.issuesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("flatfile: scan issues for counter seed: %w", err)
	}
	maxNum := 0
	pfx := prefix + "-"
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !strings.HasPrefix(name, pfx) {
			continue
		}
		if n, err := strconv.Atoi(name[len(pfx):]); err == nil && n > maxNum {
			maxNum = n
		}
	}
	return maxNum, nil
}

// readCounter reads the counter state from disk.
// Caller must hold s.mu (read or write).
func (s *FlatFileStore) readCounter() (*counterState, error) {
	data, err := os.ReadFile(s.counterPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &counterState{Prefixes: make(map[string]int), Children: make(map[string]int)}, nil
		}
		return nil, fmt.Errorf("flatfile: read counter: %w", err)
	}

	var state counterState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("flatfile: parse counter: %w", err)
	}
	if state.Prefixes == nil {
		state.Prefixes = make(map[string]int)
	}
	if state.Children == nil {
		state.Children = make(map[string]int)
	}
	return &state, nil
}

// writeCounter writes the counter state to disk atomically (write-then-rename).
// Caller must hold s.mu for writing.
func (s *FlatFileStore) writeCounter(state *counterState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("flatfile: marshal counter: %w", err)
	}
	data = append(data, '\n')

	if err := s.atomicWrite(s.counterPath, data); err != nil {
		return fmt.Errorf("flatfile: write counter: %w", err)
	}
	return nil
}
