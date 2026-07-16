package flatfile

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// txForTest opens a transaction and hands it to fn, ignoring the commit step.
func txForTest(t *testing.T, s *FlatFileStore, fn func(tx storage.Transaction)) {
	t.Helper()
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		fn(tx)
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
}

// storage.RunInTransaction contract (storage.go): "If any operation returns
// an error, the transaction is rolled back" — operations "must either all
// succeed or all fail". SQL backends get this from sql.Tx; flat-file must
// restore pre-images. Every mutation class a transaction can make is
// exercised: update, create, delete, comment, label, dependency, metadata.
func TestRunInTransactionRollsBackOnError(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []string{"test-1", "test-2"} {
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "before-" + id}, "t"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	preEvents, err := s.GetEvents(ctx, "test-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}

	sentinel := errors.New("boom")
	err = s.RunInTransaction(ctx, "test", func(tx storage.Transaction) error {
		if err := tx.UpdateIssue(ctx, "test-1", map[string]interface{}{"title": "after"}, "t"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, &types.Issue{ID: "test-3", Title: "new in tx"}, "t"); err != nil {
			return err
		}
		if err := tx.DeleteIssue(ctx, "test-2"); err != nil {
			return err
		}
		if err := tx.AddComment(ctx, "test-1", "t", "tx comment"); err != nil {
			return err
		}
		if err := tx.AddLabel(ctx, "test-1", "tx-label", "t"); err != nil {
			return err
		}
		if err := tx.SetMetadata(ctx, "tx-key", "tx-value"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}

	got, err := s.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetIssue(test-1): %v", err)
	}
	if got.Title != "before-test-1" {
		t.Errorf("test-1 title = %q, want %q (update not rolled back)", got.Title, "before-test-1")
	}
	if len(got.Labels) != 0 {
		t.Errorf("test-1 labels = %v, want none (label add not rolled back)", got.Labels)
	}
	if _, err := s.GetIssue(ctx, "test-3"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetIssue(test-3) err = %v, want ErrNotFound (create not rolled back)", err)
	}
	if _, err := s.GetIssue(ctx, "test-2"); err != nil {
		t.Errorf("GetIssue(test-2) err = %v, want restored (delete not rolled back)", err)
	}
	comments, err := s.GetIssueComments(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("test-1 comments = %d, want 0 (comment not rolled back)", len(comments))
	}
	if v, err := s.GetMetadata(ctx, "tx-key"); err == nil && v != "" {
		t.Errorf("metadata tx-key = %q, want unset (metadata not rolled back)", v)
	}
	postEvents, err := s.GetEvents(ctx, "test-1", 0)
	if err != nil {
		t.Fatalf("GetEvents after rollback: %v", err)
	}
	if len(postEvents) != len(preEvents) {
		t.Errorf("test-1 events = %d, want %d (event appends not rolled back)", len(postEvents), len(preEvents))
	}
}

// A successful transaction keeps its writes, and writes outside any
// transaction are untouched by the journal machinery.
func TestRunInTransactionSuccessPersists(t *testing.T) {
	s := newTestStore(t)
	err := s.RunInTransaction(ctx, "test", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{ID: "test-1", Title: "kept"}, "t")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	got, err := s.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "kept" {
		t.Errorf("title = %q, want %q", got.Title, "kept")
	}
}

// A successful transaction with a commit message must create a git version
// commit (the flat-file analog of embeddeddolt's StageAndCommit): cmd/bd's
// transact() marks the command as explicitly committed, so nothing else will
// commit the writes. An empty message creates no commit.
func TestRunInTransactionCommitsWithMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "bd-test@example.com")
	git("config", "user.name", "bd test")
	git("config", "commit.gpgsign", "false")

	s, err := NewFlatFileStore(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const msg = "bd: tx commit test"
	err = s.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{ID: "test-1", Title: "in tx"}, "t")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if got := git("log", "-1", "--format=%s"); got != msg {
		t.Errorf("last commit subject = %q, want %q", got, msg)
	}
	if status := git("status", "--porcelain", ".beads/"); status != "" {
		t.Errorf(".beads/ not clean after transactional commit:\n%s", status)
	}

	before := git("rev-parse", "HEAD")
	err = s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{ID: "test-2", Title: "no msg"}, "t")
	})
	if err != nil {
		t.Fatalf("RunInTransaction(no msg): %v", err)
	}
	if after := git("rev-parse", "HEAD"); after != before {
		t.Errorf("empty commitMsg created a commit: %s -> %s", before, after)
	}
}

