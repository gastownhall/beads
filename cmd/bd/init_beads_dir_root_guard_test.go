package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// beadsDirRootGuardFixture builds a project root whose .beads holds an
// initialized workspace, and returns the root and that .beads directory.
func beadsDirRootGuardFixture(t *testing.T) (root, workspace string) {
	t.Helper()
	root = t.TempDir()
	workspace = filepath.Join(root, ".beads")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"real"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, workspace
}

// TestGuardInitBeadsDirAtProjectRoot covers the guard's two answers (GH#7034):
// refuse when BEADS_DIR names the project root of an initialized workspace,
// and stand aside for every BEADS_DIR init may initialize or already answers
// for.
func TestGuardInitBeadsDirAtProjectRoot(t *testing.T) {
	root, workspace := beadsDirRootGuardFixture(t)

	t.Run("refuses the project root", func(t *testing.T) {
		t.Setenv("BEADS_DIR", root)
		err := guardInitBeadsDirAtProjectRoot()
		var exit *exitError
		if !errors.As(err, &exit) || exit.Code != 1 {
			t.Fatalf("guardInitBeadsDirAtProjectRoot() = %v with BEADS_DIR=%q, want an exit 1 refusal", err, root)
		}
	})

	for name, beadsDir := range map[string]string{
		"unset":                         "",
		"the .beads directory itself":   workspace,
		"a directory with no workspace": t.TempDir(),
		"a .beads that does not exist":  filepath.Join(t.TempDir(), ".beads"),
	} {
		t.Run("allows "+name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", beadsDir)
			if err := guardInitBeadsDirAtProjectRoot(); err != nil {
				t.Fatalf("guardInitBeadsDirAtProjectRoot() = %v with BEADS_DIR=%q, want nil", err, beadsDir)
			}
		})
	}
}

// TestBeadsDirAtProjectRootMessages verifies both messages name the directory
// BEADS_DIR was set to and the .beads directory that was meant, and that the
// "no beads database found" note stays silent for any other BEADS_DIR.
func TestBeadsDirAtProjectRootMessages(t *testing.T) {
	root, workspace := beadsDirRootGuardFixture(t)

	refusal := beadsDirAtProjectRootRefusalMessage(root, workspace)
	note := beadsDirAtProjectRootNote(root)
	for label, text := range map[string]string{"refusal": refusal, "note": note} {
		if !strings.Contains(text, root) || !strings.Contains(text, ".beads") {
			t.Errorf("%s should name %q and its .beads directory, got:\n%s", label, root, text)
		}
	}
	if !strings.HasPrefix(refusal, "bd init refuses:") {
		t.Errorf("refusal should open like init's other refusals, got:\n%s", refusal)
	}

	for name, beadsDir := range map[string]string{
		"unset":                         "",
		"the .beads directory itself":   workspace,
		"a directory with no workspace": t.TempDir(),
	} {
		if got := beadsDirAtProjectRootNote(beadsDir); got != "" {
			t.Errorf("beadsDirAtProjectRootNote(%s: %q) = %q, want \"\"", name, beadsDir, got)
		}
	}
}
