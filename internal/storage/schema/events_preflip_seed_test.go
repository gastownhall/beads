package schema

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

// Pins for the pre-flip events restore (gh 7039, ignored/0031): the version
// wiring, the frozen strings it matches, the marker's raw-text dirty-guard
// declaration, and the pure helpers' reconciliation and classification
// contracts. The end-to-end restore paths run against the real embedded
// engine in internal/storage/embeddeddolt/migrate_events_preflip_seed_test.go.

func TestLatestIgnoredVersionIncludesMigration0031(t *testing.T) {
	if got := LatestIgnoredVersion(); got != 31 {
		t.Fatalf("LatestIgnoredVersion() = %d, want 31 (0031 pre-flip events restore)", got)
	}
}

func TestPreflipEventsDropMessagePinnedToFrozen0062(t *testing.T) {
	sqlText, err := MigrationSQL("0062_events_dolt_ignore.up.sql")
	if err != nil {
		t.Fatalf("MigrationSQL(0062): %v", err)
	}
	if !strings.Contains(sqlText, "'"+preflipEventsDropMessage+"'") {
		t.Fatal("0062 no longer commits with the drop message the restore matches; update preflipEventsDropMessage with the frozen file")
	}
}

func TestPreflipEventsColumnsMatchAuxRekeyDigestList(t *testing.T) {
	for _, table := range auxRekeyTables {
		if table.name != "events" {
			continue
		}
		if table.columns != preflipEventsColumns {
			t.Fatalf("preflipEventsColumns diverged from the aux rekey's events digest list:\n  rekey:   %s\n  restore: %s", table.columns, preflipEventsColumns)
		}
		return
	}
	t.Fatal("auxRekeyTables no longer covers events")
}

// TestMigration0031MarkerDeclaresEventsWritesForDirtyGuard pins the marker's
// load-bearing comment: the restore's writes happen in Go, but the pass must
// still refuse to entangle them with a pre-existing dirty tracked events
// table, which is exactly what migrationSQLTouchesTable reading the marker's
// raw text (comments included, by design) provides. The marker must not
// declare touches on any other table, or unrelated dirty rows would block
// the restore.
func TestMigration0031MarkerDeclaresEventsWritesForDirtyGuard(t *testing.T) {
	sqlText, err := IgnoredMigrationSQL("0031_restore_preflip_events.up.sql")
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(0031): %v", err)
	}
	if !migrationSQLTouchesTable(sqlText, "events") {
		t.Fatal("0031 marker does not declare its events writes; a dirty tracked events table would no longer block the restore")
	}
	for _, table := range []string{"issues", "bd_events_journal", "wisps", "ignored_schema_migrations"} {
		if migrationSQLTouchesTable(sqlText, table) {
			t.Fatalf("0031 marker declares a write to %s; unrelated dirty tables must not block the restore", table)
		}
	}
	if body := strings.TrimSpace(sqlText); !strings.HasSuffix(body, "SELECT 1;") {
		t.Fatalf("0031 marker body is not the inert SELECT 1; sentinel: %q", body)
	}
}

