package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// repoPathTestWorkspace creates a primary workspace (with a config.yaml the
// command under test writes) and a sibling "other" workspace that exists on
// disk, both under an isolated temporary root. BEADS_DIR points at the
// primary workspace so config.FindConfigYAMLPath resolves there.
func repoPathTestWorkspace(t *testing.T) (root, primary, other string) {
	t.Helper()
	root = t.TempDir()
	primary = filepath.Join(root, "primary")
	other = filepath.Join(root, "other")
	for _, dir := range []string{filepath.Join(primary, ".beads"), filepath.Join(other, ".beads")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(primary, ".beads", "config.yaml"), []byte("repos:\n  primary: \".\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(primary, ".beads"))
	return root, primary, other
}

// writeRepoConfigEntries replaces the workspace config's repos section with
// the given additional entries, in order.
func writeRepoConfigEntries(t *testing.T, primary string, entries ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("repos:\n  primary: \".\"\n")
	if len(entries) > 0 {
		b.WriteString("  additional:\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "    - %q\n", e)
		}
	}
	if err := os.WriteFile(filepath.Join(primary, ".beads", "config.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRepoConfigEntries(t *testing.T, primary string) []string {
	t.Helper()
	repos, err := config.GetReposFromYAML(filepath.Join(primary, ".beads", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return repos.Additional
}

// captureRepoCommandOutput captures what fn prints on stdout and returns it
// alongside fn's error, unlike captureStdout, which fails the test on any
// error: the repo remove tests must observe the command's error itself.
func captureRepoCommandOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	cmdErr := fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out, cmdErr
}

func TestNormalizeRepoPathForStorage(t *testing.T) {
	t.Run("relative path resolves against the cwd", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		got, err := normalizeRepoPathForStorage("a/b")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "a", "b"); got != want {
			t.Fatalf("normalizeRepoPathForStorage(%q) = %q, want %q", "a/b", got, want)
		}
	})

	t.Run("parent-relative path resolves from a nested cwd", func(t *testing.T) {
		root := t.TempDir()
		primary := filepath.Join(root, "primary")
		if err := os.MkdirAll(primary, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(primary)
		got, err := normalizeRepoPathForStorage("../other")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "other"); got != want {
			t.Fatalf("normalizeRepoPathForStorage(%q) = %q, want %q", "../other", got, want)
		}
	})

	t.Run("dot resolves to the cwd", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		got, err := normalizeRepoPathForStorage(".")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Clean(root); got != want {
			t.Fatalf("normalizeRepoPathForStorage(%q) = %q, want %q", ".", got, want)
		}
	})

	t.Run("resolution fails when the cwd is gone", func(t *testing.T) {
		dir := t.TempDir()
		restore, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(restore) }()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := normalizeRepoPathForStorage("other"); err == nil {
			t.Fatal("expected an error resolving a relative path with no working directory")
		}
	})

	absPath := filepath.Join(t.TempDir(), "work", "other")
	verbatim := []struct {
		name string
		path string
	}{
		{"absolute path", absPath},
		{"absolute path with trailing separator", absPath + string(filepath.Separator)},
		{"home-relative path", "~/planning"},
		{"bare tilde", "~"},
		{"tilde with user prefix is not expanded", "~user/planning"},
		{"https URL", "https://dolthub.com/org/planning"},
		{"ssh URL", "git@github.com:org/beads-planning.git"},
		{"dolthub URL", "dolthub://org/planning"},
		{"empty string", ""},
	}
	for _, tt := range verbatim {
		t.Run(tt.name+" stays verbatim", func(t *testing.T) {
			got, err := normalizeRepoPathForStorage(tt.path)
			if err != nil {
				t.Fatalf("normalizeRepoPathForStorage(%q): %v", tt.path, err)
			}
			if got != tt.path {
				t.Fatalf("normalizeRepoPathForStorage(%q) = %q, want it verbatim", tt.path, got)
			}
		})
	}
}

func TestRepoPathAddFromNestedCwdStoresWorkspaceIdentity(t *testing.T) {
	root, primary, other := repoPathTestWorkspace(t)
	nested := filepath.Join(primary, "deep", "nest")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	out, err := captureRepoCommandOutput(t, func() error {
		return repoAddCmd.RunE(repoAddCmd, []string{"../../../other"})
	})
	if err != nil {
		t.Fatalf("repo add: %v", err)
	}

	want := filepath.Clean(other)
	entries := readRepoConfigEntries(t, primary)
	if len(entries) != 1 || entries[0] != want {
		t.Fatalf("repos.additional = %v, want [%s]", entries, want)
	}
	if !strings.Contains(out, "Added repository: "+want) {
		t.Fatalf("output %q does not report the stored path %q", out, want)
	}

	// The stored entry keeps naming the added workspace from any cwd.
	t.Chdir(root)
	resolved, err := filepath.Abs(expandRepoTilde(entries[0]))
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("stored entry %q resolves to %q from %q, want %q", entries[0], resolved, root, want)
	}
}

