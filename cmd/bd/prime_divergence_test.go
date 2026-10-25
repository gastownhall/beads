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

// The remaining tests cover the workspace the reminder is resolved against
// under -C (#5509): primeWorkspaceDir is the -C directory itself, not the
// parent of the beads dir that -C resolved to.

func TestPrimeDivergenceWorkspaceDir_ChangeDirUsesTarget(t *testing.T) {
	t.Cleanup(func() {
		changeDir = ""
	})
	// Only the cwd carries divergent agent files; the -C target has none.
	cwd := primeTestWorkspace(t, map[string]string{
		".beads/metadata.json": primeTestMetadata,
		"AGENTS.md":            "# Agents\n" + markerBlock,
		"CLAUDE.md":            "# Claude\n" + markerBlock,
	})
	target := primeTestWorkspace(t, map[string]string{".beads/metadata.json": primeTestMetadata})
	t.Chdir(cwd)

	changeDir = target
	if got := primeDivergenceReminder(primeWorkspaceDir()); got != "" {
		t.Fatalf("with -C, reminder against target must be empty, got %q", got)
	}
	changeDir = ""
	if got := primeDivergenceReminder(primeWorkspaceDir()); got == "" {
		t.Fatal("control: without -C the cwd fixture must emit a divergence note")
	}
}

// CASE A: the -C target is a redirect clone. The reminder must be resolved
// against the clone (where the agent files live), not against the parent of
// the external store that -C resolved BEADS_DIR to.
func TestPrimeDivergenceWorkspaceDir_RedirectUsesChangeDir(t *testing.T) {
	t.Cleanup(func() {
		changeDir = ""
	})
	external := primeTestWorkspace(t, map[string]string{".beads/metadata.json": primeTestMetadata})
	externalBeads := filepath.Join(external, ".beads")
	target := primeTestWorkspace(t, map[string]string{
		".beads/redirect": externalBeads + "\n",
		"AGENTS.md":       "# Agents\n" + markerBlock,
		"CLAUDE.md":       "# Claude\n" + markerBlock,
	})

	resolvedBeads, err := resolveChangeDirBeadsDir(target)
	if err != nil {
		t.Fatalf("resolveChangeDirBeadsDir: %v", err)
	}
	if filepath.Clean(resolvedBeads) != filepath.Clean(externalBeads) {
		t.Fatalf("resolved beads = %q, want external %q", resolvedBeads, externalBeads)
	}

	changeDir = target
	ws := primeWorkspaceDir()
	if ws != target {
		t.Fatalf("CASE A: primeWorkspaceDir = %q, want -C target %q (not parent of redirected beads %q)", ws, target, filepath.Dir(resolvedBeads))
	}
	if got := primeDivergenceReminder(ws); got == "" {
		t.Fatal("CASE A: reminder against -C target must emit divergence note")
	}
	if got := primeDivergenceReminder(filepath.Dir(resolvedBeads)); got != "" {
		t.Fatalf("control: parent-of-resolved-beads must not emit note, got %q", got)
	}
}

// CASE B: the -C target is a subdirectory of the workspace. -C resolves
// BEADS_DIR by walking up to the root's .beads, but the reminder still reads
// the -C directory itself, as `cd sub && bd prime` would.
func TestPrimeDivergenceWorkspaceDir_SubdirUsesChangeDir(t *testing.T) {
	t.Cleanup(func() {
		changeDir = ""
	})
	wsRoot := primeTestWorkspace(t, map[string]string{
		".beads/metadata.json": primeTestMetadata,
		"AGENTS.md":            "# Agents\n" + markerBlock,
		"CLAUDE.md":            "# Claude\n" + markerBlock,
		"sub/.keep":            "",
	})
	sub := filepath.Join(wsRoot, "sub")

	resolvedBeads, err := resolveChangeDirBeadsDir(sub)
	if err != nil {
		t.Fatalf("resolveChangeDirBeadsDir: %v", err)
	}
	if filepath.Clean(resolvedBeads) != filepath.Join(wsRoot, ".beads") {
		t.Fatalf("resolved beads = %q, want root %q", resolvedBeads, filepath.Join(wsRoot, ".beads"))
	}

	changeDir = sub
	ws := primeWorkspaceDir()
	if ws != sub {
		t.Fatalf("CASE B: primeWorkspaceDir = %q, want -C subdir %q (not parent-of-beads %q)", ws, sub, filepath.Dir(resolvedBeads))
	}
	if got := primeDivergenceReminder(ws); got != "" {
		t.Fatalf("CASE B: reminder against -C subdir must be empty, got %q", got)
	}
	if got := primeDivergenceReminder(filepath.Dir(resolvedBeads)); got == "" {
		t.Fatal("control: parent-of-beads-dir must emit note (proves CASE B distinguishes the wrong derivation)")
	}
}
