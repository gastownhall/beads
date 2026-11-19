package storagecontract

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// PreviousExternalRefsReader is the batch history read a tracker pull makes
// once per pull (storage.ExternalRefHistoryQuerier.PreviousExternalRefs).
type PreviousExternalRefsReader interface {
	PreviousExternalRefs(ctx context.Context, ids []string, asOf time.Time) (map[string]string, error)
}

// PreviousExternalRefsFixture supplies the store-specific hooks the contract
// needs. Every hook must act on the SAME database as Refs.
type PreviousExternalRefsFixture struct {
	// Prefix namespaces the ids the contract seeds.
	Prefix string
	// Refs is the batch read under test.
	Refs PreviousExternalRefsReader
	// Exec runs one statement on the store's database, outside the store's
	// own read path, and returns once it has completed.
	Exec func(ctx context.Context, stmt string, args ...any) error
	// ReadTx runs fn in a read transaction on the store's database. The
	// contract uses it to ask the single-id oracle,
	// issueops.PreviousExternalRefInTx.
	ReadTx func(ctx context.Context, fn func(*sql.Tx) error) error
}

// RunPreviousExternalRefsContract runs every case in order against one
// fixture. The cases seed raw rows with fixed, distinct commit dates (in
// 2020, before any commit the store made itself), so each cutoff has exactly
// one newest row per id and the oracle's answer is unambiguous.
func RunPreviousExternalRefsContract(t *testing.T, ctx context.Context, fx PreviousExternalRefsFixture) {
	t.Helper()
	t.Run("MatchesTheSingleIDOracle", func(t *testing.T) {
		RunPreviousExternalRefsMatchesTheSingleIDOracle(t, ctx, fx)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		refs, err := fx.Refs.PreviousExternalRefs(ctx, nil, time.Now())
		if err != nil || len(refs) != 0 {
			t.Fatalf("PreviousExternalRefs(nil) = (%v, %v), want an empty map", refs, err)
		}
	})
}

// contractBase is the first seeded commit's date minus an hour.
var contractBase = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

func at(hours float64) time.Time {
	return contractBase.Add(time.Duration(hours * float64(time.Hour)))
}

