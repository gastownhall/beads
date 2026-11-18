//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestAdaptiveIDLengthSpansBothPlanes pins the population a new hash id is
// sized against (bd-u9cfx0, PR #6968): issues and wisps share one id space,
// and a promoted wisp keeps its <prefix>-wisp-* id in issues while a demoted
// issue keeps its id in wisps, so the sizing count must span both planes —
// the destination table's rows under the prefix, plus the sibling table's
// rows in the prefix's EXACT namespace. Live <prefix>-wisp-* wisps never
// lengthen the base prefix: such an id can never equal a <prefix>-<hash> one.
//
// Every subtest sets max_collision_prob=0.001, where 5 rows size to a
// three-character hash and 10 rows cross to a four-character one, so both
// directions are provable with a handful of rows. Seeded ids carry fixed
// non-hash suffixes a mint cannot collide with, and a collision retry walks
// nonces at the sized length, so the minted suffix length is exactly the
// sized length.
func TestAdaptiveIDLengthSpansBothPlanes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	at := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	// strictProb drops the birthday bound to 0.001: 5 rows still size to
	// length 3, 10 rows cross to length 4.
	strictProb := func(t *testing.T, te *testEnv, ctx context.Context) {
		t.Helper()
		te.exec(t, ctx, "REPLACE INTO config (`key`, value) VALUES ('max_collision_prob', '0.001')")
	}
	// ids renders n distinct ids from a %02d pattern.
	ids := func(pattern string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf(pattern, i+1)
		}
		return out
	}
	// seed writes ids into one plane through the store, the way an explicit
	// create, an import or a promotion would leave them there.
	seed := func(t *testing.T, te *testEnv, ctx context.Context, ids []string, ephemeral bool) {
		t.Helper()
		for _, id := range ids {
			issue := &types.Issue{
				ID:        id,
				Title:     "seed " + id,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: ephemeral,
				CreatedAt: at,
			}
			if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
	}
	// countUnder counts one table's rows under prefix.
	countUnder := func(t *testing.T, te *testEnv, ctx context.Context, table, prefix string) int {
		t.Helper()
		var n int
		te.queryScalar(t, ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id LIKE ?", table),
			[]any{prefix + "-%"}, &n)
		return n
	}
	// sizedLength asks the embedded minter's own sizing helper.
	sizedLength := func(t *testing.T, te *testEnv, ctx context.Context, table, prefix string) int {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		n, err := issueops.GetAdaptiveIDLengthTx(ctx, db, table, prefix)
		if err != nil {
			t.Fatalf("GetAdaptiveIDLengthTx(%s, %s): %v", table, prefix, err)
		}
		return n
	}
	// hashSuffix returns the id's suffix under prefix, refusing child ids.
	hashSuffix := func(t *testing.T, id, prefix string) string {
		t.Helper()
		suffix, ok := strings.CutPrefix(id, prefix+"-")
		if !ok {
			t.Fatalf("id %q does not carry prefix %q", id, prefix)
		}
		if strings.Contains(suffix, ".") {
			t.Fatalf("id %q: suffix %q is a child id", id, suffix)
		}
		return suffix
	}
	// mint creates one issue through the store and returns its id.
	mint := func(t *testing.T, te *testEnv, ctx context.Context, title string, ephemeral bool) string {
		t.Helper()
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: ephemeral,
			CreatedAt: at,
		}
		if err := te.store.CreateIssue(ctx, issue, "minter"); err != nil {
			t.Fatalf("CreateIssue(%q, ephemeral=%v): %v", title, ephemeral, err)
		}
		return issue.ID
	}

	t.Run("WispMintCountsPromotedWispsInIssues", func(t *testing.T) {
		te := newTestEnv(t, "kz")
		ctx := t.Context()
		strictProb(t, te, ctx)

		// Ten wisps; the first five are promoted, keeping their kz-wisp-*
		// ids in issues (live wisps get reaped, promoted ones accumulate).
		seed(t, te, ctx, ids("kz-wisp-w%02d", 10), true)
		for _, id := range ids("kz-wisp-w%02d", 5) {
			if err := te.store.PromoteFromEphemeral(ctx, id, "promoter"); err != nil {
				t.Fatalf("promote %s: %v", id, err)
			}
		}
		if got := countUnder(t, te, ctx, "issues", "kz-wisp"); got != 5 {
			t.Fatalf("promoted wisps in issues = %d, want 5", got)
		}
		if got := countUnder(t, te, ctx, "wisps", "kz-wisp"); got != 5 {
			t.Fatalf("live wisps = %d, want 5", got)
		}
		if got := sizedLength(t, te, ctx, "wisps", "kz-wisp"); got != 4 {
			t.Fatalf("GetAdaptiveIDLengthTx(wisps, kz-wisp) = %d, want 4 (population 10 across both planes)", got)
		}
		id := mint(t, te, ctx, "fresh wisp", true)
		if suffix := hashSuffix(t, id, "kz-wisp"); len(suffix) != 4 {
			t.Fatalf("minted wisp id %s: hash %q has %d chars, want 4", id, suffix, len(suffix))
		}
	})
}
