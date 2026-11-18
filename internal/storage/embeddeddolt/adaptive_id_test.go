//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestAdaptiveIDLengthSpansBothPlanes pins the embedded minter's adaptive
// hash-ID sizing (issueops.GetAdaptiveIDLengthTx, reached through
// GenerateIssueIDInTable on create) across both live tables: issues and wisps
// share one ID space, so the population a new ID is sized from is the
// destination table's own prefix count plus the sibling table's rows in the
// prefix's exact namespace (bd-u9cfx0, PR #6968).
//
// Every case sets max_collision_prob=0.001, where 10 rows cross the 3->4
// length boundary. Seeded IDs carry two-character suffixes, so no mint at
// length 3 or more can collide with one: a longer hash is adaptive growth,
// never a collision retry.
func TestAdaptiveIDLengthSpansBothPlanes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("wisp mint counts promoted wisps in issues", func(t *testing.T) {
		// AC-1: 5 live wisps plus 5 promoted ones, which keep their
		// kz-wisp-* IDs in issues.
		te, ctx := newAdaptiveIDEnv(t, "kz")
		ids := seededIDs("kz-wisp-p", 10)
		seedRows(t, te, ids, true)
		for _, id := range ids[:5] {
			if err := te.store.PromoteFromEphemeral(ctx, id, "tester"); err != nil {
				t.Fatalf("PromoteFromEphemeral(%s): %v", id, err)
			}
			te.assertRowExists(t, ctx, "issues", id)
			te.assertRowNotExists(t, ctx, "wisps", id)
		}

		assertAdaptiveSizing(t, te, "wisps", "kz-wisp", 10, 4)
		id := mintOne(t, te, "fresh wisp", true)
		assertHashSuffixLen(t, id, "kz-wisp", 4)
		te.assertRowExists(t, ctx, "wisps", id)
	})
}

// newAdaptiveIDEnv returns an isolated store under prefix with
// max_collision_prob=0.001.
func newAdaptiveIDEnv(t *testing.T, prefix string) (*testEnv, context.Context) {
	t.Helper()
	te := newTestEnv(t, prefix)
	ctx := t.Context()
	te.exec(t, ctx, "REPLACE INTO config (`key`, value) VALUES ('max_collision_prob', '0.001')")
	return te, ctx
}

// seededIDs returns n IDs base0..base(n-1); with a one-letter base suffix
// they have two-character suffixes.
func seededIDs(base string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%d", base, i)
	}
	return ids
}

// seedRows creates one row per id, in wisps when ephemeral, else in issues.
func seedRows(t *testing.T, te *testEnv, ids []string, ephemeral bool) {
	t.Helper()
	ctx := t.Context()
	table := "issues"
	if ephemeral {
		table = "wisps"
	}
	for _, id := range ids {
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "seeded " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral,
		}, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		te.assertRowExists(t, ctx, table, id)
	}
}

// assertAdaptiveSizing checks the population and the starting hash length
// the embedded minter computes for a new ID under prefix bound for table.
func assertAdaptiveSizing(t *testing.T, te *testEnv, table, prefix string, wantCount, wantLength int) {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	count, err := issueops.AdaptiveIDCountInTx(ctx, db, table, prefix)
	if err != nil {
		t.Fatalf("AdaptiveIDCountInTx(%s, %s): %v", table, prefix, err)
	}
	if count != wantCount {
		t.Errorf("AdaptiveIDCountInTx(%s, %s) = %d, want %d", table, prefix, count, wantCount)
	}
	length, err := issueops.GetAdaptiveIDLengthTx(ctx, db, table, prefix)
	if err != nil {
		t.Fatalf("GetAdaptiveIDLengthTx(%s, %s): %v", table, prefix, err)
	}
	if length != wantLength {
		t.Errorf("GetAdaptiveIDLengthTx(%s, %s) = %d, want %d", table, prefix, length, wantLength)
	}
}

// mintOne creates an issue (a wisp when ephemeral) that names no ID and
// returns the ID the store minted for it.
func mintOne(t *testing.T, te *testEnv, title string, ephemeral bool) string {
	t.Helper()
	issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	if err := te.store.CreateIssue(t.Context(), issue, "minter"); err != nil {
		t.Fatalf("CreateIssue(%q): %v", title, err)
	}
	if issue.ID == "" {
		t.Fatalf("CreateIssue(%q) minted no ID", title)
	}
	return issue.ID
}

// assertHashSuffixLen checks id is prefix-<hash> with a hash of wantLen chars.
func assertHashSuffixLen(t *testing.T, id, prefix string, wantLen int) {
	t.Helper()
	hash, ok := strings.CutPrefix(id, prefix+"-")
	if !ok || strings.ContainsAny(hash, "-.") {
		t.Fatalf("minted %q, want %s-<hash>", id, prefix)
	}
	if len(hash) != wantLen {
		t.Errorf("minted %q: hash %q has %d chars, want %d", id, hash, len(hash), wantLen)
	}
}