func TestPlanPreflipSeedInsertsMissingMultiplicityOnly(t *testing.T) {
	row := func(vals ...string) preflipEventRow {
		fields := make([]sql.NullString, len(vals))
		for i, v := range vals {
			if v != "" {
				fields[i] = sql.NullString{String: v, Valid: true}
			}
		}
		return preflipEventRow{fields: fields}
	}
	rowA, rowC := row("i1", "created"), row("i2", "commented")
	digestA, digestC := rowA.digest(), rowC.digest()

	t.Run("restores only the missing multiplicity", func(t *testing.T) {
		source := map[string]preflipSourceDigest{
			digestA: {count: 2, sample: rowA},
			digestC: {count: 1, sample: rowC},
		}
		// One copy of digestA already lives under a legacy id; digestC is
		// fully present. Only digestA's second copy may be inserted.
		liveCounts := map[string]int{digestA: 1, digestC: 1}
		liveIDs := map[string]bool{"legacy-random-id": true}

		plan := planPreflipSeedInserts(source, liveCounts, liveIDs)
		if len(plan) != 1 {
			t.Fatalf("plan length = %d, want 1 (only the missing copy)", len(plan))
		}
		if plan[0].id != rowid.New("events", 0, digestA) {
			t.Fatalf("planned id = %s, want lowest free ordinal 0 for the digest", plan[0].id)
		}
	})

	t.Run("skips ordinals an interrupted pass already took", func(t *testing.T) {
		source := map[string]preflipSourceDigest{digestC: {count: 3, sample: rowC}}
		liveCounts := map[string]int{digestC: 1}
		liveIDs := map[string]bool{rowid.New("events", 0, digestC): true}

		plan := planPreflipSeedInserts(source, liveCounts, liveIDs)
		want := []string{rowid.New("events", 1, digestC), rowid.New("events", 2, digestC)}
		if len(plan) != len(want) {
			t.Fatalf("plan length = %d, want %d", len(plan), len(want))
		}
		for i, id := range want {
			if plan[i].id != id {
				t.Fatalf("planned id[%d] = %s, want %s (occupied ordinals must be skipped)", i, plan[i].id, id)
			}
		}
	})

	t.Run("inserts nothing when live already covers the source", func(t *testing.T) {
		source := map[string]preflipSourceDigest{digestA: {count: 1, sample: rowA}}
		liveCounts := map[string]int{digestA: 2} // post-flip local writes added a copy
		if plan := planPreflipSeedInserts(source, liveCounts, map[string]bool{}); len(plan) != 0 {
			t.Fatalf("plan length = %d, want 0 (live content beyond the source is never reduced)", len(plan))
		}
	})

	t.Run("is deterministic across calls", func(t *testing.T) {
		source := map[string]preflipSourceDigest{
			digestA: {count: 2, sample: rowA},
			digestC: {count: 2, sample: rowC},
		}
		first := planPreflipSeedInserts(source, map[string]int{}, map[string]bool{})
		second := planPreflipSeedInserts(source, map[string]int{}, map[string]bool{})
		if len(first) != 4 || len(second) != 4 {
			t.Fatalf("plan lengths = %d, %d; want 4, 4", len(first), len(second))
		}
		for i := range first {
			if first[i].id != second[i].id || first[i].row.digest() != second[i].row.digest() {
				t.Fatalf("plan[%d] differs across calls: %+v vs %+v", i, first[i], second[i])
			}
		}
	})
}

func TestPreflipSeedDowngradeClassifiesHistoricalErrors(t *testing.T) {
	drift := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")

	t.Run("encoding drift becomes a skip", func(t *testing.T) {
		err := downgradePreflipHistoricalErr(context.Background(), "ref-x", drift)
		var skip *preflipSourceSkip
		if !errors.As(err, &skip) {
			t.Fatalf("drift error not downgraded to a skip: %v", err)
		}
		if skip.ref != "ref-x" || !strings.Contains(skip.reason, "invalid hash length") {
			t.Fatalf("skip = %+v, want ref ref-x and the drift reason", skip)
		}
	})

	t.Run("unrecognized errors propagate wrapped", func(t *testing.T) {
		boom := errors.New("Error 1146: table not found")
		err := downgradePreflipHistoricalErr(context.Background(), "ref-x", boom)
		var skip *preflipSourceSkip
		if errors.As(err, &skip) {
			t.Fatalf("unrecognized error downgraded to a skip: %v", err)
		}
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "ref-x") {
			t.Fatalf("propagated error = %v, want the original wrapped with its read ref", err)
		}
	})

	t.Run("cancellation is never downgraded", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := downgradePreflipHistoricalErr(ctx, "ref-x", context.Canceled)
		var skip *preflipSourceSkip
		if errors.As(err, &skip) {
			t.Fatalf("cancellation downgraded to a skip: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want it propagated", err)
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if err := downgradePreflipHistoricalErr(context.Background(), "ref-x", nil); err != nil {
			t.Fatalf("nil became %v", err)
		}
	})
}

func TestPreflipSeedSummaryAndWarningFormat(t *testing.T) {
	if got := formatPreflipSeedSummary(0, 0); got != "" {
		t.Fatalf("summary(0,0) = %q, want empty (nothing restored is not news)", got)
	}
	if got := formatPreflipSeedSummary(0, 5); got != "" {
		t.Fatalf("summary(0,5) = %q, want empty (orphans alone print nothing)", got)
	}
	if got := formatPreflipSeedSummary(3, 0); !strings.Contains(got, "restored 3") || strings.Contains(got, "deleted issues") {
		t.Fatalf("summary(3,0) = %q, want the restored count without an orphan clause", got)
	}
	if got := formatPreflipSeedSummary(3, 2); !strings.Contains(got, "restored 3") || !strings.Contains(got, "skipped 2 for deleted issues") {
		t.Fatalf("summary(3,2) = %q, want both counts", got)
	}

	warning := formatPreflipSkippedSourceWarning(&preflipSourceSkip{ref: "abc123", reason: "incompatible events shape, missing comment"})
	for _, want := range []string{"abc123", "missing comment", "docs/getting-started/upgrading.md"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning %q missing %q", warning, want)
		}
	}
}
