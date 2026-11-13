package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
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

// TestRepoPathAddRejectsConfiguredLegacySpelling pins that add refuses an
// argument bd repo remove would match: re-adding a relative entry an older
// version stored as typed must not append its absolute twin.
func TestRepoPathAddRejectsConfiguredLegacySpelling(t *testing.T) {
	w := newRepoPathWorkspace(t)
	w.writeAdditional(t, "../other")
	t.Chdir(w.primary)

	var err error
	stderr := captureStderr(t, func() {
		err = repoAddCmd.RunE(repoAddCmd, []string{"../other"})
	})
	if err == nil || !strings.Contains(stderr, "repository already configured: ../other") {
		t.Fatalf("re-adding legacy entry ../other: err %v, stderr %q; want an already-configured error", err, stderr)
	}
	if got := w.additional(t); len(got) != 1 || got[0] != "../other" {
		t.Fatalf("repos.additional = %q, want only the legacy entry ../other", got)
	}
}

// TestRepoPathAddChecksStoredPathFromSymlinkedCwd pins that add looks for
// .beads at the path it stores. From a cwd entered through a symlink, the
// kernel resolves the argument's ".." physically while the stored absolute
// path drops it lexically, so the two can name different directories.
func TestRepoPathAddChecksStoredPathFromSymlinkedCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves .. lexically, so the argument and the stored path agree")
	}
	w := newRepoPathWorkspace(t)
	link := filepath.Join(w.root, "elsewhere", "primary")
	if err := os.Symlink(w.primary, link); err != nil {
		t.Fatal(err)
	}
	// t.Chdir sets PWD to the link: "../other" opens root/other, a workspace,
	// but filepath.Abs makes it root/elsewhere/other, which does not exist.
	t.Chdir(link)

	var err error
	stderr := captureStderr(t, func() {
		err = repoAddCmd.RunE(repoAddCmd, []string{"../other"})
	})
	missing := filepath.Join(w.root, "elsewhere", "other")
	if err == nil || !strings.Contains(stderr, "no beads workspace found at "+missing) {
		t.Fatalf("repo add ../other from a symlinked cwd: err %v, stderr %q; want no workspace found at %s", err, stderr, missing)
	}
	if got := w.additional(t); len(got) != 0 {
		t.Fatalf("repos.additional = %q, want it unchanged", got)
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
					// os.UserHomeDir reads USERPROFILE on Windows and HOME elsewhere.
					t.Setenv("HOME", tc.home(w))
					t.Setenv("USERPROFILE", tc.home(w))
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
	// os.UserHomeDir reads USERPROFILE on Windows and HOME elsewhere.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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

// repoRemoveRecordingStore records the destructive calls bd repo remove makes.
type repoRemoveRecordingStore struct {
	storage.DoltStorage
	deleted []string
	cleared []string
}

func (s *repoRemoveRecordingStore) DeleteIssuesBySourceRepo(_ context.Context, sourceRepo string) (int, error) {
	s.deleted = append(s.deleted, sourceRepo)
	return 2, nil
}

func (s *repoRemoveRecordingStore) ClearRepoMtime(_ context.Context, repoPath string) error {
	s.cleared = append(s.cleared, repoPath)
	return nil
}

// installRepoRemoveStore makes a recording store the active store for
// bd repo remove.
func installRepoRemoveStore(t *testing.T) *repoRemoveRecordingStore {
	t.Helper()
	saveAndRestoreGlobals(t)
	saveStorageMode(t)
	fake := &repoRemoveRecordingStore{}
	store = fake
	setStoreActive(true)
	return fake
}

func (w repoPathWorkspace) writeAdditional(t *testing.T, entries ...string) {
	t.Helper()
	if err := config.SetReposInYAML(w.configPath, &config.ReposConfig{Primary: ".", Additional: entries}); err != nil {
		t.Fatal(err)
	}
}

// runRepoRemove runs bd repo remove and returns its stdout.
func runRepoRemove(t *testing.T, arg string) (string, error) {
	t.Helper()
	return captureStdoutWithError(t, func() error {
		return repoRemoveCmd.RunE(repoRemoveCmd, []string{arg})
	})
}

func TestRepoPathRemovalMatchesStoredEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		// entries is the configured repos.additional list.
		entries func(w repoPathWorkspace) []string
		cwd     func(w repoPathWorkspace) string
		arg     func(w repoPathWorkspace) string
		// want is the entry that must be removed and used as the issue key.
		want func(w repoPathWorkspace) string
	}{
		{
			name:    "relative argument matches add-time absolute entry",
			entries: func(w repoPathWorkspace) []string { return []string{w.other} },
			cwd:     func(w repoPathWorkspace) string { return w.primary },
			arg:     func(repoPathWorkspace) string { return "../other" },
			want:    func(w repoPathWorkspace) string { return w.other },
		},
		{
			name:    "relative argument from nested cwd",
			entries: func(w repoPathWorkspace) []string { return []string{w.other} },
			cwd:     func(w repoPathWorkspace) string { return filepath.Join(w.primary, "nested", "deep") },
			arg:     func(repoPathWorkspace) string { return "../../../other" },
			want:    func(w repoPathWorkspace) string { return w.other },
		},
		{
			name:    "stored absolute entry from another cwd",
			entries: func(w repoPathWorkspace) []string { return []string{w.other} },
			cwd:     func(w repoPathWorkspace) string { return filepath.Join(w.root, "elsewhere") },
			arg:     func(w repoPathWorkspace) string { return w.other },
			want:    func(w repoPathWorkspace) string { return w.other },
		},
		{
			name:    "legacy relative entry from another cwd",
			entries: func(repoPathWorkspace) []string { return []string{"../other"} },
			cwd:     func(w repoPathWorkspace) string { return filepath.Join(w.root, "elsewhere") },
			arg:     func(repoPathWorkspace) string { return "../other" },
			want:    func(repoPathWorkspace) string { return "../other" },
		},
		{
			name:    "exact legacy entry wins over its absolute counterpart",
			entries: func(w repoPathWorkspace) []string { return []string{w.other, "../other"} },
			cwd:     func(w repoPathWorkspace) string { return w.primary },
			arg:     func(repoPathWorkspace) string { return "../other" },
			want:    func(repoPathWorkspace) string { return "../other" },
		},
		{
			name:    "missing target directory by relative argument",
			entries: func(w repoPathWorkspace) []string { return []string{filepath.Join(w.root, "gone")} },
			cwd:     func(w repoPathWorkspace) string { return w.primary },
			arg:     func(repoPathWorkspace) string { return "../gone" },
			want:    func(w repoPathWorkspace) string { return filepath.Join(w.root, "gone") },
		},
		{
			name:    "tilde entry by its tilde spelling",
			entries: func(repoPathWorkspace) []string { return []string{"~/other"} },
			cwd:     func(w repoPathWorkspace) string { return w.primary },
			arg:     func(repoPathWorkspace) string { return "~/other" },
			want:    func(repoPathWorkspace) string { return "~/other" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepoPathWorkspace(t)
			t.Setenv("HOME", w.root)
			fake := installRepoRemoveStore(t)
			entries := tc.entries(w)
			w.writeAdditional(t, entries...)
			t.Chdir(tc.cwd(w))
			want := tc.want(w)

			out, err := runRepoRemove(t, tc.arg(w))
			if err != nil {
				t.Fatalf("repo remove %q: %v", tc.arg(w), err)
			}
			if len(fake.deleted) != 1 || fake.deleted[0] != want {
				t.Fatalf("deleted source repos %q, want only [%q]", fake.deleted, want)
			}
			if len(fake.cleared) != 1 || fake.cleared[0] != want {
				t.Fatalf("cleared mtime keys %q, want only [%q]", fake.cleared, want)
			}
			var remaining []string
			for _, e := range entries {
				if e != want {
					remaining = append(remaining, e)
				}
			}
			got := w.additional(t)
			if strings.Join(got, "\n") != strings.Join(remaining, "\n") {
				t.Fatalf("repos.additional after remove = %q, want %q", got, remaining)
			}
			if !strings.Contains(out, "Removed repository: "+want+"\n") {
				t.Fatalf("output %q does not report the removed entry %q", out, want)
			}
		})
	}
}

