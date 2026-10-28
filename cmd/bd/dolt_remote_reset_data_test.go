package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/execenv"
	"github.com/steveyegge/beads/internal/githooksenv"
	"github.com/steveyegge/beads/internal/storage"
)

// TestResetDataRefNamesMatchDolt keeps the locally pinned data-plane ref
// names in sync with the constants dolt's git blobstore actually publishes
// (store/blobstore/git_refs.go). If a dolt bump renames either ref, this
// fails instead of reset-data silently deleting the wrong refs.
func TestResetDataRefNamesMatchDolt(t *testing.T) {
	if gitDoltDataRef != blobstore.DoltDataRef {
		t.Errorf("gitDoltDataRef = %q, dolt publishes %q", gitDoltDataRef, blobstore.DoltDataRef)
	}
	if want := "refs/heads/" + blobstore.DefaultInfoBranch; gitDoltInfoRef != want {
		t.Errorf("gitDoltInfoRef = %q, dolt publishes %q", gitDoltInfoRef, want)
	}
}

func TestEnvWithNoGitHooksPreservesExistingParameters(t *testing.T) {
	const existing = "'user.email=ci@example.com'"
	t.Setenv(githooksenv.ParametersEnv, existing)
	t.Setenv("BEADS_TEST_NO_HOOKS_KEEP", "GIT_CONFIG_PARAMETERS=unrelated-value")

	// Build the expected slice independently of Without/DisabledEnv so a
	// stale entry cannot hide behind Extract's last-wins lookup.
	var want []string
	for _, entry := range os.Environ() {
		if !execenv.KeyEqual(execenv.EntryKey(entry), githooksenv.ParametersEnv) {
			want = append(want, entry)
		}
	}
	want = append(want, githooksenv.ParametersEnv+"="+existing+" "+githooksenv.NoHooksParam)
	if got := envWithNoGitHooks(); !slices.Equal(got, want) {
		t.Fatalf("envWithNoGitHooks() differs from the expected environment: got %d entries, want %d", len(got), len(want))
	}
}

