package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAppend_ConcurrentWritersUniqueIDs proves the audit log's uniqueness
// invariant under concurrent writers.
//
// Background: a related bug in gascity's events.jsonl recorder uses an
// in-process sequence counter that is seeded by scanning the file. Two
// FileRecorder instances sharing a file race on the seed and produce
// duplicate sequence numbers.
//
// The audit package does NOT have this bug because:
//  1. There is no sequence / ordinal field on Entry — see audit.go Entry{}.
//  2. Each entry's identifier is a freshly generated random ID (newID()
//     in audit.go), derived from crypto/rand, not from any shared counter.
//     Two concurrent writers therefore cannot collide on a seeded counter
//     because no such counter exists.
//
// This test locks that invariant in: many concurrent writers appending many
// entries must all receive unique IDs, and every line on disk must be a
// well-formed JSON entry (no torn writes at the line level for reasonably
// sized payloads, which is guaranteed by O_APPEND semantics on POSIX for
// writes under PIPE_BUF).
func TestAppend_ConcurrentWritersUniqueIDs(t *testing.T) {
	// Set up an isolated .beads directory that FindBeadsDir will accept.
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	const (
		writers          = 8
		entriesPerWriter = 250
		totalEntries     = writers * entriesPerWriter
	)

	var (
		mu          sync.Mutex
		returnedIDs = make(map[string]int, totalEntries)
		firstErr    error
	)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < entriesPerWriter; i++ {
				id, err := Append(&Entry{
					Kind:  "llm_call",
					Actor: "concurrency-test",
					Model: "test-model",
					// Keep payload small enough that POSIX O_APPEND writes
					// remain atomic (under PIPE_BUF, typically 4096 bytes).
					Prompt:   "p",
					Response: "r",
					Extra: map[string]any{
						"worker": worker,
						"i":      i,
					},
				})
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if id != "" {
					returnedIDs[id]++
				}
				mu.Unlock()
			}
		}(w)
	}
	close(start)
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("append error: %v", firstErr)
	}
	if got := len(returnedIDs); got != totalEntries {
		// Any duplicate collapses the map size; surface the dup set.
		var dups []string
		for id, n := range returnedIDs {
			if n > 1 {
				dups = append(dups, id)
			}
		}
		t.Fatalf("expected %d unique returned IDs, got %d (duplicate IDs: %v)",
			totalEntries, got, dups)
	}

	// Verify the on-disk file is well-formed: every line parses as an Entry,
	// and every ID in the file is unique and matches a returned ID.
	p := filepath.Join(beadsDir, FileName)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	fileIDs := make(map[string]int, totalEntries)
	sc := bufio.NewScanner(f)
	// Entries are small but allow headroom.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("line %d: invalid JSON (torn write?): %v: %q",
				lineNo, err, truncate(string(line), 200))
		}
		if e.ID == "" {
			t.Fatalf("line %d: entry has empty ID", lineNo)
		}
		fileIDs[e.ID]++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(fileIDs) != totalEntries {
		var dups []string
		for id, n := range fileIDs {
			if n > 1 {
				dups = append(dups, id)
			}
		}
		t.Fatalf("expected %d unique IDs in file, got %d (duplicates: %v)",
			totalEntries, len(fileIDs), dups)
	}
	// Every returned ID must also appear in the file.
	for id := range returnedIDs {
		if _, ok := fileIDs[id]; !ok {
			t.Fatalf("returned ID %q missing from audit file", id)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