// TestRepoPathAddThenRemoveByAddSpelling removes, by the relative spelling it
// was added with, an entry bd repo add stored as an absolute path.
func TestRepoPathAddThenRemoveByAddSpelling(t *testing.T) {
	w := newRepoPathWorkspace(t)
	fake := installRepoRemoveStore(t)
	t.Chdir(w.primary)
	if _, err := runRepoAdd(t, "../other"); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	jsonOutput = true
	out, err := runRepoRemove(t, "../other")
	if err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != w.other {
		t.Fatalf("deleted source repos %q, want [%q]", fake.deleted, w.other)
	}
	if len(fake.cleared) != 1 || fake.cleared[0] != w.other {
		t.Fatalf("cleared mtime keys %q, want [%q]", fake.cleared, w.other)
	}
	if got := w.additional(t); len(got) != 0 {
		t.Fatalf("repos.additional after remove = %q, want empty", got)
	}
	var result struct {
		Removed       bool   `json:"removed"`
		Path          string `json:"path"`
		IssuesDeleted int    `json:"issues_deleted"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !result.Removed || result.Path != w.other || result.IssuesDeleted != 2 {
		t.Fatalf("json output = %+v, want removed path %q with 2 issues deleted", result, w.other)
	}
}

// TestRepoPathRemovalRejectsBeforeDeleting pins that every failed lookup
// returns before bd repo remove deletes issues or clears the sync cache.
func TestRepoPathRemovalRejectsBeforeDeleting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, w repoPathWorkspace)
		arg   string
	}{
		{
			name: "missing config",
			setup: func(t *testing.T, w repoPathWorkspace) {
				if err := os.Remove(w.configPath); err != nil {
					t.Fatal(err)
				}
			},
			arg: "../other",
		},
		{
			name: "unparseable config",
			setup: func(t *testing.T, w repoPathWorkspace) {
				if err := os.WriteFile(w.configPath, []byte("repos: [unterminated\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			arg: "../other",
		},
		{
			name: "repos section is not a map",
			setup: func(t *testing.T, w repoPathWorkspace) {
				if err := os.WriteFile(w.configPath, []byte("repos: ../other\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			arg: "../other",
		},
		{
			name:  "empty argument",
			setup: func(t *testing.T, w repoPathWorkspace) { w.writeAdditional(t, w.other) },
			arg:   "",
		},
		{
			name:  "empty argument with an empty configured entry",
			setup: func(t *testing.T, w repoPathWorkspace) { w.writeAdditional(t, "", w.other) },
			arg:   "",
		},
		{
			name:  "no configured match",
			setup: func(t *testing.T, w repoPathWorkspace) { w.writeAdditional(t, w.other) },
			arg:   "../no-such-repo",
		},
		{
			name:  "no configured entries",
			setup: func(*testing.T, repoPathWorkspace) {},
			arg:   "../other",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepoPathWorkspace(t)
			fake := installRepoRemoveStore(t)
			tc.setup(t, w)
			t.Chdir(w.primary)

			if _, err := runRepoRemove(t, tc.arg); err == nil {
				t.Fatalf("repo remove %q succeeded, want an error", tc.arg)
			}
			if len(fake.deleted) != 0 || len(fake.cleared) != 0 {
				t.Fatalf("failed remove deleted issues for %q and cleared mtime keys %q, want neither", fake.deleted, fake.cleared)
			}
		})
	}
}
