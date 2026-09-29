package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// explicitBeadsDirFixture builds a git repo whose root holds an initialized
// workspace (parent/.beads with metadata.json) and a child directory that has
// not been initialized yet. It chdirs into the child, so an unconstrained walk
// up from CWD would bind the parent workspace.
func explicitBeadsDirFixture(t *testing.T) (parentBeadsDir, childDir string) {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", root)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+root, "XDG_CONFIG_HOME="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git not available: %v: %s", err, out)
	}
	parentBeadsDir = filepath.Join(root, ".beads")
	if err := os.MkdirAll(parentBeadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"parent"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	childDir = filepath.Join(root, "child")
	if err := os.MkdirAll(childDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(childDir)
	return parentBeadsDir, childDir
}

// TestFindBeadsDir_ExplicitBEADS_DIRIsAuthoritative verifies that an explicit
// BEADS_DIR which does not (yet) hold project files is never replaced by a
// workspace discovered by walking up from CWD. Before the fix, FindBeadsDir
// silently fell back to the ancestor walk, so every command — including
// `bd init` after PersistentPreRun rebinds BEADS_DIR — targeted the parent
// workspace instead of the directory the caller named.
func TestFindBeadsDir_ExplicitBEADS_DIRIsAuthoritative(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, explicit string)
	}{
		{name: "missing", setup: func(*testing.T, string) {}},
		{name: "empty", setup: func(t *testing.T, explicit string) {
			if err := os.MkdirAll(explicit, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-project files only", setup: func(t *testing.T, explicit string) {
			if err := os.MkdirAll(explicit, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(explicit, "registry.json"), []byte("[]"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parentBeadsDir, childDir := explicitBeadsDirFixture(t)
			explicit := filepath.Join(childDir, ".beads")
			tc.setup(t, explicit)
			t.Setenv("BEADS_DIR", explicit)

			if got := FindBeadsDir(); got != "" {
				t.Fatalf("FindBeadsDir() = %q with BEADS_DIR=%q; want \"\" (explicit BEADS_DIR must not fall back to the ancestor workspace %q)", got, explicit, parentBeadsDir)
			}
			if got := FindDatabasePath(); got != "" {
				t.Fatalf("FindDatabasePath() = %q with BEADS_DIR=%q; want \"\"", got, explicit)
			}
		})
	}
}

// TestFindBeadsDir_ExplicitBEADS_DIRSelectedOnceInitialized verifies the
// explicit directory is returned as soon as it holds project files, even with
// an initialized ancestor workspace.
func TestFindBeadsDir_ExplicitBEADS_DIRSelectedOnceInitialized(t *testing.T) {
	_, childDir := explicitBeadsDirFixture(t)
	explicit := filepath.Join(childDir, ".beads")
	if err := os.MkdirAll(explicit, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"child"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", explicit)

	got, _ := filepath.EvalSymlinks(FindBeadsDir())
	want, _ := filepath.EvalSymlinks(explicit)
	if got != want {
		t.Fatalf("FindBeadsDir() = %q; want explicit BEADS_DIR %q", got, want)
	}
}

// TestFindBeadsDir_UnsetBEADS_DIRStillWalksUp pins the unchanged behaviour
// when BEADS_DIR is not set: discovery walks up from CWD to the ancestor
// workspace.
func TestFindBeadsDir_UnsetBEADS_DIRStillWalksUp(t *testing.T) {
	parentBeadsDir, _ := explicitBeadsDirFixture(t)
	t.Setenv("BEADS_DIR", "")
	if err := os.Unsetenv("BEADS_DIR"); err != nil {
		t.Fatal(err)
	}

	got, _ := filepath.EvalSymlinks(FindBeadsDir())
	want, _ := filepath.EvalSymlinks(parentBeadsDir)
	if got != want {
		t.Fatalf("FindBeadsDir() = %q; want ancestor workspace %q", got, want)
	}
}