// RunPreviousExternalRefsMatchesTheSingleIDOracle seeds, over four commits an
// hour apart:
//
//	1h: same=A  relink=A  nullset=NULL  gone=G1  multi=M1
//	2h:         relink=B  nullset=N     gone=G2  multi=M2
//	3h: gone deleted, late=L created            multi=M3
//	4h: multi deleted
//
// plus working (inserted, never committed) and never (never inserted). At
// cutoffs before all history, on and between each commit, and after the
// last, the batch must agree with the oracle on every id's presence and
// value, and spot checks pin the answers so agreement cannot be vacuous.
func RunPreviousExternalRefsMatchesTheSingleIDOracle(t *testing.T, ctx context.Context, fx PreviousExternalRefsFixture) {
	t.Helper()
	id := func(name string) string { return fx.Prefix + "-" + name }
	ids := []string{id("same"), id("relink"), id("nullset"), id("gone"), id("multi"), id("late"), id("working"), id("never")}

	mustExec(t, ctx, fx, insertSQL, id("same"), "https://tracker.test/A")
	mustExec(t, ctx, fx, insertSQL, id("relink"), "https://tracker.test/A")
	mustExec(t, ctx, fx, insertSQL, id("nullset"), nil)
	mustExec(t, ctx, fx, insertSQL, id("gone"), "https://tracker.test/G1")
	mustExec(t, ctx, fx, insertSQL, id("multi"), "https://tracker.test/M1")
	commitAt(t, ctx, fx, "seed refs", at(1))

	mustExec(t, ctx, fx, updateSQL, "https://tracker.test/B", id("relink"))
	mustExec(t, ctx, fx, updateSQL, "https://tracker.test/N", id("nullset"))
	mustExec(t, ctx, fx, updateSQL, "https://tracker.test/G2", id("gone"))
	mustExec(t, ctx, fx, updateSQL, "https://tracker.test/M2", id("multi"))
	commitAt(t, ctx, fx, "relink refs", at(2))

	mustExec(t, ctx, fx, deleteSQL, id("gone"))
	mustExec(t, ctx, fx, insertSQL, id("late"), "https://tracker.test/L")
	mustExec(t, ctx, fx, updateSQL, "https://tracker.test/M3", id("multi"))
	commitAt(t, ctx, fx, "delete gone, create late", at(3))

	mustExec(t, ctx, fx, deleteSQL, id("multi"))
	commitAt(t, ctx, fx, "delete multi", at(4))

	mustExec(t, ctx, fx, insertSQL, id("working"), "https://tracker.test/W")

	type answer struct {
		ref   string
		found bool
	}
	present := func(ref string) answer { return answer{ref: ref, found: true} }
	absent := answer{}
	for _, cutoff := range []struct {
		hours float64
		spot  map[string]answer
	}{
		{0, map[string]answer{id("same"): absent, id("gone"): absent}},
		{1, map[string]answer{id("same"): present("https://tracker.test/A"), id("nullset"): present(""), id("late"): absent}},
		{1.5, map[string]answer{id("relink"): present("https://tracker.test/A"), id("nullset"): present("")}},
		{2, map[string]answer{id("relink"): present("https://tracker.test/B"), id("nullset"): present("https://tracker.test/N")}},
		{3, map[string]answer{id("gone"): present("https://tracker.test/G2"), id("late"): present("https://tracker.test/L"), id("multi"): present("https://tracker.test/M3")}},
		{10, map[string]answer{id("multi"): present("https://tracker.test/M3"), id("working"): absent, id("never"): absent}},
	} {
		t.Run(fmt.Sprintf("cutoff %gh", cutoff.hours), func(t *testing.T) {
			asOf := at(cutoff.hours)
			// Ask with a duplicate and a blank to show neither changes the answer.
			refs, err := fx.Refs.PreviousExternalRefs(ctx, append([]string{"", ids[0]}, ids...), asOf)
			if err != nil {
				t.Fatalf("PreviousExternalRefs: %v", err)
			}
			for got := range refs {
				if !containsID(ids, got) {
					t.Errorf("PreviousExternalRefs returned %s, which was not asked about", got)
				}
			}
			for _, issueID := range ids {
				var want answer
				if err := fx.ReadTx(ctx, func(tx *sql.Tx) error {
					var err error
					want.ref, want.found, err = issueops.PreviousExternalRefInTx(ctx, tx, issueID, asOf)
					return err
				}); err != nil {
					t.Fatalf("oracle PreviousExternalRefInTx(%s): %v", issueID, err)
				}
				ref, found := refs[issueID]
				if (answer{ref: ref, found: found}) != want {
					t.Errorf("%s: batch = (%q, %v), oracle = (%q, %v)", issueID, ref, found, want.ref, want.found)
				}
				if spot, ok := cutoff.spot[issueID]; ok && want != spot {
					t.Errorf("%s: oracle = (%q, %v), want (%q, %v): the fixture did not seed what this case asserts", issueID, want.ref, want.found, spot.ref, spot.found)
				}
			}
		})
	}
}

const (
	insertSQL = `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, external_ref) VALUES (?, 'previous external refs contract', '', '', '', '', ?)`
	updateSQL = `UPDATE issues SET external_ref = ? WHERE id = ?`
	deleteSQL = `DELETE FROM issues WHERE id = ?`
)

func mustExec(t *testing.T, ctx context.Context, fx PreviousExternalRefsFixture, stmt string, args ...any) {
	t.Helper()
	if err := fx.Exec(ctx, stmt, args...); err != nil {
		t.Fatalf("%s %v: %v", stmt, args, err)
	}
}

func commitAt(t *testing.T, ctx context.Context, fx PreviousExternalRefsFixture, msg string, when time.Time) {
	t.Helper()
	mustExec(t, ctx, fx, "CALL DOLT_COMMIT('-A', '-m', ?, '--date', ?)", msg, when.UTC().Format("2006-01-02T15:04:05"))
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