// A transaction rollback must never rewind counter.json past an ID a
// concurrent non-transactional writer allocated — SQL backends never revert
// another session's committed sequence allocation. If the restore raced (or
// rewound past) a foreign allocation, the next allocation would reuse its ID
// and atomicWrite would silently overwrite the existing issue file. The
// foreign CreateIssue must block until the transaction (and its rollback)
// completes, keep its allocation, and later allocations must not collide.
func TestRollbackDoesNotRewindCounterPastConcurrentAllocation(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := s.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode): %v", err)
	}

	txStarted := make(chan struct{})
	type allocation struct {
		id  string
		err error
	}
	foreign := make(chan allocation, 1)
	go func() {
		<-txStarted
		issue := &types.Issue{Title: "foreign survivor"}
		err := s.CreateIssue(ctx, issue, "other-goroutine")
		foreign <- allocation{issue.ID, err}
	}()

	sentinel := errors.New("boom")
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		// Bumps the counter and journals counter.json's pre-image.
		if err := tx.CreateIssue(ctx, &types.Issue{Title: "tx alloc"}, "t"); err != nil {
			return err
		}
		close(txStarted)
		// Give the foreign CreateIssue time to attempt its allocation while
		// the counter pre-image sits in the journal. Pre-isolation it could
		// allocate here and be rewound past by the rollback restore.
		time.Sleep(100 * time.Millisecond)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}

	got := <-foreign
	if got.err != nil {
		t.Fatalf("concurrent CreateIssue: %v", got.err)
	}

	// A fresh allocation after the rollback must not reuse the foreign ID.
	next := &types.Issue{Title: "post-rollback"}
	if err := s.CreateIssue(ctx, next, "t"); err != nil {
		t.Fatalf("CreateIssue after rollback: %v", err)
	}
	if next.ID == got.id {
		t.Errorf("counter rewound past concurrent allocation: %s reallocated", next.ID)
	}

	survivor, err := s.GetIssue(ctx, got.id)
	if err != nil {
		t.Fatalf("GetIssue(%s) after rollback: %v", got.id, err)
	}
	if survivor.Title != "foreign survivor" {
		t.Errorf("foreign issue overwritten: title = %q", survivor.Title)
	}
}

// BEADS_DIR lets the beads directory basename differ from ".beads"
// (cmd/bd FindBeadsDir semantics). The transaction version commit must still
// land: a hardcoded ".beads/" pathspec matches nothing in such a repo, so
// git status reports clean and commitTransaction silently skips the commit,
// leaving transactional writes uncommitted in the working tree. Extends
// TestRunInTransactionCommitsWithMessage, which covers the ".beads" default.
func TestRunInTransactionCommitsWithCustomBeadsDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "bd-test@example.com")
	git("config", "user.name", "bd test")
	git("config", "commit.gpgsign", "false")

	s, err := NewFlatFileStore(filepath.Join(dir, "beads-alt"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const msg = "bd: tx commit custom dir"
	err = s.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{ID: "test-1", Title: "in tx"}, "t")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if got := git("log", "-1", "--format=%s"); got != msg {
		t.Errorf("last commit subject = %q, want %q", got, msg)
	}
	if status := git("status", "--porcelain"); status != "" {
		t.Errorf("work tree not clean after transactional commit:\n%s", status)
	}
}

