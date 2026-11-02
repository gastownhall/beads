package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

const ignoredMigration0031Up = "0031_restore_preflip_events.up.sql"

// TestLatestIgnoredVersionIncludesMigration0031 pins the ignored slot the
// pre-upgrade events restore claims (gastownhall/beads#7039), directly above
// wisp_id_tombstones' (ignored 0030), and that the slot carries its repair.
func TestLatestIgnoredVersionIncludesMigration0031(t *testing.T) {
	const wantIgnored = 31
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (pre-upgrade events restore)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0031 has no pre-migration repair registered; the marker alone restores nothing")
	}
}

// TestEventsFlipDropCommitMessageMatches0062 pins the message the restore
// locates its sources by against the frozen 0062 bytes that write it.
func TestEventsFlipDropCommitMessageMatches0062(t *testing.T) {
	flipSQL, err := MigrationSQL("0062_events_dolt_ignore.up.sql")
	if err != nil {
		t.Fatalf("MigrationSQL(0062) = %v", err)
	}
	want := "CALL DOLT_COMMIT('--skip-empty', '-m', '" + eventsFlipDropCommitMessage + "');"
	if !strings.Contains(flipSQL, want) {
		t.Fatalf("0062 no longer commits the drop with %q; the restore would find no source", eventsFlipDropCommitMessage)
	}
}

// TestMigration0031MarkerIsSeenTouchingEvents pins the raw-text declaration
// the pending-migration dirty-table guard relies on: the marker's repair
// writes events, so a tracked, dirty events table must refuse the pass.
func TestMigration0031MarkerIsSeenTouchingEvents(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0031Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0031Up, err)
	}
	if !migrationSQLTouchesTable(markerSQL, "events") {
		t.Fatal("ignored 0031 does not declare its events write; the dirty-table guard would not see it")
	}
	if migrationSQLTouchesTable(markerSQL, "issues") || migrationSQLTouchesTable(markerSQL, "bd_events_journal") {
		t.Fatal("ignored 0031 declares a write to a table its repair never touches")
	}
}

// TestPreflipEventsColumnsMatchAuxRekeyDigestList pins the restore's digest
// columns to the aux rekey's events list: the restore derives ids with the
// same rowid.Digest the rekey converged existing rows to, so a drift between
// the two lists would mint ids no clone agrees on and miss rows already held
// under their rekeyed ids.
func TestPreflipEventsColumnsMatchAuxRekeyDigestList(t *testing.T) {
	for _, table := range auxRekeyTables {
		if table.name != "events" {
			continue
		}
		if table.columns != preflipEventsDigestColumns {
			t.Fatalf("preflipEventsDigestColumns diverged from the aux rekey's events digest list:\n  rekey:   %s\n  restore: %s", table.columns, preflipEventsDigestColumns)
		}
		return
	}
	t.Fatal("auxRekeyTables no longer covers events")
}

// preflipTestContent builds a source issue's content from rows of field
// values ("" is NULL); row i gets the id "src-<i>".
func preflipTestContent(rows ...[]string) preflipEventsContent {
	c := preflipEventsContent{}
	for i, vals := range rows {
		c.add(rowid.Digest(preflipTestFields(vals)), fmt.Sprintf("src-%d", i), true)
	}
	return c
}

// preflipTestLive is preflipTestContent for the live side, which keeps no ids.
func preflipTestLive(rows ...[]string) preflipEventsContent {
	c := preflipEventsContent{}
	for _, vals := range rows {
		c.add(rowid.Digest(preflipTestFields(vals)), "", false)
	}
	return c
}

func preflipTestFields(vals []string) []sql.NullString {
	fields := make([]sql.NullString, len(vals))
	for i, v := range vals {
		if v != "" {
			fields[i] = sql.NullString{String: v, Valid: true}
		}
	}
	return fields
}

// preflipTestProbe answers an id probe from a fixed set of held ids and
// records how many probes ran and the largest one.
type preflipTestProbe struct {
	held          map[string]bool
	calls, widest int
}

