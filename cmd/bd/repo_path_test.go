package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// repoPathWorkspace is a primary workspace whose config.yaml bd repo add and
// bd repo remove edit, plus a sibling workspace to add.
type repoPathWorkspace struct {
	root       string // symlink-free temp root holding both workspaces
	primary    string
	other      string
	configPath string
}

// newRepoPathWorkspace builds root/primary and root/other, each with a .beads
// directory, and points BEADS_DIR at the primary's. Callers change cwd, so
// they must not run in parallel.
func newRepoPathWorkspace(t *testing.T) repoPathWorkspace {
	t.Helper()
	isolateBeadsDirForTest(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := repoPathWorkspace{
		root:    root,
		primary: filepath.Join(root, "primary"),
		other:   filepath.Join(root, "other"),
	}
	for _, dir := range []string{
		filepath.Join(w.primary, ".beads"),
		filepath.Join(w.primary, "nested", "deep"),
		filepath.Join(w.other, ".beads"),
		filepath.Join(root, "elsewhere"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.configPath = filepath.Join(w.primary, ".beads", "config.yaml")
	if err := os.WriteFile(w.configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(w.primary, ".beads"))
	savedJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = savedJSON })
	jsonOutput = false
	return w
}

func (w repoPathWorkspace) additional(t *testing.T) []string {
	t.Helper()
	repos, err := config.GetReposFromYAML(w.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return repos.Additional
}

// runRepoAdd runs bd repo add and returns its stdout.
func runRepoAdd(t *testing.T, arg string) (string, error) {
	t.Helper()
	return captureStdoutWithError(t, func() error {
		return repoAddCmd.RunE(repoAddCmd, []string{arg})
	})
}

func TestRepoPathOutputReportsStoredAbsolutePath(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		name := "text"
		if asJSON {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			w := newRepoPathWorkspace(t)
			jsonOutput = asJSON
			t.Chdir(filepath.Join(w.primary, "nested", "deep"))

			out, err := runRepoAdd(t, "../../../other")
			if err != nil {
				t.Fatalf("repo add: %v", err)
			}
			if got := w.additional(t); len(got) != 1 || got[0] != w.other {
				t.Fatalf("repos.additional = %q, want [%q]", got, w.other)
			}

			if asJSON {
				var result map[string]interface{}
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatalf("decode %q: %v", out, err)
				}
				if result["path"] != w.other {
					t.Fatalf("json path = %v, want %q", result["path"], w.other)
				}
			} else if !strings.Contains(out, "Added repository: "+w.other+"\n") {
				t.Fatalf("output %q does not report the stored path %q", out, w.other)
			}

			// The stored entry names the same workspace from another cwd.
			t.Chdir(filepath.Join(w.root, "elsewhere"))
			resolved, err := filepath.Abs(expandRepoTilde(w.additional(t)[0]))
			if err != nil {
				t.Fatal(err)
			}
			if !sameDir(t, resolved, w.other) {
				t.Fatalf("stored entry resolves to %s from another cwd, want %s", resolved, w.other)
			}
		})
	}
}

