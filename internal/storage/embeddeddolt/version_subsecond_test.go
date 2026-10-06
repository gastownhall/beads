//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestRecordVersionKeepsSubSecondChangeAt pins the WRITER half of migration
// 0069's contract: a sub-second change_at must survive through
// RecordVersionAtInTx into the row and back.
//
// 0069 widened issue_versions.change_at to DATETIME(6) precisely so that two
// versions minted inside the same second stay distinguishable, because
// --at <instant> is the only PORTABLE selector the read surface offers while
// revision remains a per-store ordinal and no durable version address has
// shipped. The column's own half is pinned by
// TestMigration0069ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI in
// internal/storage/schema. The writer's half was NOT pinned, and that is
// exactly how it got defeated: recordVersionAtInTx carried an
// `at.Truncate(time.Second)` — correct while the column was precision 0,
// where Dolt rounds half-up — which kept flooring every value after the
// widening landed. A store with both applied stored four versions minted in
// one second as the identical 15:40:38.000000.
//
// So this test asserts the two things a caller actually depends on: the
// microseconds are there, and same-second versions differ.
func TestRecordVersionKeepsSubSecondChangeAt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := context.Background()
	store, cleanup, err := buildAsOfReadStore(ctx)
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	t.Cleanup(cleanup)

	h := newAsOfReadHarness(t)
	h.stores["subsecond"] = store

	const id = "vt-subsec"
	// Two instants inside the SAME second, a quarter of a second apart. Under
	// the old floor both landed on :07.000000; under a precision-0 column the
	// second would additionally have rounded up to :08.
	first := time.Date(2026, 9, 27, 11, 4, 7, 250000000, time.UTC)
	second := time.Date(2026, 9, 27, 11, 4, 7, 750000000, time.UTC)

	if _, err := h.mintAt(ctx, "subsecond", id, first); err != nil {
		t.Fatalf("mint at %s: %v", first, err)
	}
	if _, err := h.mintAt(ctx, "subsecond", id, second); err != nil {
		t.Fatalf("mint at %s: %v", second, err)
	}

	var stamps []time.Time
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT change_at FROM issue_versions WHERE issue_id = ? ORDER BY revision`, id)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var at time.Time
			if err := rows.Scan(&at); err != nil {
				return err
			}
			stamps = append(stamps, at)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read change_at back: %v", err)
	}

	if len(stamps) < 2 {
		t.Fatalf("got %d version rows for %s, want at least 2", len(stamps), id)
	}
	// The harness mints a creation version too, so take the last two.
	got := stamps[len(stamps)-2:]

	for i, at := range got {
		if at.Nanosecond() == 0 {
			t.Errorf("change_at[%d] = %s has a zero fractional part; the sub-second component was dropped between the caller and the row. "+
				"If recordVersionAtInTx floors `at` again, or the column reverts to DATETIME(0), --at can no longer separate versions minted in the same second.",
				i, at.Format("15:04:05.000000"))
		}
	}
	if got[0].Equal(got[1]) {
		t.Errorf("two versions minted 500ms apart both stored %s — same-second versions must stay distinguishable by change_at, "+
			"which is what migration 0069 widened the column for", got[0].Format("15:04:05.000000"))
	}
	if !got[1].After(got[0]) {
		t.Errorf("change_at went backwards or sideways: %s then %s",
			got[0].Format("15:04:05.000000"), got[1].Format("15:04:05.000000"))
	}
}
