//go:build cgo

package embeddeddolt_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// runGit runs git in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Repository-local hooks path, so a developer's global core.hooksPath
	// cannot run inside the fixture repositories (engdocs/TESTING.md).
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=bd-test", "GIT_AUTHOR_EMAIL=bd-test@example.com",
		"GIT_COMMITTER_NAME=bd-test", "GIT_COMMITTER_EMAIL=bd-test@example.com",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0="+filepath.Join(dir, ".git", "hooks"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newBareGitRemote creates a bare repository with one commit on main, the
// shape a git host presents before any Dolt data has been pushed to it.
func newBareGitRemote(t *testing.T, name string) string {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, name)
	runGit(t, base, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	runGit(t, seed, "commit", "--allow-empty", "-m", "init")
	runGit(t, seed, "push", bare, "main")
	return bare
}

// lsRemoteRef returns the object a ref points at on the bare repository, or
// "" when the ref does not exist there.
func lsRemoteRef(t *testing.T, bare, ref string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, bare, "ls-remote", bare, ref))
}

// The engine, not a mock, is the authority on the SQL shapes above: a remote
// added with a ref lists that ref back from dolt_remotes and from
// repo_state.json, for a branch and for a ref outside refs/heads/; a remote
// added without one lists the default; Dolt refuses a ref on a non-git URL
// and the refusal reaches the caller; a push lands on the custom ref and
// never on refs/dolt/data; a clone with the ref records it on origin.
func TestRemoteGitDataRefOnEngine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	te := newTestEnv(t, "gref")

	shapes := map[string]string{
		"branch": "refs/heads/issue-data",
		"unit":   "refs/dolt/units/team-12542",
	}
	bares := map[string]string{}
	for name, ref := range shapes {
		bares[name] = newBareGitRemote(t, name)
		if err := te.store.AddRemoteWithRef(ctx, name, "git+file://"+bares[name], ref); err != nil {
			t.Fatalf("AddRemoteWithRef(%s, %s): %v", name, ref, err)
		}
	}
	plainDir := t.TempDir()
	if err := te.store.AddRemote(ctx, "plain", "file://"+plainDir); err != nil {
		t.Fatalf("AddRemote(plain): %v", err)
	}

	t.Run("listed from SQL and from disk", func(t *testing.T) {
		remotes, err := te.store.ListRemotes(ctx)
		if err != nil {
			t.Fatalf("ListRemotes: %v", err)
		}
		assertRefs(t, "ListRemotes", remotes, shapes)

		persisted, err := doltutil.PersistedRemotes(filepath.Join(te.dataDir, te.database))
		if err != nil {
			t.Fatalf("PersistedRemotes: %v", err)
		}
		assertRefs(t, "PersistedRemotes", persisted, shapes)
	})

	t.Run("ref on a non-git remote is Dolt's error, not a silent default", func(t *testing.T) {
		err := te.store.AddRemoteWithRef(ctx, "bad", "file://"+t.TempDir(), "refs/heads/issue-data")
		if err == nil {
			t.Fatal("AddRemoteWithRef on file:// with a ref = nil, want error")
		}
		if !strings.Contains(err.Error(), "git remotes") {
			t.Errorf("error should be Dolt's --ref refusal, got: %v", err)
		}
		if ok, _ := te.store.HasRemote(ctx, "bad"); ok {
			t.Error("refused remote must not exist")
		}
	})

	for name, ref := range shapes {
		t.Run("push and clone on "+ref, func(t *testing.T) {
			bare := bares[name]
			if err := te.store.PushRemote(ctx, name, false); err != nil {
				t.Fatalf("PushRemote(%s): %v", name, err)
			}
			if got := lsRemoteRef(t, bare, ref); got == "" {
				t.Fatalf("push did not create %s on the git remote", ref)
			}
			if got := lsRemoteRef(t, bare, storage.DefaultGitDataRef); got != "" {
				t.Errorf("push created %s (%s) although the remote was added with --ref %s", storage.DefaultGitDataRef, got, ref)
			}
			if got := lsRemoteRef(t, bare, "refs/heads/main"); got == "" {
				t.Error("refs/heads/main disappeared from the git remote")
			}

			cloneDir := t.TempDir()
			boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, cloneDir, "", "")
			if err != nil {
				t.Fatalf("OpenSQL for clone: %v", err)
			}
			cloneErr := versioncontrolops.DoltCloneWithRef(ctx, boot, "git+file://"+bare, "cloned", "", ref)
			_ = bootCleanup()
			if cloneErr != nil {
				t.Fatalf("DoltCloneWithRef: %v", cloneErr)
			}
			persisted, err := doltutil.PersistedRemotes(filepath.Join(cloneDir, "cloned"))
			if err != nil {
				t.Fatalf("PersistedRemotes on clone: %v", err)
			}
			assertRefs(t, "clone repo_state.json", persisted, map[string]string{"origin": ref})

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, cloneDir, "cloned", "main")
			if err != nil {
				t.Fatalf("OpenSQL on clone: %v", err)
			}
			defer func() { _ = cleanup() }()
			listed, err := versioncontrolops.ListRemotes(ctx, db)
			if err != nil {
				t.Fatalf("ListRemotes on clone: %v", err)
			}
			assertRefs(t, "clone dolt_remotes", listed, map[string]string{"origin": ref})
		})
	}
}

// assertRefs checks that every named remote in want lists exactly that ref
// and that the remote named plain, when present, lists the default.
func assertRefs(t *testing.T, where string, remotes []storage.RemoteInfo, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, r := range remotes {
		got[r.Name] = r.Ref
	}
	for name, ref := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s: remote %s missing from %+v", where, name, remotes)
			continue
		}
		if got[name] != ref {
			t.Errorf("%s: remote %s Ref = %q, want %q", where, name, got[name], ref)
		}
	}
	if ref, ok := got["plain"]; ok && ref != "" {
		t.Errorf("%s: remote plain Ref = %q, want empty", where, ref)
	}
}