func TestRepoPathAddReportsStoredPathInJSON(t *testing.T) {
	_, primary, other := repoPathTestWorkspace(t)
	t.Chdir(primary)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out, err := captureRepoCommandOutput(t, func() error {
		return repoAddCmd.RunE(repoAddCmd, []string{"../other"})
	})
	if err != nil {
		t.Fatalf("repo add: %v", err)
	}

	var result struct {
		Added bool   `json:"added"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("parse json output %q: %v", out, err)
	}
	want := filepath.Clean(other)
	if !result.Added || result.Path != want {
		t.Fatalf("json output = %+v, want added path %q", result, want)
	}
}

func TestRepoPathAddKeepsVerbatimTargets(t *testing.T) {
	t.Run("absolute target", func(t *testing.T) {
		_, primary, other := repoPathTestWorkspace(t)
		t.Chdir(t.TempDir()) // any cwd: an absolute target carries its own
		out, err := captureRepoCommandOutput(t, func() error {
			return repoAddCmd.RunE(repoAddCmd, []string{other})
		})
		if err != nil {
			t.Fatalf("repo add: %v", err)
		}
		if entries := readRepoConfigEntries(t, primary); len(entries) != 1 || entries[0] != other {
			t.Fatalf("repos.additional = %v, want [%s]", entries, other)
		}
		if !strings.Contains(out, "Added repository: "+other) {
			t.Fatalf("output %q does not report the stored path %q", out, other)
		}
	})

	t.Run("absolute target with trailing separator", func(t *testing.T) {
		_, primary, other := repoPathTestWorkspace(t)
		target := other + string(filepath.Separator)
		t.Chdir(t.TempDir())
		if _, err := captureRepoCommandOutput(t, func() error {
			return repoAddCmd.RunE(repoAddCmd, []string{target})
		}); err != nil {
			t.Fatalf("repo add: %v", err)
		}
		if entries := readRepoConfigEntries(t, primary); len(entries) != 1 || entries[0] != target {
			t.Fatalf("repos.additional = %v, want [%s] verbatim", entries, target)
		}
	})

	t.Run("home-relative target", func(t *testing.T) {
		_, primary, _ := repoPathTestWorkspace(t)
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "planning", ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		out, err := captureRepoCommandOutput(t, func() error {
			return repoAddCmd.RunE(repoAddCmd, []string{"~/planning"})
		})
		if err != nil {
			t.Fatalf("repo add: %v", err)
		}
		if entries := readRepoConfigEntries(t, primary); len(entries) != 1 || entries[0] != "~/planning" {
			t.Fatalf("repos.additional = %v, want [~/planning]", entries)
		}
		if !strings.Contains(out, "Added repository: ~/planning") {
			t.Fatalf("output %q does not report the stored ~/planning entry", out)
		}
	})

	t.Run("home lookup failure leaves the target unchanged", func(t *testing.T) {
		// With no home directory to expand, add still validates the literal
		// path, so a directory literally named '~' works. The stored entry is
		// the raw string either way.
		root, primary, _ := repoPathTestWorkspace(t)
		literal := filepath.Join(root, "~literal", ".beads")
		if err := os.MkdirAll(literal, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", "")
		t.Chdir(root)
		target := filepath.Join(root, "~literal")
		if _, err := captureRepoCommandOutput(t, func() error {
			return repoAddCmd.RunE(repoAddCmd, []string{target})
		}); err != nil {
			t.Fatalf("repo add: %v", err)
		}
		if entries := readRepoConfigEntries(t, primary); len(entries) != 1 || entries[0] != target {
			t.Fatalf("repos.additional = %v, want [%s]", entries, target)
		}
	})

	t.Run("remote URLs", func(t *testing.T) {
		_, primary, _ := repoPathTestWorkspace(t)
		t.Chdir(t.TempDir())
		for _, url := range []string{
			"https://dolthub.com/org/planning",
			"git@github.com:org/beads-planning.git",
		} {
			if _, err := captureRepoCommandOutput(t, func() error {
				return repoAddCmd.RunE(repoAddCmd, []string{url})
			}); err != nil {
				t.Fatalf("repo add %s: %v", url, err)
			}
		}
		entries := readRepoConfigEntries(t, primary)
		if len(entries) != 2 || entries[0] != "https://dolthub.com/org/planning" || entries[1] != "git@github.com:org/beads-planning.git" {
			t.Fatalf("repos.additional = %v, want both URLs verbatim", entries)
		}
	})
}

func TestRepoPathAddDuplicateSpellings(t *testing.T) {
	_, primary, other := repoPathTestWorkspace(t)
	t.Chdir(primary)

	if _, err := captureRepoCommandOutput(t, func() error {
		return repoAddCmd.RunE(repoAddCmd, []string{"../other"})
	}); err != nil {
		t.Fatalf("first repo add: %v", err)
	}
	want := filepath.Clean(other)
	if entries := readRepoConfigEntries(t, primary); len(entries) != 1 || entries[0] != want {
		t.Fatalf("repos.additional = %v, want [%s]", entries, want)
	}

	// The same relative spelling resolves to the stored absolute entry, so
	// the existing string-level duplicate detection still catches it.
	var addErr error
	stderr := captureStderr(t, func() {
		addErr = repoAddCmd.RunE(repoAddCmd, []string{"../other"})
	})
	if addErr == nil || !strings.Contains(stderr, "already configured") {
		t.Fatalf("second add of the same target should be rejected, got: %v (stderr %q)", addErr, stderr)
	}

	// The absolute spelling of the same target now normalizes to the stored
	// entry, so it is caught too: only byte-distinct spellings are distinct
	// entries, the duplicate check stays string-level.
	var absErr error
	stderr = captureStderr(t, func() {
		absErr = repoAddCmd.RunE(repoAddCmd, []string{want})
	})
	if absErr == nil || !strings.Contains(stderr, "already configured") {
		t.Fatalf("add by the stored absolute spelling should be rejected, got: %v (stderr %q)", absErr, stderr)
	}

	// A byte-distinct spelling (trailing separator) is a separate entry.
	trailing := want + string(filepath.Separator)
	if _, err := captureRepoCommandOutput(t, func() error {
		return repoAddCmd.RunE(repoAddCmd, []string{trailing})
	}); err != nil {
		t.Fatalf("repo add by trailing-separator spelling: %v", err)
	}
	entries := readRepoConfigEntries(t, primary)
	if len(entries) != 2 || entries[0] != want || entries[1] != trailing {
		t.Fatalf("repos.additional = %v, want [%s %s]", entries, want, trailing)
	}
}