// Non-blocking dependency types must not participate in blocking-cycle
// detection: sqlite/dolt build the graph from blocks/conditional-blocks only
// (issueops.AppendBlockingGraphInTx), so 'A related B' plus a new 'B blocks A'
// is not a cycle on any SQL backend.
func TestCycleThroughEdgesIgnoresNonBlockingDeps(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []string{"test-a", "test-b"} {
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: id}, "t"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID: "test-a", DependsOnID: "test-b", Type: types.DepRelated,
	}, "t"); err != nil {
		t.Fatalf("AddDependency(related): %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks,
	}, "t"); err != nil {
		t.Fatalf("AddDependency(blocks): %v", err)
	}

	txForTest(t, s, func(tx storage.Transaction) {
		cycle, err := tx.CycleThroughEdges(ctx, [][2]string{{"test-b", "test-a"}})
		if err != nil {
			t.Fatalf("CycleThroughEdges: %v", err)
		}
		if cycle != "" {
			t.Errorf("cycle = %q, want none: related edges must not close blocking cycles", cycle)
		}
	})
}

// A real blocking cycle renders exactly as the shared SQL-backend renderer
// does ("u → v → u", issueops cycles_test.go) — no duplicated first node.
func TestCycleThroughEdgesRendersWithoutDuplicateNode(t *testing.T) {
	s := newTestStore(t)

	// Write the cyclic pair directly; AddDependency's per-edge check would
	// reject the second edge before CycleThroughEdges ever saw the graph.
	a := &types.Issue{ID: "test-a", Title: "a", Dependencies: []*types.Dependency{
		{IssueID: "test-a", DependsOnID: "test-b", Type: types.DepBlocks},
	}}
	b := &types.Issue{ID: "test-b", Title: "b", Dependencies: []*types.Dependency{
		{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks},
	}}
	for _, issue := range []*types.Issue{a, b} {
		if err := s.writeIssue(issue); err != nil {
			t.Fatalf("writeIssue(%s): %v", issue.ID, err)
		}
	}

	txForTest(t, s, func(tx storage.Transaction) {
		cycle, err := tx.CycleThroughEdges(ctx, [][2]string{{"test-b", "test-a"}})
		if err != nil {
			t.Fatalf("CycleThroughEdges: %v", err)
		}
		if want := "test-b → test-a → test-b"; cycle != want {
			t.Errorf("cycle = %q, want %q", cycle, want)
		}
	})
}

// A self-loop renders as "a → a", matching issueops cycles_test.go.
func TestCycleThroughEdgesSelfLoop(t *testing.T) {
	s := newTestStore(t)
	if err := s.writeIssue(&types.Issue{ID: "test-a", Title: "a", Dependencies: []*types.Dependency{
		{IssueID: "test-a", DependsOnID: "test-a", Type: types.DepBlocks},
	}}); err != nil {
		t.Fatalf("writeIssue: %v", err)
	}

	txForTest(t, s, func(tx storage.Transaction) {
		cycle, err := tx.CycleThroughEdges(ctx, [][2]string{{"test-a", "test-a"}})
		if err != nil {
			t.Fatalf("CycleThroughEdges: %v", err)
		}
		if want := "test-a → test-a"; cycle != want {
			t.Errorf("cycle = %q, want %q", cycle, want)
		}
	})
}

// A failed transaction that cascade-deleted an issue must restore the issue
// file, its events file, AND every comment file byte-identical — including
// recreating the comments/<id>/ directory the cascade removed (SQL rollback
// restores the comment rows; losing them violates all-or-nothing).
func TestRollbackRestoresCascadeDeletedCommentFiles(t *testing.T) {
	s := newTestStore(t)
	const id = "cascade-1"
	if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "with comments"}, "t"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	for _, text := range []string{"first comment", "second comment"} {
		if _, err := s.AddIssueComment(ctx, id, "t", text); err != nil {
			t.Fatalf("AddIssueComment: %v", err)
		}
	}

	// Snapshot every on-disk artifact of the issue before the transaction.
	pre := map[string][]byte{}
	snapshot := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("snapshot %s: %v", path, err)
		}
		pre[path] = data
	}
	snapshot(filepath.Join(s.issuesDir, id+".json"))
	snapshot(s.eventFilename(id))
	commentsDir := filepath.Join(s.commentsDir, id)
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("read comments dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("comment files = %d, want 2", len(entries))
	}
	for _, e := range entries {
		snapshot(filepath.Join(commentsDir, e.Name()))
	}

	sentinel := errors.New("boom")
	err = s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		if err := tx.DeleteIssue(ctx, id); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}
	if strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback itself failed: %v", err)
	}

	for path, want := range pre {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("after rollback, %s unreadable: %v", path, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("after rollback, %s differs from pre-transaction content", path)
		}
	}
}

