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

	t.Run("wisp mint counts a wisp namespace promoted entirely into issues", func(t *testing.T) {
		// The reported shape (PR #6968): every kz-wisp-* row was promoted,
		// so the wisps table is empty and its own count reads zero.
		te, ctx := newAdaptiveIDEnv(t, "kz")
		ids := seededIDs("kz-wisp-q", 10)
		ids = append(ids, seededIDs("kz-wisp-r", 2)...)
		seedRows(t, te, ids, true)
		for _, id := range ids {
			if err := te.store.PromoteFromEphemeral(ctx, id, "tester"); err != nil {
				t.Fatalf("PromoteFromEphemeral(%s): %v", id, err)
			}
		}
		var inIssues, inWisps int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'kz-wisp-%'", nil, &inIssues)
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps", nil, &inWisps)
		if inIssues != len(ids) || inWisps != 0 {
			t.Fatalf("after promotion: issues kz-wisp-* rows = %d, wisps rows = %d; want %d and 0", inIssues, inWisps, len(ids))
		}

		assertAdaptiveSizing(t, te, "wisps", "kz-wisp", 12, 4)
		id := mintOne(t, te, "next wisp", true)
		assertHashSuffixLen(t, id, "kz-wisp", 4)
		te.assertRowExists(t, ctx, "wisps", id)
	})

	t.Run("durable mint counts base-namespace rows in wisps", func(t *testing.T) {
		// AC-2: 5 dm-<hash> rows in issues plus 5 in wisps (demoted issues,
		// or base-namespace wisps).
		te, ctx := newAdaptiveIDEnv(t, "dm")
		seedRows(t, te, seededIDs("dm-a", 5), false)
		seedRows(t, te, seededIDs("dm-b", 5), true)

		assertAdaptiveSizing(t, te, "issues", "dm", 10, 4)
		id := mintOne(t, te, "fresh issue", false)
		assertHashSuffixLen(t, id, "dm", 4)
		te.assertRowExists(t, ctx, "issues", id)
	})

	t.Run("durable mint counts a base namespace held only in wisps", func(t *testing.T) {
		// The reported shape: every dm-<hash> row sits in wisps.
		te, _ := newAdaptiveIDEnv(t, "dm")
		seedRows(t, te, seededIDs("dm-b", 10), true)
		seedRows(t, te, seededIDs("dm-c", 2), true)

		assertAdaptiveSizing(t, te, "issues", "dm", 12, 4)
	})

	t.Run("live wisps outside the base namespace do not lengthen base IDs", func(t *testing.T) {
		// AC-3: a plain sum of both tables' lw-% counts would be 15
		// (length 4); only the 3 lw-<hash> issues share the namespace.
		te, ctx := newAdaptiveIDEnv(t, "lw")
		seedRows(t, te, seededIDs("lw-a", 3), false)
		seedRows(t, te, seededIDs("lw-wisp-b", 10), true)
		seedRows(t, te, seededIDs("lw-mol-c", 2), true)

		assertAdaptiveSizing(t, te, "issues", "lw", 3, 3)
		id := mintOne(t, te, "fresh issue", false)
		assertHashSuffixLen(t, id, "lw", 3)
		te.assertRowExists(t, ctx, "issues", id)
	})

	t.Run("destination sub-prefix rows still count for the base prefix", func(t *testing.T) {
		// AC-4: the destination table's own rule is unchanged, so 10
		// sp-mol-* issues still give a base sp mint length 4. An
		// exact-namespace rule on both sides would drop them to 0 (length 3).
		te, ctx := newAdaptiveIDEnv(t, "sp")
		seedRows(t, te, seededIDs("sp-mol-a", 10), false)

		assertAdaptiveSizing(t, te, "issues", "sp", 10, 4)
		id := mintOne(t, te, "fresh issue", false)
		assertHashSuffixLen(t, id, "sp", 4)
		te.assertRowExists(t, ctx, "issues", id)
	})

	t.Run("mints under the combined count leave existing IDs unchanged", func(t *testing.T) {
		// AC-5: rows of both planes in both namespaces, including promoted
		// wisps; a wisp mint and a durable mint add rows and touch none.
		te, ctx := newAdaptiveIDEnv(t, "kp")
		seedRows(t, te, seededIDs("kp-a", 5), false)
		seedRows(t, te, seededIDs("kp-b", 5), true)
		promoted := seededIDs("kp-wisp-c", 6)
		seedRows(t, te, promoted, true)
		for _, id := range promoted[:3] {
			if err := te.store.PromoteFromEphemeral(ctx, id, "tester"); err != nil {
				t.Fatalf("PromoteFromEphemeral(%s): %v", id, err)
			}
		}
		before := snapshotPlanes(t, te)

		wisp := mintOne(t, te, "fresh wisp", true)
		assertHashSuffixLen(t, wisp, "kp-wisp", 3)
		issue := mintOne(t, te, "fresh issue", false)
		assertHashSuffixLen(t, issue, "kp", 4)

		after := snapshotPlanes(t, te)
		for id, row := range before {
			if after[id] != row {
				t.Errorf("existing %s changed: before %q, after %q", id, row, after[id])
			}
		}
		if len(after) != len(before)+2 || after[wisp] != "wisps|fresh wisp" || after[issue] != "issues|fresh issue" {
			t.Errorf("after the mints: %d rows (want %d), %s=%q, %s=%q", len(after), len(before)+2, wisp, after[wisp], issue, after[issue])
		}
	})
}

// snapshotPlanes returns every live row of both tables as id -> "table|title".
func snapshotPlanes(t *testing.T, te *testEnv) map[string]string {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	rows, err := db.QueryContext(ctx, "SELECT 'issues', id, title FROM issues UNION ALL SELECT 'wisps', id, title FROM wisps")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var table, id, title string
		if err := rows.Scan(&table, &id, &title); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		if prev, dup := out[id]; dup {
			t.Fatalf("id %s is in both planes (%s and %s)", id, prev, table)
		}
		out[id] = table + "|" + title
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return out
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