func TestClassifyResetDataRemote(t *testing.T) {
	makeBareGitRepo := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	makeDoltFileStore := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	// writeFiles writes each named file, its directories included, into dir.
	writeFiles := func(t *testing.T, dir string, files ...string) string {
		for _, f := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	bareRepo := makeBareGitRepo(t)
	fileStore := makeDoltFileStore(t)
	// What Dolt leaves after pushing to a file:// remote in a bare git
	// repository: its file store among the repository's files.
	repoWithStore := makeBareGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoWithStore, "manifest"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same in a checkout, beside .git and the working files, and in a
	// linked worktree, whose .git is a file naming its git directory.
	checkoutWithStore := writeFiles(t, t.TempDir(), ".git/HEAD", ".git/objects/info/packs", "README.md", "manifest")
	worktreeWithStore := writeFiles(t, t.TempDir(), ".git", "README.md", "manifest")
	checkout := writeFiles(t, t.TempDir(), ".git/HEAD", "README.md")
	emptyDir := t.TempDir()
	strangeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(strangeDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		url     string
		want    resetDataKind
		wantErr bool
	}{
		{"git+https", "git+https://github.com/org/repo.git", resetDataGitBacked, false},
		{"git+ssh", "git+ssh://git@github.com/org/repo.git", resetDataGitBacked, false},
		{"scp-style", "git@github.com:org/repo.git", resetDataGitBacked, false},
		{"git+file", "git+file://" + bareRepo, resetDataGitBacked, false},
		{"file url to bare git repo", "file://" + bareRepo, resetDataFileInGitRepo, false},
		{"bare path to bare git repo", bareRepo, resetDataFileInGitRepo, false},
		{"file url to bare git repo holding a dolt store", "file://" + repoWithStore, resetDataFileInGitRepo, false},
		{"file url to git checkout holding a dolt store", "file://" + checkoutWithStore, resetDataFileInGitRepo, false},
		{"bare path to git checkout holding a dolt store", checkoutWithStore, resetDataFileInGitRepo, false},
		{"file url to linked worktree holding a dolt store", "file://" + worktreeWithStore, resetDataFileInGitRepo, false},
		{"file url to git checkout", "file://" + checkout, resetDataFileInGitRepo, false},
		{"file url to dolt store", "file://" + fileStore, resetDataFileStore, false},
		{"bare path to dolt store", fileStore, resetDataFileStore, false},
		{"file url to missing dir", "file://" + filepath.Join(emptyDir, "nope"), resetDataFileAbsent, false},
		{"file url to empty dir", "file://" + emptyDir, resetDataFileAbsent, false},
		{"file url to unrecognized dir", "file://" + strangeDir, 0, true},
		{"aws", "aws://[table:bucket]/db", resetDataUnsupported, false},
		{"gs", "gs://bucket/db", resetDataUnsupported, false},
		{"dolthub", "dolthub://org/db", resetDataUnsupported, false},
		{"https hosted", "https://doltremoteapi.dolthub.com/org/db", resetDataUnsupported, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyResetDataRemote(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("classifyResetDataRemote(%q) = %v, want error", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyResetDataRemote(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("classifyResetDataRemote(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestResetDataGitURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git+https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "ssh://git@github.com/org/repo.git"},
		{"git+file:///tmp/remote.git", "file:///tmp/remote.git"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
	}
	for _, tt := range tests {
		if got := resetDataGitURL(tt.url); got != tt.want {
			t.Errorf("resetDataGitURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

// TestMoveGitRefReplacesOnlyTheValueReadFirst pins the swap the server-mode
// reset-data ends with: the data ref moves to the fresh store's commit in one
// update, and only from the value reset-data read before it pushed.
func TestMoveGitRefReplacesOnlyTheValueReadFirst(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	forEachGitObjectFormat(t, func(t *testing.T, initFlags []string) {
		testMoveGitRef(t, run, initFlags)
	})
}

func testMoveGitRef(t *testing.T, run func(t *testing.T, dir string, args ...string) string, initFlags []string) {
	setup := func(t *testing.T) (remote, url, oldOID, newOID string) {
		base := t.TempDir()
		remote = filepath.Join(base, "remote.git")
		run(t, base, append([]string{"init", "-q", "--bare"}, append(initFlags, remote)...)...)
		work := filepath.Join(base, "work")
		run(t, base, append([]string{"init", "-q"}, append(initFlags, work)...)...)
		for i, ref := range []string{"refs/dolt/data", "refs/dolt/bd-reset-data-test"} {
			run(t, work, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", strconv.Itoa(i))
			run(t, work, "push", "-q", remote, "HEAD:"+ref)
		}
		oid := func(ref string) string { return run(t, remote, "rev-parse", ref) }
		return remote, "file://" + remote, oid("refs/dolt/data"), oid("refs/dolt/bd-reset-data-test")
	}
	ctx := context.Background()
	t.Run("moves from the value read first", func(t *testing.T) {
		remote, url, oldOID, newOID := setup(t)
		if err := moveGitRef(ctx, url, "refs/dolt/bd-reset-data-test", newOID, "refs/dolt/data", oldOID); err != nil {
			t.Fatal(err)
		}
		if got := run(t, remote, "rev-parse", "refs/dolt/data"); got != newOID {
			t.Errorf("refs/dolt/data = %s, want the fresh store %s", got, newOID)
		}
	})
	t.Run("refuses when the data ref moved since", func(t *testing.T) {
		remote, url, oldOID, newOID := setup(t)
		if err := moveGitRef(ctx, url, "refs/dolt/bd-reset-data-test", newOID, "refs/dolt/data", strings.Repeat("1", len(oldOID))); err == nil {
			t.Fatal("moveGitRef replaced a data ref another client had moved")
		}
		if got := run(t, remote, "rev-parse", "refs/dolt/data"); got != oldOID {
			t.Errorf("refs/dolt/data = %s, want it untouched at %s", got, oldOID)
		}
	})
	t.Run("creates an absent data ref, and only an absent one", func(t *testing.T) {
		remote, url, oldOID, newOID := setup(t)
		run(t, remote, "update-ref", "-d", "refs/dolt/data")
		if err := moveGitRef(ctx, url, "refs/dolt/bd-reset-data-test", newOID, "refs/dolt/data", ""); err != nil {
			t.Fatal(err)
		}
		if got := run(t, remote, "rev-parse", "refs/dolt/data"); got != newOID {
			t.Errorf("refs/dolt/data = %s, want %s", got, newOID)
		}
		run(t, remote, "update-ref", "refs/dolt/data", oldOID)
		if err := moveGitRef(ctx, url, "refs/dolt/bd-reset-data-test", newOID, "refs/dolt/data", ""); err == nil {
			t.Error("moveGitRef replaced a data ref it expected to be absent")
		}
	})
}

// TestDeleteTempGitRefWorksOutsideAGitRepository pins the temporary-ref
// cleanup of the server-mode reset-data: it runs from a scratch repository,
// so a BEADS_DIR workspace outside git still removes the ref, and it still
// runs after the command's context was canceled (Ctrl-C).
func TestDeleteTempGitRefWorksOutsideAGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	forEachGitObjectFormat(t, func(t *testing.T, initFlags []string) {
		testDeleteTempGitRefOutsideAGitRepository(t, run, initFlags)
	})
}

func testDeleteTempGitRefOutsideAGitRepository(t *testing.T, run func(t *testing.T, dir string, args ...string) string, initFlags []string) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	run(t, base, append([]string{"init", "-q", "--bare"}, append(initFlags, remote)...)...)
	work := filepath.Join(base, "work")
	run(t, base, append([]string{"init", "-q"}, append(initFlags, work)...)...)
	run(t, work, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "0")
	const tmpRef = "refs/dolt/bd-reset-data-test"
	run(t, work, "push", "-q", remote, "HEAD:refs/dolt/data", "HEAD:"+tmpRef)

	outside := filepath.Join(base, "not-a-repo")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outside)
	t.Setenv("GIT_CEILING_DIRECTORIES", base)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := deleteTempGitRef(context.WithoutCancel(ctx), "file://"+remote, tmpRef); err != nil {
		t.Fatal(err)
	}
	refs := run(t, remote, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, tmpRef) {
		t.Errorf("temporary ref still on the remote: %q", refs)
	}
	if !strings.Contains(refs, "refs/dolt/data") {
		t.Errorf("data ref removed with the temporary ref: %q", refs)
	}
}

// fakeResetDataServerStore is the part of a server-mode store the server
// route of reset-data drives: a temporary remote and a push to it.
type fakeResetDataServerStore struct {
	storage.DoltStorage
	push func(remote string) error
}

func (f *fakeResetDataServerStore) AddRemoteWithRef(context.Context, string, string, string) error {
	return nil
}
func (f *fakeResetDataServerStore) RemoveRemote(context.Context, string) error { return nil }
func (f *fakeResetDataServerStore) PushRemote(_ context.Context, remote string, _ bool) error {
	return f.push(remote)
}

// unmanagedResetDataServerStore is a server-mode store whose Dolt server is
// not the one bd manages for the workspace, as for a server the operator
// runs.
type unmanagedResetDataServerStore struct {
	fakeResetDataServerStore
}

func (*unmanagedResetDataServerStore) ManagedServerDir() (string, bool) { return "", false }

// TestResetThroughServerAsksForTheRestartBdDoesNotDo pins the report of a
// reset through a Dolt server bd does not restart: one that is not the server
// bd manages for the workspace, or one behind a store that cannot say. That
// server still holds its pre-reset view of the remote, so the text report
// warns to restart it before the next push or pull of the remote, and --json
// sets server_restart_required with that warning, not server_restarted.
func TestResetThroughServerAsksForTheRestartBdDoesNotDo(t *testing.T) {
	const url = "git+ssh://git@example.com/org/repo.git"
	t.Setenv("BD_JSON_ENVELOPE", "")
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, tc := range []struct {
		name string
		st   storage.DoltStorage
	}{
		{"a server bd does not manage", &unmanagedResetDataServerStore{}},
		{"a store without ManagedServerDir", &fakeResetDataServerStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refresh := refreshServerAfterReset(tc.st, "origin")
			report := func() error {
				return reportResetData("origin", url, []string{"refs/dolt/data"}, false, refresh)
			}

			jsonOutput = false
			text := captureStdout(t, report)
			if want := "Warning: restart the Dolt server before the next push or pull of remote origin"; !strings.Contains(text, want) {
				t.Errorf("text report lacks %q:\n%s", want, text)
			}
			if strings.Contains(text, "Restarted bd's Dolt server") {
				t.Errorf("text report claims a restart bd did not do:\n%s", text)
			}

			jsonOutput = true
			raw := captureStdout(t, report)
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("--json report is not one JSON object: %v\n%s", err, raw)
			}
			if got["server_restarted"] != false || got["server_restart_required"] != true {
				t.Errorf("server_restarted = %v, server_restart_required = %v, want false and true", got["server_restarted"], got["server_restart_required"])
			}
			if w, _ := got["warning"].(string); !strings.Contains(w, "restart the Dolt server before the next push or pull of remote origin") {
				t.Errorf("warning = %q, want the restart notice naming remote origin", w)
			}
		})
	}
}

// managedResetDataServerStore is a server-mode store whose Dolt server is
// the one bd manages for the workspace, with its state files in dir.
type managedResetDataServerStore struct {
	fakeResetDataServerStore
	dir string
}

func (m *managedResetDataServerStore) ManagedServerDir() (string, bool) { return m.dir, true }

// TestResetThroughServerFailsWhenBdsServerDoesNotRestart pins the report of a
// reset through bd's own Dolt server when restarting the server afterwards
// fails. The remote's data plane was reset, which the report still says, but
// the command fails with the restart's error, in text and in --json
// (server_restart_error), and says what the failure left: a server bd sees
// stopped needs `bd dolt start`, which reads the remote afresh, while one
// that may still run, as when the stop itself failed, may still hold its
// pre-reset view of the remote and needs a restart before the next push or
// pull of it.
func TestResetThroughServerFailsWhenBdsServerDoesNotRestart(t *testing.T) {
	const url = "git+ssh://git@example.com/org/repo.git"
	t.Setenv("BD_JSON_ENVELOPE", "")
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	oldRestart := restartDoltServer
	t.Cleanup(func() { restartDoltServer = oldRestart })
	restartErr := errors.New("restarting Dolt server (server remains stopped; check dolt-server.log): exit status 1")
	restartDoltServer = func(string) (*doltserver.State, error) { return nil, restartErr }

	const stopped = "bd's Dolt server is stopped; start it with `bd dolt start`"
	const mayRun = "restart bd's Dolt server (bd dolt stop, then bd dolt start) before the next push or pull of remote origin: it may still hold its pre-reset view of the remote"
	for _, tc := range []struct {
		name   string
		server func(t *testing.T, dir string)
		want   string
	}{
		{"left stopped", func(*testing.T, string) {}, stopped},
		{"still running", func(t *testing.T, dir string) {
			pid, _ := startStandInDoltServer(t)
			trackServerForTest(t, dir, pid)
		}, mayRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.server(t, dir)
			refresh := refreshServerAfterReset(&managedResetDataServerStore{dir: dir}, "origin")
			var reportErr error
			report := func() {
				reportErr = reportResetData("origin", url, []string{"refs/dolt/data"}, false, refresh)
			}

			jsonOutput = false
			stdout, stderr := captureStdio(t, report)
			if reportErr == nil {
				t.Error("reset-data exits 0 after the restart of bd's Dolt server failed")
			}
			if want := `✓ Remote "origin" data plane reset`; !strings.Contains(stdout, want) {
				t.Errorf("text report lacks %q:\n%s", want, stdout)
			}
			for _, want := range []string{"Error: restarting bd's Dolt server after the reset failed: " + restartErr.Error(), "Hint: " + tc.want + "."} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if all := stdout + stderr; strings.Contains(all, "Restarted bd's Dolt server") || strings.Contains(all, "still holds its pre-reset view") {
				t.Errorf("report claims a restart, or a server state, it does not know:\n%s", all)
			}

			jsonOutput = true
			stdout, _ = captureStdio(t, report)
			if reportErr == nil {
				t.Error("reset-data --json exits 0 after the restart of bd's Dolt server failed")
			}
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("--json report is not one JSON object: %v\n%s", err, stdout)
			}
			if got["server_restarted"] != false || got["server_restart_required"] != true {
				t.Errorf("server_restarted = %v, server_restart_required = %v, want false and true", got["server_restarted"], got["server_restart_required"])
			}
			if got["server_restart_error"] != restartErr.Error() {
				t.Errorf("server_restart_error = %v, want %q", got["server_restart_error"], restartErr.Error())
			}
			if got["warning"] != tc.want {
				t.Errorf("warning = %v, want %q", got["warning"], tc.want)
			}
		})
	}
}

// TestResetThroughServerNamesATemporaryRefItCouldNotDelete pins the failure
// report of the server route: when the fresh-store push fails after writing
// the temporary ref and the ref cannot be deleted, the error names the ref
// and the command that deletes it, so a copy of the store is not left on the
// remote unreported. A push that failed before writing anything leaves no
// ref, and the error says nothing about one.
func TestResetThroughServerNamesATemporaryRefItCouldNotDelete(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only ref directory this test relies on")
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	forEachGitObjectFormat(t, func(t *testing.T, initFlags []string) {
		testResetThroughServerLeftoverRef(t, run, initFlags)
	})
}

// resetDataTestRemote makes a bare git remote holding refs/dolt/data and a
// work repository to push to it from, both in the object format initFlags
// select.
func resetDataTestRemote(t *testing.T, run func(t *testing.T, dir string, args ...string) string, initFlags []string) (remote, work string) {
	t.Helper()
	base := t.TempDir()
	remote = filepath.Join(base, "remote.git")
	run(t, base, append([]string{"init", "-q", "--bare"}, append(initFlags, remote)...)...)
	work = filepath.Join(base, "work")
	run(t, base, append([]string{"init", "-q"}, append(initFlags, work)...)...)
	run(t, work, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "0")
	run(t, work, "push", "-q", remote, "HEAD:refs/dolt/data")
	return remote, work
}

func testResetThroughServerLeftoverRef(t *testing.T, run func(t *testing.T, dir string, args ...string) string, initFlags []string) {
	setup := func(t *testing.T) (remote, work string) {
		return resetDataTestRemote(t, run, initFlags)
	}
	ctx := context.Background()
	pushErr := errors.New("push failed")

	t.Run("a written ref that cannot be deleted is named", func(t *testing.T) {
		remote, work := setup(t)
		refsDir := filepath.Join(remote, "refs", "dolt")
		st := &fakeResetDataServerStore{push: func(name string) error {
			run(t, work, "push", "-q", remote, "HEAD:refs/dolt/"+name)
			// The ref stays: its directory takes no deletion.
			if err := os.Chmod(refsDir, 0o555); err != nil {
				t.Fatal(err)
			}
			return pushErr
		}}
		t.Cleanup(func() { _ = os.Chmod(refsDir, 0o755) })
		_, err := resetGitBackedThroughServer(ctx, st, "origin", "git+file://"+remote, "")
		if !errors.Is(err, pushErr) {
			t.Fatalf("err = %v, want the push failure", err)
		}
		tmpRef := ""
		for _, ref := range strings.Fields(run(t, remote, "for-each-ref", "--format=%(refname)", "refs/dolt/")) {
			if strings.HasPrefix(ref, "refs/dolt/bd-reset-data-") {
				tmpRef = ref
			}
		}
		if tmpRef == "" {
			t.Fatal("precondition: the temporary ref was deleted")
		}
		for _, want := range []string{"the remote's Dolt data is unchanged", "the temporary ref " + tmpRef, "git push file://" + remote + " :" + tmpRef + " in a clone of that remote"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q:\n%v", want, err)
			}
		}
	})

	t.Run("a push that wrote nothing reports no leftover", func(t *testing.T) {
		remote, _ := setup(t)
		st := &fakeResetDataServerStore{push: func(string) error { return pushErr }}
		_, err := resetGitBackedThroughServer(ctx, st, "origin", "git+file://"+remote, "")
		if !errors.Is(err, pushErr) {
			t.Fatalf("err = %v, want the push failure", err)
		}
		if strings.Contains(err.Error(), "temporary ref") {
			t.Errorf("error names a temporary ref the push never wrote:\n%v", err)
		}
	})
}

// TestResetThroughServerReplacesTheDataRef pins the whole git side of the
// server route of reset-data on SHA-1 and SHA-256 remotes alike (Dolt
// supports both): the data ref ends at the commit the fresh-store push wrote
// to the temporary ref, and the temporary ref is gone. Scratch repositories
// of the wrong object format can neither fetch from the remote nor push to
// it.
func TestResetThroughServerReplacesTheDataRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	forEachGitObjectFormat(t, func(t *testing.T, initFlags []string) {
		remote, work := resetDataTestRemote(t, run, initFlags)
		fresh := ""
		st := &fakeResetDataServerStore{push: func(name string) error {
			run(t, work, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "fresh")
			run(t, work, "push", "-q", remote, "HEAD:refs/dolt/"+name)
			fresh = run(t, work, "rev-parse", "HEAD")
			return nil
		}}
		deleted, err := resetGitBackedThroughServer(context.Background(), st, "origin", "git+file://"+remote, "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(deleted, []string{"refs/dolt/data"}) {
			t.Errorf("replaced refs = %v, want [refs/dolt/data]", deleted)
		}
		if got := run(t, remote, "rev-parse", "refs/dolt/data"); got != fresh {
			t.Errorf("refs/dolt/data = %s, want the fresh store %s", got, fresh)
		}
		if refs := run(t, remote, "for-each-ref", "--format=%(refname)", "refs/dolt/"); strings.Contains(refs, "bd-reset-data-") {
			t.Errorf("temporary ref left on the remote: %q", refs)
		}
	})
}

// TestResetThroughServerReadsTheDataRefBackAfterAFailedMove pins the server
// route of reset-data when git reports that moving the data ref failed. git
// can fail a push the remote applied (Ctrl-C, or a connection lost after the
// remote took the update), so the data ref's value, read back, decides. A
// move that landed is a reset: bd restarts its own server, which would
// otherwise keep its pre-reset view of the remote. A move that did not land
// leaves the server alone and says why: the data is unchanged, or another
// client changed the ref after reset-data read it. A data ref that cannot be
// read back leaves the outcome unknown: the command fails without calling the
// data unchanged, and still restarts bd's server, or asks for the restart of
// a server bd does not manage.
func TestResetThroughServerReadsTheDataRefBackAfterAFailedMove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(t *testing.T, work, msg string) string {
		t.Helper()
		run(t, work, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", msg)
		return run(t, work, "rev-parse", "HEAD")
	}
	oldJSON, oldRestart, oldMove := jsonOutput, restartDoltServer, moveDataRef
	t.Cleanup(func() { jsonOutput, restartDoltServer, moveDataRef = oldJSON, oldRestart, oldMove })
	jsonOutput = false
	moveErr := errors.New("fatal: the remote end hung up unexpectedly")
	restartErr := errors.New("restarting Dolt server (server remains stopped; check dolt-server.log): exit status 1")

	// How the move goes: it lands and git then fails (landed), git fails
	// before it (dropped), another client moves the data ref first, so that
	// the lease refuses it (raced), or it lands and the remote then cannot be
	// read (lost).
	const (
		landed = iota
		dropped
		raced
		lost
	)
	const unknown = "whether the remote holds the fresh store is unknown"
	for _, tc := range []struct {
		name       string
		move       int
		unmanaged  bool
		restartErr error
		// wantRef names the commit the data ref ends at: the fresh
		// store's, the one reset-data read first, or another client's.
		wantRef     string
		wantErr     bool
		wantRestart bool
		want        []string
	}{
		{
			name: "a move that landed is a reset, and bd restarts its server", move: landed,
			wantRef: "fresh", wantRestart: true,
			want: []string{"Warning: git reported an error moving refs/dolt/data, but the remote holds the fresh store: " + moveErr.Error(), `✓ Remote "origin" data plane reset`, "Restarted bd's Dolt server"},
		},
		{
			name: "a move that did not land leaves the data unchanged", move: dropped,
			wantRef: "before", wantErr: true,
			want: []string{"replacing refs/dolt/data with the fresh store failed; the remote's Dolt data is unchanged"},
		},
		{
			name: "a move the lease refused names the other client's change", move: raced,
			wantRef: "other", wantErr: true,
			want: []string{"another client changed it since reset-data read it, and the reset did not replace it"},
		},
		{
			name: "a ref that cannot be read back is an unknown outcome, and bd restarts its server", move: lost,
			wantRef: "fresh", wantErr: true, wantRestart: true,
			want: []string{unknown, "the temporary ref refs/dolt/bd-reset-data-", "Hint: bd restarted its Dolt server, so that it rereads the remote"},
		},
		{
			name: "an unknown outcome asks for the restart of a server bd does not manage", move: lost, unmanaged: true,
			wantRef: "fresh", wantErr: true,
			want: []string{unknown, "Hint: restart the Dolt server before the next push or pull of remote origin: it may still hold its pre-reset view of the remote."},
		},
		{
			name: "an unknown outcome names a restart that failed", move: lost, restartErr: restartErr,
			wantRef: "fresh", wantErr: true, wantRestart: true,
			want: []string{unknown, "restarting bd's Dolt server failed too: " + restartErr.Error(), "Hint: bd's Dolt server is stopped; start it with `bd dolt start`."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote, work := resetDataTestRemote(t, run, nil)
			refs := map[string]string{"before": run(t, remote, "rev-parse", "refs/dolt/data")}
			fake := fakeResetDataServerStore{push: func(name string) error {
				refs["fresh"] = commit(t, work, "fresh")
				run(t, work, "push", "-q", remote, "HEAD:refs/dolt/"+name)
				return nil
			}}
			var st storage.DoltStorage = &managedResetDataServerStore{fakeResetDataServerStore: fake, dir: t.TempDir()}
			if tc.unmanaged {
				st = &unmanagedResetDataServerStore{fakeResetDataServerStore: fake}
			}
			restarts := 0
			restartDoltServer = func(string) (*doltserver.State, error) {
				restarts++
				return nil, tc.restartErr
			}
			away := remote + ".away"
			moveDataRef = func(ctx context.Context, gitURL, src, srcOID, ref, before string) error {
				switch tc.move {
				case dropped:
					return moveErr
				case raced:
					refs["other"] = commit(t, work, "other")
					run(t, work, "push", "-q", remote, "HEAD:refs/dolt/data")
					return moveGitRef(ctx, gitURL, src, srcOID, ref, before)
				}
				if err := moveGitRef(ctx, gitURL, src, srcOID, ref, before); err != nil {
					t.Fatalf("precondition: the move failed: %v", err)
				}
				if tc.move == lost {
					if err := os.Rename(remote, away); err != nil {
						t.Fatal(err)
					}
				}
				return moveErr
			}

			var err error
			stdout, stderr := captureStdio(t, func() {
				err = resetDataThroughServer(context.Background(), st, "origin", "git+file://"+remote, "")
			})
			if tc.move == lost {
				if rerr := os.Rename(away, remote); rerr != nil {
					t.Fatal(rerr)
				}
			}
			out := stdout + stderr
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want an error: %t\n%s", err, tc.wantErr, out)
			}
			if (restarts > 0) != tc.wantRestart {
				t.Errorf("bd restarted its Dolt server %d times, want a restart: %t\n%s", restarts, tc.wantRestart, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			if tc.move != dropped && strings.Contains(out, "unchanged") {
				t.Errorf("output says the remote's data is unchanged:\n%s", out)
			}
			if got := run(t, remote, "rev-parse", "refs/dolt/data"); got != refs[tc.wantRef] {
				t.Errorf("refs/dolt/data = %s, want the %s commit %s", got, tc.wantRef, refs[tc.wantRef])
			}
			if left := run(t, remote, "for-each-ref", "--format=%(refname)", "refs/dolt/"); tc.move != lost && strings.Contains(left, "bd-reset-data-") {
				t.Errorf("temporary ref left on the remote: %q", left)
			}
		})
	}
}

// forEachGitObjectFormat runs fn once for SHA-1 git repositories and once for
// SHA-256 ones, passing the git init flags that select the format. The
// SHA-256 leg is skipped where git lacks --object-format (before 2.29).
func forEachGitObjectFormat(t *testing.T, fn func(t *testing.T, initFlags []string)) {
	t.Helper()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			var initFlags []string
			if format == "sha256" {
				initFlags = []string{"--object-format=sha256"}
				probe := filepath.Join(t.TempDir(), "probe.git")
				if out, err := exec.Command("git", "init", "-q", "--bare", "--object-format=sha256", probe).CombinedOutput(); err != nil {
					t.Skipf("git cannot create SHA-256 repositories (needs git 2.29 or later): %v: %s", err, strings.TrimSpace(string(out)))
				}
			}
			fn(t, initFlags)
		})
	}
}
