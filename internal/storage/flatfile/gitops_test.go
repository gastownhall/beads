package flatfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// gitRun runs git in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// gitTry runs git in dir and returns combined output and the error.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// newGitStore creates a git repo on the given branch with an initial commit
// containing one issue file under .beads/issues/, and returns a store rooted
// in it plus the repo dir.
func newGitStore(t *testing.T, branch string) (*FlatFileStore, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", branch)
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "test")
	gitRun(t, dir, "config", "commit.gpgsign", "false")

	issuesDir := filepath.Join(dir, ".beads", "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIssueFile(t, dir, "test-1", "initial")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")

	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	return s, dir
}

// writeIssueFile writes a minimal issue JSON with the given title.
func writeIssueFile(t *testing.T, repoDir, id, title string) {
	t.Helper()
	path := filepath.Join(repoDir, ".beads", "issues", id+".json")
	content := `{"id":"` + id + `","title":"` + title + `","status":"open","priority":2,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeConflictedMerge drives the repo into a merge with a conflict on the
// test-1 issue file. Returns the name of the side branch.
func makeConflictedMerge(t *testing.T, dir, branch string) {
	t.Helper()
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeIssueFile(t, dir, "test-1", "side edit")
	gitRun(t, dir, "commit", "-q", "-am", "side edit")
	gitRun(t, dir, "checkout", "-q", branch)
	writeIssueFile(t, dir, "test-1", "local edit")
	gitRun(t, dir, "commit", "-q", "-am", "local edit")
	if _, err := gitTry(dir, "merge", "side"); err == nil {
		t.Fatal("expected merge conflict, merge succeeded")
	}
	if out := gitRun(t, dir, "diff", "--name-only", "--diff-filter=U"); out == "" {
		t.Fatal("expected unmerged paths after conflicted merge")
	}
}

// TASKS-pqek: a url beginning with '-' would be parsed by git as an option;
// AddRemote must reject it before invoking git.
func TestAddRemoteRejectsFlagURL(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")

	if err := s.AddRemote(ctx, "origin", "--mirror=fetch"); err == nil {
		t.Fatal("AddRemote accepted a flag-shaped url")
	}
	if out := gitRun(t, dir, "remote"); out != "" {
		t.Fatalf("remote was configured despite rejection: %q", out)
	}
}

// TASKS-m49g: git failures (here: not a git repository at all) must surface
// as errors, not collapse into "empty history / clean status / no remotes".
func TestReadPathsPropagateGitFailures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir() // deliberately NOT a git repo
	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}

	if _, err := s.Log(ctx, 5); err == nil {
		t.Error("Log in non-repo: want error, got nil")
	}
	if _, err := s.Status(ctx); err == nil {
		t.Error("Status in non-repo: want error, got nil")
	}
	if _, err := s.History(ctx, "test-1"); err == nil {
		t.Error("History in non-repo: want error, got nil")
	}
	if _, err := s.Diff(ctx, "HEAD~1", "HEAD"); err == nil {
		t.Error("Diff in non-repo: want error, got nil")
	}
	if _, err := s.ListRemotes(ctx); err == nil {
		t.Error("ListRemotes in non-repo: want error, got nil")
	}
	if _, err := s.GetConflicts(ctx); err == nil {
		t.Error("GetConflicts in non-repo: want error, got nil")
	}
	if _, err := s.CommitExists(ctx, "deadbeef"); err == nil {
		t.Error("CommitExists in non-repo: want error, got nil")
	}
	// TASKS-muo9: AsOf must not collapse "not a git repository" into
	// ErrNotFound — the caller would conclude the issue never existed.
	if _, err := s.AsOf(ctx, "test-1", "HEAD"); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AsOf in non-repo = %v, want non-ErrNotFound error", err)
	}
}

// TASKS-m49g: the one benign case — a repo with no commits yet — still reads
// as empty history, and a missing object is a clean false, not an error.
func TestBenignGitCasesStayQuiet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}

	if commits, err := s.Log(ctx, 5); err != nil || commits != nil {
		t.Errorf("Log on empty repo = (%v, %v), want (nil, nil)", commits, err)
	}
	if entries, err := s.History(ctx, "test-1"); err != nil || entries != nil {
		t.Errorf("History on empty repo = (%v, %v), want (nil, nil)", entries, err)
	}

	s2, dir2 := newGitStore(t, "main")
	head := gitRun(t, dir2, "rev-parse", "HEAD")
	if ok, err := s2.CommitExists(ctx, head); err != nil || !ok {
		t.Errorf("CommitExists(HEAD) = (%v, %v), want (true, nil)", ok, err)
	}
	for _, missing := range []string{"deadbeef", "0123456789012345678901234567890123456789"} {
		if ok, err := s2.CommitExists(ctx, missing); err != nil || ok {
			t.Errorf("CommitExists(%s) = (%v, %v), want (false, nil)", missing, ok, err)
		}
	}

	// TASKS-muo9: the benign AsOf cases stay ErrNotFound — issue absent at a
	// valid ref (tracked or on disk only) and a ref that does not resolve.
	if _, err := s2.AsOf(ctx, "test-9", "HEAD"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AsOf(missing issue) = %v, want ErrNotFound", err)
	}
	writeIssueFile(t, dir2, "test-9", "on disk only")
	if _, err := s2.AsOf(ctx, "test-9", "HEAD"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AsOf(untracked issue) = %v, want ErrNotFound", err)
	}
	if _, err := s2.AsOf(ctx, "test-1", "no-such-ref"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AsOf(bad ref) = %v, want ErrNotFound", err)
	}
	// And the existing issue still loads at HEAD.
	if issue, err := s2.AsOf(ctx, "test-1", "HEAD"); err != nil || issue == nil || issue.ID != "test-1" {
		t.Errorf("AsOf(test-1, HEAD) = (%v, %v), want issue", issue, err)
	}
}

// TASKS-m49g: an unfetched peer yields the Dolt-parity partial result
// (-1/-1 "unknown"), never a false 0/0 "in sync".
func TestSyncStatusUnfetchedPeer(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")
	gitRun(t, dir, "remote", "add", "peer", filepath.Join(t.TempDir(), "nowhere.git"))

	st, err := s.SyncStatus(ctx, "peer")
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if st.LocalAhead != -1 || st.LocalBehind != -1 {
		t.Fatalf("SyncStatus ahead/behind = %d/%d, want -1/-1 sentinel", st.LocalAhead, st.LocalBehind)
	}
}

// TASKS-rkqg: CommitPending must commit only .beads/, never sweep the user's
// own staged files into a bd auto-commit.
func TestCommitPendingScopedToBeads(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")

	// User stages their own file; bd has a pending issue change.
	if err := os.WriteFile(filepath.Join(dir, "app.c"), []byte("int main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "app.c")
	writeIssueFile(t, dir, "test-1", "pending edit")

	committed, err := s.CommitPending(ctx, "")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatal("expected CommitPending to commit the pending .beads change")
	}
	// Oracle: git — the auto-commit contains only .beads paths, and app.c is
	// still staged for the user's own commit.
	files := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, "app.c") {
		t.Fatalf("auto-commit swept user file: %q", files)
	}
	if !strings.Contains(files, ".beads/issues/test-1.json") {
		t.Fatalf("auto-commit missing bd change: %q", files)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(staged, "app.c") {
		t.Fatalf("user's staged file lost from index: %q", staged)
	}
}

// TASKS-pot8: a failed 'git add' must propagate from Commit/CommitPending.
// The pathspec commit exits 0 with only the tracked files, so a swallowed add
// error reports success while a new issue file is silently dropped from the
// commit. Oracle: git — an unreadable new file makes add fail (exit 128)
// while the pathspec commit of the tracked edit would still succeed.
func TestCommitPropagatesAddFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	ctx := context.Background()
	s, dir := newGitStore(t, "main")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	// Tracked issue modified; new issue file unreadable so 'git add' fails.
	writeIssueFile(t, dir, "test-1", "tracked edit")
	writeIssueFile(t, dir, "test-2", "new issue")
	newPath := filepath.Join(dir, ".beads", "issues", "test-2.json")
	if err := os.Chmod(newPath, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(newPath, 0o644)

	if err := s.Commit(ctx, "msg"); err == nil {
		t.Error("Commit: want add failure propagated, got nil")
	}
	if committed, err := s.CommitPending(ctx, ""); err == nil || committed {
		t.Errorf("CommitPending = (%v, %v), want (false, add failure)", committed, err)
	}
	// No half-commit missing the new issue may have been created.
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("a commit was created despite add failure: %s", gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"))
	}
}

// newPeerClone creates a bare peer repo seeded from repoDir (registered there
// as remote "peer") plus a working clone of it, and returns the clone dir.
func newPeerClone(t *testing.T, repoDir, branch string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "peer.git")
	gitRun(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", branch, bare)
	gitRun(t, repoDir, "remote", "add", "peer", bare)
	gitRun(t, repoDir, "push", "-q", "peer", branch)

	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	gitRun(t, clone, "config", "user.email", "peer@example.com")
	gitRun(t, clone, "config", "user.name", "peer")
	gitRun(t, clone, "config", "commit.gpgsign", "false")
	return clone
}

// TASKS-vl2x: Push/PushTo must not stage .beads/ as a side effect — push
// publishes commits, and a stealth 'git add' pollutes the user's index.
func TestPushDoesNotTouchIndex(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")
	newPeerClone(t, dir, "main")
	gitRun(t, dir, "branch", "--set-upstream-to=peer/main", "main")

	// Uncommitted bd change sitting in the worktree.
	writeIssueFile(t, dir, "test-1", "uncommitted edit")

	if err := s.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("Push staged files as a side effect: %q", staged)
	}
	if err := s.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("PushTo: %v", err)
	}
	if err := s.ForcePush(ctx); err != nil {
		t.Fatalf("ForcePush: %v", err)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("push staged files as a side effect: %q", staged)
	}
}

// TASKS-7u7b: a conflicted Sync must leave the unmerged index entries intact
// (no "git add" sweep) so ResolveConflicts still has stage-2/3 entries to work
// with, and no conflict-markered JSON is ever staged as resolved.
func TestSyncConflictPreservesUnmergedPaths(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")
	clone := newPeerClone(t, dir, "main")

	// Diverge: peer edits test-1 one way, local another.
	writeIssueFile(t, clone, "test-1", "peer edit")
	gitRun(t, clone, "commit", "-q", "-am", "peer edit")
	gitRun(t, clone, "push", "-q", "origin", "main")
	writeIssueFile(t, dir, "test-1", "local edit")
	gitRun(t, dir, "commit", "-q", "-am", "local edit")

	res, err := s.Sync(ctx, "peer", "")
	if err == nil {
		t.Fatal("expected Sync to fail with merge conflict")
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].IssueID != "test-1" {
		t.Fatalf("expected conflict on test-1, got %+v", res.Conflicts)
	}
	// Oracle: git's own unmerged-path listing. If Sync had staged .beads/,
	// this would be empty and resolution would silently no-op.
	if out := gitRun(t, dir, "diff", "--name-only", "--diff-filter=U"); !strings.Contains(out, "test-1.json") {
		t.Fatalf("unmerged entry for test-1.json lost after Sync; diff-filter=U = %q", out)
	}
	if err := s.ResolveConflicts(ctx, "", "ours"); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if err := s.CommitMergeResolution(ctx, "resolve"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "local edit") || strings.Contains(string(data), "<<<<<<<") {
		t.Fatalf("ours-resolution did not produce local content: %q", string(data))
	}
}

// TASKS-34om: ResolveConflicts must handle modify/delete conflicts where the
// CHOSEN side deleted the file (routine under delete cascades: issue deleted
// on one branch, updated on the other). A blanket 'git checkout
// --ours/--theirs .beads/' errors "does not have our/their version" exactly
// then. Mixed merge: test-1 is modify/delete, test-2 is a content conflict —
// both must resolve in one call and the merge must conclude.
func TestResolveConflictsModifyDelete(t *testing.T) {
	ctx := context.Background()

	setup := func(t *testing.T, deleteOn string) (*FlatFileStore, string) {
		s, dir := newGitStore(t, "main")
		writeIssueFile(t, dir, "test-2", "initial 2")
		gitRun(t, dir, "add", ".beads/issues/test-2.json")
		gitRun(t, dir, "commit", "-q", "-m", "add test-2")

		gitRun(t, dir, "checkout", "-q", "-b", "side")
		if deleteOn == "side" {
			gitRun(t, dir, "rm", "-q", ".beads/issues/test-1.json")
		} else {
			writeIssueFile(t, dir, "test-1", "side edit")
		}
		writeIssueFile(t, dir, "test-2", "side edit 2")
		gitRun(t, dir, "commit", "-q", "-am", "side")

		gitRun(t, dir, "checkout", "-q", "main")
		if deleteOn == "main" {
			gitRun(t, dir, "rm", "-q", ".beads/issues/test-1.json")
		} else {
			writeIssueFile(t, dir, "test-1", "main edit")
		}
		writeIssueFile(t, dir, "test-2", "main edit 2")
		gitRun(t, dir, "commit", "-q", "-am", "main")

		if _, err := gitTry(dir, "merge", "side"); err == nil {
			t.Fatal("expected merge conflict")
		}
		return s, dir
	}

	// Direction 1: THEIRS deleted test-1, strategy "theirs" keeps the delete.
	t.Run("theirs-deleted", func(t *testing.T) {
		s, dir := setup(t, "side")
		if err := s.ResolveConflicts(ctx, "", "theirs"); err != nil {
			t.Fatalf("ResolveConflicts(theirs): %v", err)
		}
		if err := s.CommitMergeResolution(ctx, "resolve"); err != nil {
			t.Fatalf("CommitMergeResolution: %v", err)
		}
		if _, err := gitTry(dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
			t.Fatal("MERGE_HEAD still present: merge was not concluded")
		}
		if _, err := os.Stat(filepath.Join(dir, ".beads", "issues", "test-1.json")); !os.IsNotExist(err) {
			t.Errorf("test-1.json should be deleted (theirs), stat err = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-2.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "side edit 2") || strings.Contains(string(data), "<<<<<<<") {
			t.Errorf("content conflict not resolved to theirs: %q", string(data))
		}
	})

	// Direction 2: OURS deleted test-1, strategy "ours" keeps the delete.
	t.Run("ours-deleted", func(t *testing.T) {
		s, dir := setup(t, "main")
		if err := s.ResolveConflicts(ctx, "", "ours"); err != nil {
			t.Fatalf("ResolveConflicts(ours): %v", err)
		}
		if err := s.CommitMergeResolution(ctx, "resolve"); err != nil {
			t.Fatalf("CommitMergeResolution: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".beads", "issues", "test-1.json")); !os.IsNotExist(err) {
			t.Errorf("test-1.json should be deleted (ours), stat err = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-2.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "main edit 2") || strings.Contains(string(data), "<<<<<<<") {
			t.Errorf("content conflict not resolved to ours: %q", string(data))
		}
	})

	// Surviving side chosen: modify/delete resolved toward the modification.
	t.Run("surviving-side-chosen", func(t *testing.T) {
		s, dir := setup(t, "side")
		if err := s.ResolveConflicts(ctx, "", "ours"); err != nil {
			t.Fatalf("ResolveConflicts(ours): %v", err)
		}
		if err := s.CommitMergeResolution(ctx, "resolve"); err != nil {
			t.Fatalf("CommitMergeResolution: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-1.json"))
		if err != nil {
			t.Fatalf("test-1.json should survive (ours modified): %v", err)
		}
		if !strings.Contains(string(data), "main edit") {
			t.Errorf("test-1 not resolved to ours: %q", string(data))
		}
	})
}

// TASKS-i39o: Sync and SyncStatus must track the current branch (Dolt uses
// peer/s.branch), not a hardcoded "main". Repo here uses "trunk".
func TestSyncUsesCurrentBranch(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "trunk")
	clone := newPeerClone(t, dir, "trunk")

	// Peer adds an issue; local adds a different one (diverged, no conflict).
	writeIssueFile(t, clone, "test-2", "from peer")
	gitRun(t, clone, "add", ".beads/issues/test-2.json")
	gitRun(t, clone, "commit", "-q", "-m", "peer adds test-2")
	gitRun(t, clone, "push", "-q", "origin", "trunk")
	writeIssueFile(t, dir, "test-3", "local only")
	gitRun(t, dir, "add", ".beads/issues/test-3.json")
	gitRun(t, dir, "commit", "-q", "-m", "local adds test-3")

	gitRun(t, dir, "fetch", "-q", "peer")
	st, err := s.SyncStatus(ctx, "peer")
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if st.LocalAhead != 1 || st.LocalBehind != 1 {
		t.Fatalf("SyncStatus ahead/behind = %d/%d, want 1/1 (branch trunk not compared?)", st.LocalAhead, st.LocalBehind)
	}

	res, err := s.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("Sync on branch trunk: %v", err)
	}
	if !res.Merged {
		t.Fatal("Sync did not merge peer/trunk")
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads", "issues", "test-2.json")); err != nil {
		t.Fatalf("peer issue not merged in: %v", err)
	}
}

// TASKS-8wtk: working-tree-mutating git ops must be excluded from an open
// transaction (txMu/writeMu), or the transaction's rollback can restore a
// pre-merge journal image over the just-merged state — or the merge aborts on
// the transaction's uncommitted write ("local changes would be overwritten").
// Mirrors TestRollbackDoesNotRevertConcurrentNonTxWrite with Merge as the
// concurrent mutator.
func TestMergeExcludedFromOpenTransaction(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")

	// Side branch is strictly ahead: test-1 edited to "merged edit".
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeIssueFile(t, dir, "test-1", "merged edit")
	gitRun(t, dir, "commit", "-q", "-am", "side edit")
	gitRun(t, dir, "checkout", "-q", "main")

	txStarted := make(chan struct{})
	mergeDone := make(chan error, 1)
	go func() {
		<-txStarted
		_, err := s.Merge(ctx, "side")
		mergeDone <- err
	}()

	sentinel := errors.New("boom")
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		// Journal test-1's pre-image ("initial") inside the transaction.
		if err := tx.UpdateIssue(ctx, "test-1", map[string]interface{}{"title": "tx edit"}, "tester"); err != nil {
			return err
		}
		close(txStarted)
		// Pre-fix the unlocked Merge runs here: it either aborts on the
		// transaction's dirty test-1 or fast-forwards it, only for the
		// rollback below to restore the pre-merge image.
		select {
		case err := <-mergeDone:
			mergeDone <- err
			t.Error("Merge completed inside an open transaction")
		case <-time.After(100 * time.Millisecond):
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}
	if err := <-mergeDone; err != nil {
		t.Fatalf("concurrent Merge: %v", err)
	}

	// The merged state must survive the rollback, and the rollback must not
	// leave the tree dirty against the merged HEAD.
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "merged edit") {
		t.Errorf("merged state reverted by foreign rollback: %q", string(data))
	}
	if out := gitRun(t, dir, "status", "--porcelain", ".beads/"); out != "" {
		t.Errorf("tree dirty after rollback + merge: %q", out)
	}
}

// TASKS-vun8: a pathspec-limited commit is forbidden during a merge; the
// resolution commit must be a plain commit that concludes the merge
// (oracle: git itself — MERGE_HEAD must be gone afterwards).
func TestCommitMergeResolutionConcludesMerge(t *testing.T) {
	ctx := context.Background()
	s, dir := newGitStore(t, "main")
	makeConflictedMerge(t, dir, "main")

	if err := s.ResolveConflicts(ctx, "", "theirs"); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if err := s.CommitMergeResolution(ctx, "resolve"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}
	if _, err := gitTry(dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		t.Fatal("MERGE_HEAD still present: merge was not concluded")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "issues", "test-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "<<<<<<<") {
		t.Fatal("conflict markers persisted in issue file")
	}
}

// scrubGitIdentityEnv removes every ambient source of git identity for the
// test's child processes: global/system config and the identity env vars.
func scrubGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, v := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
	} {
		t.Setenv(v, "") // register restore
		os.Unsetenv(v)
	}
}

// BEADS-05ne: on a machine with no git identity (fresh install, CI runner),
// auto-commits must fall back to a synthetic identity instead of failing with
// "empty ident name" — which broke every bd write on such machines.
func TestCommitFallsBackToSyntheticIdentity(t *testing.T) {
	ctx := context.Background()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	scrubGitIdentityEnv(t)

	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	issuesDir := filepath.Join(dir, ".beads", "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIssueFile(t, dir, "test-1", "initial")

	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	if err := s.Commit(ctx, "bd: test auto-commit"); err != nil {
		t.Fatalf("Commit without git identity: %v", err)
	}
	// Oracle: git itself — the commit exists and carries the fallback author.
	author := strings.TrimSpace(gitRun(t, dir, "log", "-1", "--format=%an <%ae>"))
	if author != "beads <beads@localhost>" {
		t.Errorf("fallback author = %q, want %q", author, "beads <beads@localhost>")
	}
}

// BEADS-05ne: a real configured identity must always win — the fallback may
// only engage when identity resolution fails, never override the user's own.
func TestCommitKeepsConfiguredIdentity(t *testing.T) {
	ctx := context.Background()
	scrubGitIdentityEnv(t)

	// newGitStore sets repo-local user.name/user.email ("test").
	s, dir := newGitStore(t, "main")
	writeIssueFile(t, dir, "test-1", "identity edit")
	if err := s.Commit(ctx, "bd: identity check"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	author := strings.TrimSpace(gitRun(t, dir, "log", "-1", "--format=%an <%ae>"))
	if author != "test <test@example.com>" {
		t.Errorf("author = %q, want configured identity %q", author, "test <test@example.com>")
	}
}

// BEADS-ss0h: a flat-file workspace outside any git repo is a valid store —
// the JSON files are the data, git is optional versioning. Auto-commit
// (CommitPending) must no-op there instead of failing every mutating command
// with "fatal: not a git repository" (broke the MCP/npm package gates, which
// run bd in plain temp dirs).
func TestCommitPendingNoOpOutsideGitRepo(t *testing.T) {
	ctx := context.Background()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir() // deliberately NOT a git repo
	issuesDir := filepath.Join(dir, ".beads", "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIssueFile(t, dir, "test-1", "unversioned")

	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	committed, err := s.CommitPending(ctx, "")
	if err != nil {
		t.Fatalf("CommitPending outside git repo: %v", err)
	}
	if committed {
		t.Error("CommitPending reported a commit with no git repo present")
	}
	// Commit is the auto-commit funnel (maybeAutoCommitStore routes every
	// mutating command through it with a message), so it must no-op too.
	if err := s.Commit(ctx, "bd: auto-commit"); err != nil {
		t.Errorf("Commit outside a git repo: %v; want no-op", err)
	}
}

// BEADS-ss0h: an idempotent write leaves the git tree clean; the follow-up
// auto-commit must no-op (Dolt's nothing-to-commit semantics), not exit 1 —
// git prints "nothing to commit" on STDOUT where the stderr-only error
// wrapper cannot see it, so this failed whole commands (e.g. re-adding an
// existing dependency edge).
func TestCommitNoOpOnCleanTree(t *testing.T) {
	ctx := context.Background()
	s, _ := newGitStore(t, "main")
	if err := s.Commit(ctx, "bd: nothing changed"); err != nil {
		t.Fatalf("Commit on clean tree: %v; want no-op", err)
	}
}
