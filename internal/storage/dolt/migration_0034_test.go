package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestMigration0034_ColumnTypes verifies migration 0034 has widened the event
// payload columns from TEXT to LONGTEXT. These columns receive
// json.Marshal(oldIssue) and json.Marshal(updates) via RecordFullEventInTable
// (issueops/update.go:217-221); the original TEXT cap of 65535 bytes started
// blocking bd update on beads with large descriptions/notes (be-kkp).
func TestMigration0034_ColumnTypes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	cases := []struct {
		table  string
		column string
	}{
		{"events", "old_value"},
		{"events", "new_value"},
		{"wisp_events", "old_value"},
		{"wisp_events", "new_value"},
	}

	for _, c := range cases {
		t.Run(c.table+"."+c.column, func(t *testing.T) {
			cols := queryColumns(t, store, c.table)
			var got string
			for _, ci := range cols {
				if ci.Name == c.column {
					got = ci.ColumnType
					break
				}
			}
			if got == "" {
				t.Fatalf("%s.%s not found in information_schema", c.table, c.column)
			}
			if !strings.EqualFold(got, "longtext") {
				t.Fatalf("%s.%s column_type = %q; want longtext", c.table, c.column, got)
			}
		})
	}
}

// TestMigration0034_LargePayloadInsert proves the widened columns accept
// payloads that exceed the pre-migration 65535-byte TEXT cap. The bug surface
// in be-kkp was specifically the wisp_events path (designer rig / mc-ejzh.1),
// so the wisp_events case is the load-bearing assertion; the events check is
// a parity guard so future divergence between the two tables is caught.
//
// The events table has an FK on issues(id) so we seed a real issue first;
// wisp_events has no FK and accepts a synthetic id.
func TestMigration0034_LargePayloadInsert(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// 200 KiB — comfortably above the 65535-byte TEXT cap, well below
	// MEDIUMTEXT's 16 MiB so we'd notice if the migration silently picked
	// the wrong wider type.
	const payloadSize = 200 * 1024
	payload := strings.Repeat("x", payloadSize)

	issue := &types.Issue{
		Title:       "be-kkp longtext probe",
		Description: "anchor for events FK",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("seed issue for events FK: %v", err)
	}

	cases := []struct {
		table   string
		issueID string
	}{
		{"events", issue.ID},
		{"wisp_events", "be-kkp-wisp-probe"},
	}

	// Use a distinctive actor so the readback ignores the create event that
	// CreateIssue auto-records (which uses a different actor and would
	// otherwise collide on issue_id).
	const probeActor = "be-kkp-longtext-probe"

	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			//nolint:gosec // G201: table is a hardcoded constant
			_, err := store.db.ExecContext(ctx,
				"INSERT INTO "+c.table+" (issue_id, event_type, actor, old_value, new_value) VALUES (?, ?, ?, ?, ?)",
				c.issueID, "updated", probeActor, payload, payload)
			if err != nil {
				t.Fatalf("insert %d-byte payload into %s: %v", payloadSize, c.table, err)
			}

			//nolint:gosec // G201: table is a hardcoded constant
			row := store.db.QueryRowContext(ctx,
				"SELECT old_value, new_value FROM "+c.table+" WHERE issue_id = ? AND actor = ? LIMIT 1",
				c.issueID, probeActor)
			var gotOld, gotNew string
			if err := row.Scan(&gotOld, &gotNew); err != nil {
				t.Fatalf("read back %s row: %v", c.table, err)
			}
			if len(gotOld) != payloadSize || len(gotNew) != payloadSize {
				t.Fatalf("%s round-trip lost bytes: old=%d new=%d, want %d each",
					c.table, len(gotOld), len(gotNew), payloadSize)
			}
		})
	}
}

// TestMigration0034_RoundTrip exercises the down→up reversibility per the
// pattern be-eei §8 set for migration 0033. The down SQL must restore the
// pre-0034 TEXT type so a rollback leaves the schema in its prior shape; the
// up SQL must be safely re-runnable on a TEXT-typed column.
func TestMigration0034_RoundTrip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	assertType := func(phase, table, column, want string) {
		t.Helper()
		cols := queryColumns(t, store, table)
		for _, ci := range cols {
			if ci.Name != column {
				continue
			}
			if !strings.EqualFold(ci.ColumnType, want) {
				t.Fatalf("%s: %s.%s column_type = %q; want %q",
					phase, table, column, ci.ColumnType, want)
			}
			return
		}
		t.Fatalf("%s: %s.%s not found", phase, table, column)
	}

	targets := []struct{ table, column string }{
		{"events", "old_value"},
		{"events", "new_value"},
		{"wisp_events", "old_value"},
		{"wisp_events", "new_value"},
	}

	for _, tg := range targets {
		assertType("post-up", tg.table, tg.column, "longtext")
	}

	runMigrationSQL(t, ctx, store, downSQL0034)
	for _, tg := range targets {
		assertType("post-down", tg.table, tg.column, "text")
	}

	runMigrationSQL(t, ctx, store, upSQL0034)
	for _, tg := range targets {
		assertType("post-up-rerun", tg.table, tg.column, "longtext")
	}
}

// upSQL0034 / downSQL0034 mirror the embedded migration files. Kept inline so
// the test catches divergence if the .sql ever drifts from the shipped DDL,
// and so the round-trip test doesn't need to load embed.FS directly.
var upSQL0034 = []string{
	"ALTER TABLE events MODIFY old_value LONGTEXT",
	"ALTER TABLE events MODIFY new_value LONGTEXT",
	"ALTER TABLE wisp_events MODIFY old_value LONGTEXT DEFAULT ''",
	"ALTER TABLE wisp_events MODIFY new_value LONGTEXT DEFAULT ''",
}

var downSQL0034 = []string{
	"ALTER TABLE wisp_events MODIFY new_value TEXT DEFAULT ''",
	"ALTER TABLE wisp_events MODIFY old_value TEXT DEFAULT ''",
	"ALTER TABLE events MODIFY new_value TEXT",
	"ALTER TABLE events MODIFY old_value TEXT",
}
