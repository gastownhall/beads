//go:build cgo

package embeddeddolt_test

import (
	"testing"
)

// TestCommitAllOutOfBandChangeAndHonestNoOp covers the embedded half of the
// explicit-commit fix. Embedded Commit already stages everything via
// DOLT_COMMIT('-Am'), so the staging gap is server-mode-only; what CommitAll
// adds here is the honest report: (true, nil) exactly when a commit was
// created, (false, nil) when there was nothing to commit — so `bd vc commit`
// can print "Nothing to commit" instead of "Created commit <existing HEAD>".
func TestCommitAllOutOfBandChangeAndHonestNoOp(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "cat")

	// Flush anything setup left uncommitted, then pin the honest no-op.
	if _, err := te.store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}
	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	committed, err := te.store.CommitAll(ctx, "test: nothing to commit")
	if err != nil {
		t.Fatalf("CommitAll on clean working set: %v", err)
	}
	if committed {
		t.Fatalf("CommitAll reported a commit on a clean working set")
	}
	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("CommitAll reported committed=false but HEAD advanced: %s -> %s", headBefore, headAfter)
	}

	// Out-of-band write: a config row inserted through a raw SQL connection,
	// bypassing the store's transaction tracking entirely.
	te.exec(t, ctx, "INSERT INTO config (`key`, value) VALUES ('test.oob-commit-all', 'v1')")

	committed, err = te.store.CommitAll(ctx, "test: sweep out-of-band config")
	if err != nil {
		t.Fatalf("CommitAll with out-of-band config change: %v", err)
	}
	if !committed {
		t.Fatalf("CommitAll reported nothing to commit while config was dirty")
	}
	var dirty int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'", nil, &dirty)
	if dirty != 0 {
		t.Fatalf("CommitAll left config dirty (%d dolt_status rows)", dirty)
	}
	headSwept, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headSwept == headAfter {
		t.Fatalf("CommitAll reported committed=true but HEAD did not advance from %s", headAfter)
	}
}
