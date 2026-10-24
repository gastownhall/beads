package versioncontrolops

import (
	"context"
	"reflect"
	"testing"
)

// After a quiet compaction the compacted head holds exactly the data of the
// head the plan was read from, so the comparison names no table. The answer
// is an empty slice, not nil, so `bd compact --json` writes [] for it.
func TestTablesChangedBetweenIsEmptyAfterQuietCompact(t *testing.T) {
	db := startCompactTestServer(t)
	ctx := context.Background()

	entries, err := Log(ctx, db, 0)
	if err != nil {
		t.Fatal(err)
	}
	planned := mainHead(t, db)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	recent := []string{entries[0].Hash}
	if err := CompactAtHead(ctx, conn, planned, entries[len(entries)-1].Hash, entries[1].Hash, len(entries)-1, recent); err != nil {
		t.Fatalf("CompactAtHead: %v", err)
	}
	compacted := mainHead(t, conn)
	if compacted == planned {
		t.Fatalf("compacted head equals the planned head %s: the squash did not rewrite history", planned)
	}

	changed, err := TablesChangedBetween(ctx, conn, planned, compacted)
	if err != nil {
		t.Fatalf("TablesChangedBetween: %v", err)
	}
	if changed == nil || len(changed) != 0 {
		t.Fatalf("tables changed by a quiet compact = %#v, want an empty non-nil slice", changed)
	}
}

// Positive control: the same read names every table whose data differs, a
// modified row, an added table and a dropped table alike, sorted by name.
func TestTablesChangedBetweenNamesEveryChangedTable(t *testing.T) {
	db := startCompactTestServer(t)
	ctx := context.Background()

	mustExec(t, db, "CREATE TABLE gone (id INT PRIMARY KEY)")
	mustExec(t, db, "CALL DOLT_COMMIT('-Am', 'create gone')")
	from := mainHead(t, db)

	mustExec(t, db, "UPDATE items SET v = 'rewritten' WHERE id = 2")
	mustExec(t, db, "CREATE TABLE added (id INT PRIMARY KEY)")
	mustExec(t, db, "DROP TABLE gone")
	mustExec(t, db, "CALL DOLT_COMMIT('-Am', 'rewrite a row, add a table, drop a table')")
	to := mainHead(t, db)

	changed, err := TablesChangedBetween(ctx, db, from, to)
	if err != nil {
		t.Fatalf("TablesChangedBetween: %v", err)
	}
	if want := []string{"added", "gone", "items"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("tables changed = %#v, want %#v", changed, want)
	}

	same, err := TablesChangedBetween(ctx, db, to, to)
	if err != nil {
		t.Fatalf("TablesChangedBetween(to, to): %v", err)
	}
	if len(same) != 0 {
		t.Fatalf("tables changed between a commit and itself = %#v, want none", same)
	}
}