func (p *preflipTestProbe) probe(ids []string) (map[string]bool, error) {
	p.calls++
	p.widest = max(p.widest, len(ids))
	out := make(map[string]bool)
	for _, id := range ids {
		if p.held[id] {
			out[id] = true
		}
	}
	return out, nil
}

// allocatePreflipTestIDs allocates every need against held and returns the
// assigned ids in need order.
func allocatePreflipTestIDs(t *testing.T, needs []*preflipEventsNeed, held map[string]bool) []string {
	t.Helper()
	p := &preflipTestProbe{held: held}
	if err := allocatePreflipEvents(needs, p.probe); err != nil {
		t.Fatalf("allocatePreflipEvents: %v", err)
	}
	var got []string
	for _, need := range needs {
		if len(need.dstIDs) != len(need.srcIDs) {
			t.Fatalf("need %s got %d ids for %d source rows", need.digest[:12], len(need.dstIDs), len(need.srcIDs))
		}
		got = append(got, need.dstIDs...)
	}
	return got
}

// TestPlanPreflipEventsRestoresMissingMultiplicityOnly pins the reconcile
// rule: a content is restored only as many times as the source holds it more
// often than the live table, whatever ids the live copies hold, by copying its
// lowest-id source rows under the lowest derived ids no row holds, in the same
// order on every call.
func TestPlanPreflipEventsRestoresMissingMultiplicityOnly(t *testing.T) {
	a, c := []string{"i1", "created"}, []string{"i2", "commented"}
	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	dA, dC := rowid.Digest([]sql.NullString{v("i1"), v("created")}), rowid.Digest([]sql.NullString{v("i2"), v("commented")})

	t.Run("a live legacy-id copy counts", func(t *testing.T) {
		needs := planPreflipEventsNeeds(preflipTestContent(a, a, c), preflipTestLive(a, c))
		if len(needs) != 1 || !slices.Equal(needs[0].srcIDs, []string{"src-0"}) {
			t.Fatalf("needs = %+v, want one copy of a's lowest-id source row", needs)
		}
		got := allocatePreflipTestIDs(t, needs, map[string]bool{"legacy-random-id": true})
		if want := []string{rowid.New("events", 0, dA)}; !slices.Equal(got, want) {
			t.Fatalf("restored ids = %v, want only a's second copy at ordinal 0 %v", got, want)
		}
	})

	t.Run("occupied ordinals are skipped", func(t *testing.T) {
		needs := planPreflipEventsNeeds(preflipTestContent(c, c, c), preflipTestLive(c))
		got := allocatePreflipTestIDs(t, needs, map[string]bool{rowid.New("events", 0, dC): true})
		if want := []string{rowid.New("events", 1, dC), rowid.New("events", 2, dC)}; !slices.Equal(got, want) {
			t.Fatalf("restored ids = %v, want ordinals 1 and 2 %v", got, want)
		}
	})

	t.Run("live above the source restores nothing", func(t *testing.T) {
		if needs := planPreflipEventsNeeds(preflipTestContent(a), preflipTestLive(a, a)); len(needs) != 0 {
			t.Fatalf("needs = %+v, want none: live content beyond the source is never reduced", needs)
		}
	})

	t.Run("deterministic across calls", func(t *testing.T) {
		first := allocatePreflipTestIDs(t, planPreflipEventsNeeds(preflipTestContent(c, a, c, a), preflipEventsContent{}), nil)
		second := allocatePreflipTestIDs(t, planPreflipEventsNeeds(preflipTestContent(a, c, a, c), preflipEventsContent{}), nil)
		if len(first) != 4 || !slices.Equal(first, second) {
			t.Fatalf("allocations differ across calls: %v vs %v", first, second)
		}
	})
}

