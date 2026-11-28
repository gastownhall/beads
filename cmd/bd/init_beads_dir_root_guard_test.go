package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/remedy"
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
// BEADS_DIR was set to where they say what is wrong, and the .beads directory
// that was meant where they say what to set it to, and that the "no beads
// database found" note stays silent for any other BEADS_DIR.
func TestBeadsDirAtProjectRootMessages(t *testing.T) {
	root, workspace := beadsDirRootGuardFixture(t)

	// The note names the .beads directory the helper finds: the fixture's,
	// canonicalized, so a symlinked temp root does not fail the match.
	meant, ok := beads.ProjectRootNamedAsBeadsDir(root)
	if !ok {
		t.Fatalf("beads.ProjectRootNamedAsBeadsDir(%q) reported no slip", root)
	}
	refusal := beadsDirAtProjectRootRefusalMessage(root, workspace)
	note := beadsDirAtProjectRootNote(root)
	for label, c := range map[string]struct {
		text  string
		marks []string
	}{
		"refusal": {refusal, []string{
			"BEADS_DIR names " + remedy.PrintedValue(root) + ", which holds no beads workspace, but " + remedy.PrintedValue(workspace) + " does.",
			"set BEADS_DIR to\n      " + remedy.PrintedValue(workspace) + "\n",
		}},
		"note": {note, []string{
			"BEADS_DIR is set to " + remedy.PrintedValue(root) + ", which holds no beads workspace;",
			"\n      " + remedy.PrintedValue(meant) + " does.",
		}},
	} {
		for _, mark := range c.marks {
			if !strings.Contains(c.text, mark) {
				t.Errorf("%s should contain %q, got:\n%s", label, mark, c.text)
			}
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

// TestImportRefusesBeadsDirAtProjectRoot covers bd import's answer under the
// same slip (GH#7034), which the root pre-run asks before discovery: refuse
// the project root of an initialized workspace, as bd init does, whatever
// else it holds, a database file of its own or a store directory an earlier
// import left there (embeddeddolt, or dolt) included, and stand aside for
// every other BEADS_DIR.
func TestImportRefusesBeadsDirAtProjectRoot(t *testing.T) {
	// rootWith builds the fixture's project root with the named entries of
	// its own: a directory where the content is nil, a file otherwise.
	rootWith := func(t *testing.T, entries map[string][]byte) string {
		t.Helper()
		root, _ := beadsDirRootGuardFixture(t)
		for name, content := range entries {
			path := filepath.Join(root, name)
			var err error
			if content == nil {
				err = os.MkdirAll(path, 0o700)
			} else {
				err = os.WriteFile(path, content, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	for _, tc := range []struct {
		name     string
		beadsDir func(t *testing.T) string
		want     bool
	}{
		{"the project root", func(t *testing.T) string { return rootWith(t, nil) }, true},
		{"a project root holding a database file of its own", func(t *testing.T) string {
			return rootWith(t, map[string][]byte{"app.db": []byte("not a beads store")})
		}, true},
		{"a project root holding a file named dolt", func(t *testing.T) string {
			return rootWith(t, map[string][]byte{"dolt": []byte("not a store")})
		}, true},
		{"a project root holding an embeddeddolt store", func(t *testing.T) string {
			return rootWith(t, map[string][]byte{"embeddeddolt": nil})
		}, true},
		{"a project root holding an embeddeddolt store and a config.json of its own", func(t *testing.T) string {
			return rootWith(t, map[string][]byte{"embeddeddolt": nil, "config.json": []byte(`{"database": "postgres://localhost/app", "port": 8080}`)})
		}, true},
		{"a project root holding a dolt store", func(t *testing.T) string {
			return rootWith(t, map[string][]byte{"dolt": nil})
		}, true},
		{"unset", func(*testing.T) string { return "" }, false},
		{"the .beads directory itself", func(t *testing.T) string {
			_, workspace := beadsDirRootGuardFixture(t)
			return workspace
		}, false},
		{"a directory with no workspace", func(t *testing.T) string { return t.TempDir() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := tc.beadsDir(t)
			t.Setenv("BEADS_DIR", beadsDir)
			if got := importRefusesBeadsDirAtProjectRoot(); got != tc.want {
				t.Errorf("importRefusesBeadsDirAtProjectRoot() = %v with BEADS_DIR=%q, want %v", got, beadsDir, tc.want)
			}
		})
	}
}
