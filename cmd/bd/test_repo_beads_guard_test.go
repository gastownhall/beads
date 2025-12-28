package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Guardrail: ensure the cmd/bd test suite does not touch the real repo .beads state.
// Disable with BEADS_TEST_GUARD_DISABLE=1 (useful when running tests while actively using beads).
func TestMain(m *testing.M) {
	if os.Getenv("BEADS_TEST_GUARD_DISABLE") != "" {
		os.Exit(m.Run())
	}

	repoRoot := findRepoRoot()
	if repoRoot == "" {
		os.Exit(m.Run())
	}

	repoBeadsDir := filepath.Join(repoRoot, ".beads")
	if _, err := os.Stat(repoBeadsDir); err != nil {
		os.Exit(m.Run())
	}

	watch := []string{
		"beads.db",
		"beads.db-wal",
		"beads.db-shm",
		"beads.db-journal",
		"issues.jsonl",
		"beads.jsonl",
		"metadata.json",
		"interactions.jsonl",
		"deletions.jsonl",
		"molecules.jsonl",
		"daemon.lock",
		"daemon.pid",
		"bd.sock",
	}

	before := snapshotFiles(repoBeadsDir, watch)
	code := m.Run()
	after := snapshotFiles(repoBeadsDir, watch)

	if diff := diffSnapshots(before, after); diff != "" {
		fmt.Fprintf(os.Stderr, "ERROR: test suite modified repo .beads state:\n%s\n", diff)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

type fileSnap struct {
	exists  bool
	size    int64
	modUnix int64
}

func snapshotFiles(dir string, names []string) map[string]fileSnap {
	out := make(map[string]fileSnap, len(names))
	for _, name := range names {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil {
			out[name] = fileSnap{exists: false}
			continue
		}
		out[name] = fileSnap{exists: true, size: info.Size(), modUnix: info.ModTime().UnixNano()}
	}
	return out
}

func diffSnapshots(before, after map[string]fileSnap) string {
	var out string
	for name, b := range before {
		a := after[name]
		if b.exists != a.exists {
			out += fmt.Sprintf("- %s: exists %v → %v\n", name, b.exists, a.exists)
			continue
		}
		if !b.exists {
			continue
		}
		if b.size != a.size || b.modUnix != a.modUnix {
			out += fmt.Sprintf("- %s: size %d → %d, mtime %s → %s\n",
				name,
				b.size,
				a.size,
				time.Unix(0, b.modUnix).UTC().Format(time.RFC3339Nano),
				time.Unix(0, a.modUnix).UTC().Format(time.RFC3339Nano),
			)
		}
	}
	return out
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 25; i++ {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	return ""
}
