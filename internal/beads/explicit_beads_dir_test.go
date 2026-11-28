package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

// TestProjectRootNamedAsBeadsDir pins the one shape the helper reports: a
// directory that is not a beads directory itself and whose .beads holds a
// workspace (GH#7034).
// Every neighbouring shape is a BEADS_DIR that `bd init` may initialize, or
// one init already answers for, so it must not be reported.
func TestProjectRootNamedAsBeadsDir(t *testing.T) {
	workspaceAt := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"real"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	foreignFile := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// rootHolding builds a project root whose .beads holds a workspace and
	// whose own file name, metadata.json or config.json, holds content.
	rootHolding := func(name, content string) func(t *testing.T, root string) string {
		return func(t *testing.T, root string) string {
			t.Helper()
			workspaceAt(t, filepath.Join(root, ".beads"))
			foreignFile(t, filepath.Join(root, name), content)
			return root
		}
	}
	cases := []struct {
		name string
		// setup builds the fixture under root and returns the BEADS_DIR value.
		setup func(t *testing.T, root string) string
		want  bool
	}{
		{name: "unset", setup: func(*testing.T, string) string { return "" }},
		{name: "project root of an initialized workspace", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			return root
		}},
		{name: "project root whose .beads redirects to a workspace", want: true, setup: func(t *testing.T, root string) string {
			real := filepath.Join(t.TempDir(), ".beads")
			workspaceAt(t, real)
			nested := filepath.Join(root, ".beads")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nested, RedirectFileName), []byte(real+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{name: "the .beads directory itself", setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			return filepath.Join(root, ".beads")
		}},
		{name: "directory with no .beads", setup: func(_ *testing.T, root string) string { return root }},
		{name: "directory that does not exist", setup: func(_ *testing.T, root string) string {
			return filepath.Join(root, "missing")
		}},
		{name: "directory whose .beads is empty", setup: func(t *testing.T, root string) string {
			if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{name: "directory whose .beads is a file", setup: func(t *testing.T, root string) string {
			if err := os.WriteFile(filepath.Join(root, ".beads"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{name: "a workspace that also contains a .beads workspace", setup: func(t *testing.T, root string) string {
			workspaceAt(t, root)
			workspaceAt(t, filepath.Join(root, ".beads"))
			return root
		}},
		{name: "directory holding a redirect that also contains a .beads workspace", setup: func(t *testing.T, root string) string {
			real := filepath.Join(t.TempDir(), ".beads")
			workspaceAt(t, real)
			if err := os.WriteFile(filepath.Join(root, RedirectFileName), []byte(real+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			workspaceAt(t, filepath.Join(root, ".beads"))
			return root
		}},
		{name: "a .beads directory with a stray .beads workspace nested in it", setup: func(t *testing.T, root string) string {
			outer := filepath.Join(root, ".beads")
			workspaceAt(t, filepath.Join(outer, ".beads"))
			return outer
		}},
		// A project root holds files of its own. None of these makes it the
		// workspace while the real one sits in its .beads.
		{name: "project root with its own config.yaml", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			foreignFile(t, filepath.Join(root, "config.yaml"), "server:\n  port: 8080\n")
			return root
		}},
		{name: "project root with a metadata.json that is not beads metadata", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			foreignFile(t, filepath.Join(root, "metadata.json"), `{"name":"app","version":"1.0"}`)
			return root
		}},
		{name: "project root with a metadata.json that is not JSON", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			foreignFile(t, filepath.Join(root, "metadata.json"), "not json")
			return root
		}},
		{name: "project root with an application database file", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			foreignFile(t, filepath.Join(root, "app.db"), "x")
			return root
		}},
		{name: "project root with a stray embeddeddolt directory", want: true, setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			if err := os.MkdirAll(filepath.Join(root, "embeddeddolt", "beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		// A project's own metadata.json or config.json keeps its settings
		// under the keys beads uses too. A key alone does not make the root a
		// workspace, or `bd init`, `bd import` and `bd bootstrap` would write
		// into it and migrate the file away.
		{name: "project root whose metadata.json names a database of its own", want: true,
			setup: rootHolding("metadata.json", `{"database":"analytics","tables":["events"]}`)},
		{name: "project root whose config.json names a database URL", want: true,
			setup: rootHolding("config.json", `{"database":"postgres://localhost/app","port":8080}`)},
		{name: "project root whose config.json names a backend URL", want: true,
			setup: rootHolding("config.json", `{"backend":"http://localhost:3000"}`)},
		{name: "project root whose config.json names a backend beads removed", want: true,
			setup: rootHolding("config.json", `{"backend":"postgres","host":"db.internal"}`)},
		{name: "project root whose config.json carries a project_id", want: true,
			setup: rootHolding("config.json", `{"project_id":"my-gcp-project","region":"us-central1"}`)},
		{name: "project root whose config.json names its own database file", want: true, setup: func(t *testing.T, root string) string {
			rootHolding("config.json", `{"database":"app.db"}`)(t, root)
			foreignFile(t, filepath.Join(root, "app.db"), "x")
			return root
		}},
		// Metadata only beads writes makes the directory a workspace of its
		// own, whichever bd wrote it.
		{name: "directory holding a metadata.json as bd init writes it", setup: func(t *testing.T, root string) string {
			workspaceAt(t, filepath.Join(root, ".beads"))
			cfg := &configfile.Config{
				Database:     "dolt",
				Backend:      configfile.BackendDolt,
				DoltMode:     configfile.DoltModeEmbedded,
				DoltDatabase: "real",
				ProjectID:    configfile.GenerateProjectID(),
			}
			if err := cfg.Save(root); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{name: "directory whose metadata.json names only a Dolt mode",
			setup: rootHolding("metadata.json", `{"dolt_mode":"server"}`)},
		{name: "directory whose metadata.json names only a Dolt database",
			setup: rootHolding("metadata.json", `{"dolt_database":"real"}`)},
		{name: "directory whose metadata.json names only the dolt backend",
			setup: rootHolding("metadata.json", `{"backend":"dolt"}`)},
		{name: "directory whose metadata.json names only the dolt database",
			setup: rootHolding("metadata.json", `{"database":"dolt"}`)},
		{name: "directory holding a metadata.json from before Dolt",
			setup: rootHolding("metadata.json", `{"database":"beads.db","jsonl_export":"issues.jsonl"}`)},
		{name: "directory holding a legacy beads config.json",
			setup: rootHolding("config.json", `{"database":"beads.db","version":"0.20.1","jsonl_export":"beads.jsonl"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			named := tc.setup(t, root)
			got, ok := ProjectRootNamedAsBeadsDir(named)
			if ok != tc.want {
				t.Fatalf("ProjectRootNamedAsBeadsDir(%q) = (%q, %v), want ok=%v", named, got, ok, tc.want)
			}
			if !tc.want {
				if got != "" {
					t.Fatalf("ProjectRootNamedAsBeadsDir(%q) returned %q with ok=false, want \"\"", named, got)
				}
				return
			}
			wantInfo, err := os.Stat(filepath.Join(root, ".beads"))
			if err != nil {
				t.Fatal(err)
			}
			gotInfo, err := os.Stat(got)
			if err != nil {
				t.Fatalf("ProjectRootNamedAsBeadsDir(%q) returned %q: %v", named, got, err)
			}
			if !os.SameFile(wantInfo, gotInfo) {
				t.Fatalf("ProjectRootNamedAsBeadsDir(%q) = %q, want the .beads directory inside %q", named, got, root)
			}
		})
	}
}
