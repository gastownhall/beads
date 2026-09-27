//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestAutoMintedCreateTakesCreateOnlyPath pins that an auto-minted create
// inserts CreateOnly even though the caller's options don't ask for it (the
// store's single-issue path passes none). The CreateOnly branch is the only
// writer of the issue-create coordination row in local_metadata, so the row is
// the observable proof: an auto-minted create leaves one, and an explicit-ID
// create, which keeps the caller's options, leaves none.
func TestAutoMintedCreateTakesCreateOnlyPath(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "co")
	ctx := t.Context()

	coordinationRows := func() int {
		var n int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM local_metadata WHERE `key` LIKE 'issue-create/%'", nil, &n)
		return n
	}

	explicit := &types.Issue{ID: "co-explicit", Title: "explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, explicit, "tester"); err != nil {
		t.Fatalf("explicit-ID CreateIssue: %v", err)
	}
	if n := coordinationRows(); n != 0 {
		t.Fatalf("explicit-ID create wrote %d coordination rows, want 0", n)
	}

	auto := &types.Issue{Title: "auto", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, auto, "tester"); err != nil {
		t.Fatalf("auto-minted CreateIssue: %v", err)
	}
	if n := coordinationRows(); n != 1 {
		t.Fatalf("auto-minted create wrote %d coordination rows, want 1", n)
	}
}

// TestFailedAutoMintedCreateRestoresEmptyID pins that a failed create hands an
// auto-minted struct back with an empty ID. A label over MaxFieldLen fails the
// create after the ID is minted and the row inserted. The transaction rolls
// back, so the minted ID names nothing, and a caller retrying the struct must
// mint again rather than take the explicit-ID upsert path. A caller-supplied
// ID is the caller's and survives the same failure.
func TestFailedAutoMintedCreateRestoresEmptyID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "fr")
	ctx := t.Context()
	tooLong := strings.Repeat("x", types.MaxFieldLen+1)

	auto := &types.Issue{Title: "auto", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{tooLong}}
	if err := te.store.CreateIssue(ctx, auto, "tester"); !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("auto-minted CreateIssue with an over-length label: err = %v, want ErrFieldTooLong", err)
	}
	if auto.ID != "" {
		t.Fatalf("failed auto-minted create left ID %q, want it restored to empty", auto.ID)
	}

	explicit := &types.Issue{ID: "fr-explicit", Title: "explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{tooLong}}
	if err := te.store.CreateIssue(ctx, explicit, "tester"); !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("explicit-ID CreateIssue with an over-length label: err = %v, want ErrFieldTooLong", err)
	}
	if explicit.ID != "fr-explicit" {
		t.Fatalf("failed explicit-ID create changed ID to %q, want fr-explicit", explicit.ID)
	}

	var rows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues", nil, &rows)
	if rows != 0 {
		t.Fatalf("issues holds %d rows after two failed creates, want 0", rows)
	}
}