// SQL backends never revert another session's committed write when a
// transaction rolls back. A non-transactional mutation that arrives while a
// transaction is active must therefore never be captured in that (foreign)
// transaction's journal: it is held until the transaction completes and then
// applied, surviving the rollback.
func TestRollbackDoesNotRevertConcurrentNonTxWrite(t *testing.T) {
	s := newTestStore(t)
	const id = "iso-1"
	if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "isolation"}, "t"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	txStarted := make(chan struct{})
	labelDone := make(chan error, 1)
	go func() {
		<-txStarted
		labelDone <- s.AddLabel(ctx, id, "survivor", "other-goroutine")
	}()

	sentinel := errors.New("boom")
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		// Journal the issue's pre-image (no label) inside the transaction.
		if err := tx.UpdateIssue(ctx, id, map[string]interface{}{"title": "in-tx"}, "t"); err != nil {
			return err
		}
		close(txStarted)
		// Give the other goroutine time to attempt its non-tx AddLabel while
		// this transaction is still open. Pre-fix it would run here and be
		// journaled; post-fix it blocks until the transaction completes.
		select {
		case err := <-labelDone:
			labelDone <- err
			t.Error("non-tx AddLabel completed inside an open transaction")
		case <-time.After(100 * time.Millisecond):
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}

	if err := <-labelDone; err != nil {
		t.Fatalf("concurrent AddLabel: %v", err)
	}
	got, err := s.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "isolation" {
		t.Errorf("title = %q, want %q (tx update not rolled back)", got.Title, "isolation")
	}
	if len(got.Labels) != 1 || got.Labels[0] != "survivor" {
		t.Errorf("labels = %v, want [survivor] (committed non-tx write reverted by foreign rollback)", got.Labels)
	}
}

// Deps arm of the same isolation rule (TASKS-eqgo): a non-transactional
// AddDependency arriving while a foreign transaction is open must not be
// journaled into that transaction — its edge survives the rollback, exactly
// as a committed edge from another SQL session would.
func TestRollbackDoesNotRevertConcurrentDepAdd(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateIssue(ctx, &types.Issue{ID: "dep-src", Title: "source"}, "t"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "dep-tgt", Title: "target"}, "t"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	txStarted := make(chan struct{})
	depDone := make(chan error, 1)
	go func() {
		<-txStarted
		depDone <- s.AddDependency(ctx, &types.Dependency{
			IssueID: "dep-src", DependsOnID: "dep-tgt", Type: types.DepBlocks,
		}, "other-goroutine")
	}()

	sentinel := errors.New("boom")
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		// Journal dep-src's pre-image (no dependencies) inside the tx, so a
		// mis-scoped journal would restore it over the concurrent edge.
		if err := tx.UpdateIssue(ctx, "dep-src", map[string]interface{}{"title": "in-tx"}, "t"); err != nil {
			return err
		}
		close(txStarted)
		select {
		case err := <-depDone:
			depDone <- err
			t.Error("non-tx AddDependency completed inside an open transaction")
		case <-time.After(100 * time.Millisecond):
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction error = %v, want sentinel", err)
	}

	if err := <-depDone; err != nil {
		t.Fatalf("concurrent AddDependency: %v", err)
	}
	got, err := s.GetIssue(ctx, "dep-src")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "source" {
		t.Errorf("title = %q, want %q (tx update not rolled back)", got.Title, "source")
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].DependsOnID != "dep-tgt" {
		t.Errorf("dependencies = %+v, want one edge to dep-tgt (committed non-tx AddDependency reverted by foreign rollback)", got.Dependencies)
	}
}