// TestAllocatePreflipEventsBatchesItsProbes pins that ids are checked for
// many needs at once, in bounded probes, not one probe per row: the restore's
// statement count must not grow with the number of rows it copies. Needs
// whose candidates are taken get their next free ordinals in a later round.
func TestAllocatePreflipEventsBatchesItsProbes(t *testing.T) {
	defer SetPreflipEventsBatchSizesForTest(200, 4)()
	var needs []*preflipEventsNeed
	held := make(map[string]bool)
	for i := range 10 {
		digest := rowid.Digest([]sql.NullString{{String: fmt.Sprintf("issue-%d", i), Valid: true}})
		needs = append(needs, &preflipEventsNeed{digest: digest, srcIDs: []string{fmt.Sprintf("s%d-a", i), fmt.Sprintf("s%d-b", i)}})
		if i%3 == 0 {
			// Another row holds this content's ordinal 1.
			held[rowid.New("events", 1, digest)] = true
		}
	}
	p := &preflipTestProbe{held: held}
	if err := allocatePreflipEvents(needs, p.probe); err != nil {
		t.Fatalf("allocatePreflipEvents: %v", err)
	}
	// 20 candidates in probes of at most 4, then the 4 needs that lost
	// ordinal 1 propose ordinal 2 together: one more probe.
	if p.calls != 6 || p.widest > 4 {
		t.Fatalf("probes = %d (widest %d), want 6 of at most 4 ids", p.calls, p.widest)
	}
	for i, need := range needs {
		want := []string{rowid.New("events", 0, need.digest), rowid.New("events", 1, need.digest)}
		if i%3 == 0 {
			want[1] = rowid.New("events", 2, need.digest)
		}
		if !slices.Equal(need.dstIDs, want) {
			t.Fatalf("need %d ids = %v, want %v", i, need.dstIDs, want)
		}
	}
}

// TestUnreadablePreflipSourceClassifiesHistoricalErrors pins which failures
// skip a source: any failure reading its history, except a canceled pass
// (whatever the error looks like) and a failure that looks like the
// connection rather than the history. Those, and every live failure, keep
// 0031 pending for the next open.
func TestUnreadablePreflipSourceClassifiesHistoricalErrors(t *testing.T) {
	drift := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"historical drift", context.Background(), &preflipSourceError{drift}, true},
		{"incompatible shape", context.Background(), &preflipSourceError{errPreflipSourceIncompatible}, true},
		{"wrapped historical drift", context.Background(), fmt.Errorf("reading: %w", &preflipSourceError{drift}), true},
		{"unrecognized historical error", context.Background(), &preflipSourceError{errors.New("Error 1146: table not found")}, true},
		{"missing chunk", context.Background(), &preflipSourceError{errors.New("Error 1105 (HY000): chunk not found")}, true},
		{"transient historical error", context.Background(), &preflipSourceError{errors.New("driver: bad connection")}, false},
		{"historical i/o timeout", context.Background(), &preflipSourceError{errors.New("read tcp 127.0.0.1:3307: i/o timeout")}, false},
		{"dead pinned connection", context.Background(), &preflipSourceError{fmt.Errorf("reading: %w", sql.ErrConnDone)}, false},
		{"server-side cancellation", context.Background(), &preflipSourceError{errors.New("Error 1105 (HY000): context canceled")}, false},
		{"live drift", context.Background(), drift, false},
		{"canceled historical drift", canceled, &preflipSourceError{drift}, false},
		{"canceled historical cancellation", canceled, &preflipSourceError{context.Canceled}, false},
		{"nil", context.Background(), nil, false},
	} {
		if got := unreadablePreflipSource(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: unreadablePreflipSource = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPreflipGhostCommitErrRecognizesShallowClones pins the one history
// failure that is silence rather than a warning: Dolt's refusal to read a
// commit a shallow clone does not hold (doltdb.ErrGhostCommitEncountered).
func TestPreflipGhostCommitErrRecognizesShallowClones(t *testing.T) {
	ghost := errors.New("Error 1105: Commit not found. You are using a shallow clone which does not contain the requested commit. Please do a full clone.")
	if !preflipGhostCommitErr(ghost) {
		t.Fatalf("preflipGhostCommitErr(%q) = false, want true", ghost)
	}
	if preflipGhostCommitErr(errors.New("Error 1105: chunk not found")) {
		t.Fatal("preflipGhostCommitErr took a missing chunk for a shallow clone's ghost commit")
	}
}
