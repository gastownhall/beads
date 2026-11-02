package schema

import (
	"database/sql"
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

// preflipTestContent builds an issue's content from rows of field values ("" is NULL).
func preflipTestContent(rows ...[]string) preflipEventsContent {
	c := preflipEventsContent{}
	for _, vals := range rows {
		fields := make([]sql.NullString, len(vals))
		for i, v := range vals {
			if v != "" {
				fields[i] = sql.NullString{String: v, Valid: true}
			}
		}
		c.add(fields)
	}
	return c
}

// allocatePreflipTestIDs runs allocatePreflipEvents over every need against a
// fixed set of held ids and returns the emitted ids in order.
func allocatePreflipTestIDs(t *testing.T, needs []preflipEventsNeed, held map[string]bool) []string {
	t.Helper()
	probe := func(ids []string) (map[string]bool, error) {
		out := make(map[string]bool)
		for _, id := range ids {
			if held[id] {
				out[id] = true
			}
		}
		return out, nil
	}
	var got []string
	for _, need := range needs {
		if err := allocatePreflipEvents(need, probe, func(r preflipEventsRow) error {
			got = append(got, r.id)
			return nil
		}); err != nil {
			t.Fatalf("allocatePreflipEvents: %v", err)
		}
	}
	return got
}

// TestPlanPreflipEventsRestoresMissingMultiplicityOnly pins the reconcile
// rule: a content is restored only as many times as the source holds it more
// often than the live table, whatever ids the live copies hold, under the
// lowest derived ids no row holds, in the same order on every call.
func TestPlanPreflipEventsRestoresMissingMultiplicityOnly(t *testing.T) {
	a, c := []string{"i1", "created"}, []string{"i2", "commented"}
	v := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	dA, dC := rowid.Digest([]sql.NullString{v("i1"), v("created")}), rowid.Digest([]sql.NullString{v("i2"), v("commented")})

	t.Run("a live legacy-id copy counts", func(t *testing.T) {
		needs := planPreflipEventsNeeds(preflipTestContent(a, a, c), preflipTestContent(a, c))
		got := allocatePreflipTestIDs(t, needs, map[string]bool{"legacy-random-id": true})
		if want := []string{rowid.New("events", 0, dA)}; !slices.Equal(got, want) {
			t.Fatalf("restored ids = %v, want only a's second copy at ordinal 0 %v", got, want)
		}
	})

	t.Run("occupied ordinals are skipped", func(t *testing.T) {
		needs := planPreflipEventsNeeds(preflipTestContent(c, c, c), preflipTestContent(c))
		got := allocatePreflipTestIDs(t, needs, map[string]bool{rowid.New("events", 0, dC): true})
		if want := []string{rowid.New("events", 1, dC), rowid.New("events", 2, dC)}; !slices.Equal(got, want) {
			t.Fatalf("restored ids = %v, want ordinals 1 and 2 %v", got, want)
		}
	})

	t.Run("live above the source restores nothing", func(t *testing.T) {
		if needs := planPreflipEventsNeeds(preflipTestContent(a), preflipTestContent(a, a)); len(needs) != 0 {
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
