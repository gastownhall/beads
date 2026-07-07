package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const markerBlock = "<!-- BEGIN BEADS INTEGRATION v:1 profile:agents hash:abc -->\nbody\n<!-- END BEADS INTEGRATION -->\n"

func writePrimeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestPrimeDivergenceReminder_BothIndependentWithMarker(t *testing.T) {
	dir := t.TempDir()
	writePrimeTestFile(t, filepath.Join(dir, "AGENTS.md"), "# Agents\n"+markerBlock)
	writePrimeTestFile(t, filepath.Join(dir, "CLAUDE.md"), "# Claude\n"+markerBlock)

	got := primeDivergenceReminder(dir)
	if got == "" {
		t.Fatal("expected reminder, got empty string")
	}
	if !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, "CLAUDE.md") {
		t.Fatalf("reminder missing file names: %q", got)
	}
	if strings.Count(got, "\n> ") != 1 {
		t.Fatalf("expected a single one-line note, got: %q", got)
	}
}

func TestPrimeDivergenceReminder_MissingOneFile(t *testing.T) {
	dir := t.TempDir()
	writePrimeTestFile(t, filepath.Join(dir, "AGENTS.md"), "# Agents\n"+markerBlock)
	// CLAUDE.md absent.
	if got := primeDivergenceReminder(dir); got != "" {
		t.Fatalf("expected empty when one file missing, got %q", got)
	}
}

func TestPrimeDivergenceReminder_MarkerMissingInOne(t *testing.T) {
	dir := t.TempDir()
	writePrimeTestFile(t, filepath.Join(dir, "AGENTS.md"), "# Agents\n"+markerBlock)
	writePrimeTestFile(t, filepath.Join(dir, "CLAUDE.md"), "# Claude without marker\n")
	if got := primeDivergenceReminder(dir); got != "" {
		t.Fatalf("expected empty when one file lacks marker, got %q", got)
	}
}

func TestPrimeDivergenceReminder_Symlink(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	claude := filepath.Join(dir, "CLAUDE.md")
	writePrimeTestFile(t, agents, "# Agents\n"+markerBlock)
	if err := os.Symlink(agents, claude); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unsupported: %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
	// CLAUDE.md is a symlink (to a file with the marker); reminder must be empty.
	if got := primeDivergenceReminder(dir); got != "" {
		t.Fatalf("expected empty when a file is a symlink, got %q", got)
	}
}

func TestPrimeDivergenceReminder_Hardlink(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	claude := filepath.Join(dir, "CLAUDE.md")
	writePrimeTestFile(t, agents, "# Agents\n"+markerBlock)
	if err := os.Link(agents, claude); err != nil {
		t.Skipf("shared-inode link unsupported: %v", err)
	}
	// Same inode: independent-files condition fails, so no reminder.
	if got := primeDivergenceReminder(dir); got != "" {
		t.Fatalf("expected empty when files share an inode, got %q", got)
	}
}

func TestPrimeDivergenceReminder_NeitherPresent(t *testing.T) {
	dir := t.TempDir()
	if got := primeDivergenceReminder(dir); got != "" {
		t.Fatalf("expected empty when neither file present, got %q", got)
	}
}

func TestPrimeDivergenceReminder_EmptyDirArgUsesCwd(t *testing.T) {
	// With "" the helper uses the current working directory; in a temp dir with
	// no agent files it must return empty (and not error).
	dir := t.TempDir()
	t.Chdir(dir)
	if got := primeDivergenceReminder(""); got != "" {
		t.Fatalf("expected empty for cwd without files, got %q", got)
	}
}