func TestRepoPathDuplicateRelativeSpellingIsRejected(t *testing.T) {
	w := newRepoPathWorkspace(t)
	t.Chdir(w.primary)
	if _, err := runRepoAdd(t, "../other"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	t.Chdir(filepath.Join(w.primary, "nested"))
	if _, err := runRepoAdd(t, "../../other"); err == nil {
		t.Fatal("adding the same workspace by another relative spelling succeeded, want a duplicate error")
	}
	if got := w.additional(t); len(got) != 1 || got[0] != w.other {
		t.Fatalf("repos.additional = %q, want [%q]", got, w.other)
	}
}

func TestNormalizeRepoPathForStorageRelative(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, tc := range []struct{ in, want string }{
		{"other", filepath.Join(root, "other")},
		{"./other/", filepath.Join(root, "other")},
		{"../other", filepath.Join(filepath.Dir(root), "other")},
		{".", root},
	} {
		got, err := normalizeRepoPathForStorage(tc.in)
		if err != nil {
			t.Fatalf("normalizeRepoPathForStorage(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("normalizeRepoPathForStorage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRepoPathForStorageReportsUnresolvableCwd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("getcwd of a removed directory fails only on Linux")
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", "")
	if got, err := normalizeRepoPathForStorage("other"); err == nil {
		t.Fatalf("normalizeRepoPathForStorage from a removed cwd = %q, want an error", got)
	}
}

func TestRepoPathOutputPreservesVerbatimArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  func(w repoPathWorkspace) string
		home func(w repoPathWorkspace) string
	}{
		{name: "absolute", arg: func(w repoPathWorkspace) string { return w.other }},
		{name: "absolute trailing separator", arg: func(w repoPathWorkspace) string { return w.other + string(filepath.Separator) }},
		{name: "absolute uncleaned", arg: func(w repoPathWorkspace) string {
			return filepath.Join(w.root, "elsewhere") + string(filepath.Separator) + ".." + string(filepath.Separator) + "other"
		}},
		{name: "https url", arg: func(repoPathWorkspace) string { return "https://github.com/example/planning.git" }},
		{name: "ssh url", arg: func(repoPathWorkspace) string { return "git@github.com:example/planning.git" }},
		{name: "home relative", arg: func(repoPathWorkspace) string { return "~/other" },
			home: func(w repoPathWorkspace) string { return w.root }},
		{name: "bare tilde", arg: func(repoPathWorkspace) string { return "~" },
			home: func(w repoPathWorkspace) string { return w.other }},
	} {
		for _, asJSON := range []bool{false, true} {
			name := tc.name + "/text"
			if asJSON {
				name = tc.name + "/json"
			}
			t.Run(name, func(t *testing.T) {
				w := newRepoPathWorkspace(t)
				if tc.home != nil {
					t.Setenv("HOME", tc.home(w))
				}
				jsonOutput = asJSON
				t.Chdir(filepath.Join(w.primary, "nested"))
				arg := tc.arg(w)

				out, err := runRepoAdd(t, arg)
				if err != nil {
					t.Fatalf("repo add %q: %v", arg, err)
				}
				if got := w.additional(t); len(got) != 1 || got[0] != arg {
					t.Fatalf("repos.additional = %q, want [%q] verbatim", got, arg)
				}
				if asJSON {
					var result map[string]interface{}
					if err := json.Unmarshal([]byte(out), &result); err != nil {
						t.Fatalf("decode %q: %v", out, err)
					}
					if result["path"] != arg {
						t.Fatalf("json path = %v, want %q verbatim", result["path"], arg)
					}
				} else if !strings.Contains(out, "Added repository: "+arg+"\n") {
					t.Fatalf("output %q does not report %q verbatim", out, arg)
				}
			})
		}
	}
}

func TestExpandRepoTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ in, want string }{
		{"~", home},
		{"~/planning", filepath.Join(home, "planning")},
		{"planning", "planning"},
		{"/abs/planning", "/abs/planning"},
	} {
		if got := expandRepoTilde(tc.in); got != tc.want {
			t.Errorf("expandRepoTilde(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandRepoTildeWithoutHomeLeavesEntryUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("os.UserHomeDir reads HOME only on Unix")
	}
	t.Setenv("HOME", "")
	if got := expandRepoTilde("~/planning"); got != "~/planning" {
		t.Fatalf("expandRepoTilde without a home directory = %q, want the entry unchanged", got)
	}
}

func TestNormalizeRepoPathForStorageVerbatim(t *testing.T) {
	t.Chdir(t.TempDir())
	abs := filepath.Join(t.TempDir(), "planning") + string(filepath.Separator)
	for _, in := range []string{
		abs,
		"https://github.com/example/planning.git",
		"git@github.com:example/planning.git",
		"~/planning",
		"~",
	} {
		got, err := normalizeRepoPathForStorage(in)
		if err != nil {
			t.Fatalf("normalizeRepoPathForStorage(%q): %v", in, err)
		}
		if got != in {
			t.Errorf("normalizeRepoPathForStorage(%q) = %q, want it verbatim", in, got)
		}
	}
}
