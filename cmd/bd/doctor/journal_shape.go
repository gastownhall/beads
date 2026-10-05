package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// journalOptionalColumns mirrors the optional-column list
// internal/storage/issueops/journal_shape.go's ProbeJournalShape treats as
// optional (BEADS-JOURNAL-PLAN.md §4.2a, PR A1). It is duplicated here
// rather than imported: this check reports over doctor's own plain
// *sql.DB (openDoltConn), and ProbeJournalShape prints its own
// once-per-probe STDERR warning as a side effect, which would duplicate
// this check's finding on every `bd doctor` run.
var journalOptionalColumns = []string{"actor", "issue_json", "dep_json", "comment_json"}

// CheckEventsJournalShape is the standalone entry point for the "Events
// Journal Shape" doctor check (T2.12, BEADS-JOURNAL-PLAN.md §4.2f, PR A2).
// It opens its own connection rather than joining RunDoltHealthChecks'
// shared one, the same standalone shape as CheckDoltRemoteGitOrigin and
// CheckMigrationContentSkew, so it can be registered in doctor.go beside
// them without touching runDoltHealthChecksInternal's fixed-shape branches.
func CheckEventsJournalShape(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Events Journal Shape",
			Status:   StatusOK,
			Message:  "N/A (non-Dolt backend)",
			Category: CategoryDolt,
		}
	}

	conn, err := openDoltConn(beadsDir)
	if err != nil {
		// Matches runDoltHealthChecksInternal's own no-server-running
		// tolerance: a store that hasn't been opened yet (server not started)
		// is not a journal-shape finding.
		return DoctorCheck{
			Name:     "Events Journal Shape",
			Status:   StatusOK,
			Message:  "Skipped (no server running; will auto-start on next bd command)",
			Category: CategoryDolt,
		}
	}
	defer conn.Close()

	return checkEventsJournalShapeWithDB(conn)
}

// checkEventsJournalShapeWithDB is T2.12 (BEADS-JOURNAL-PLAN.md §4.2f, PR
// A2): "bd doctor 'events journal shape'" reports:
//   - optional columns bd_events_journal is still missing. Adaptive I/O (PR
//     A1) keeps a table in this state writable and readable, dropping only
//     the affected payloads, but a dropped payload is still a finding — the
//     plan's own risk note says to "keep the doctor finding and a
//     once-per-store warning, never silence".
//   - the bd_events_seq counter sitting behind bd_events_journal's own
//     MAX(seq): the counter must never trail what has already been durably
//     written (nextEventSeq increments it inside the same transaction as the
//     row that carries its new value), so this can only mean the counter was
//     lost and reseeded (e.g. restored from a stale backup) without the rows
//     that came after it.
//
// The plan's design note (f) also lists "outbox depth and the age of its
// oldest row" and "a NULL epoch". Those refer to bd_events_outbox and
// bd_events_seq.epoch, introduced by PRs A5/A6 (§4.3's own table scopes
// T2.10/T2.11 to Postgres/PR B1 and T2.13 to PR A12; A5/A6 are likewise a
// separate, later plan item, §5) — neither object exists in this schema yet,
// so there is nothing to probe. Once they land, this is the function to
// extend.
func checkEventsJournalShapeWithDB(conn *doltConn) DoctorCheck {
	ctx := context.Background()

	exists, err := tableExists(ctx, conn.db, "bd_events_journal")
	if err != nil {
		return DoctorCheck{
			Name:     "Events Journal Shape",
			Status:   StatusWarning,
			Message:  "Could not probe bd_events_journal",
			Detail:   err.Error(),
			Category: CategoryDolt,
		}
	}
	if !exists {
		return DoctorCheck{
			Name:     "Events Journal Shape",
			Status:   StatusOK,
			Message:  "N/A (bd_events_journal not created yet)",
			Category: CategoryDolt,
		}
	}

	var findings []string
	var hasMissingColumns, hasUnfixableFinding bool

	missingOptional, err := missingJournalOptionalColumns(ctx, conn.db)
	if err != nil {
		findings = append(findings, fmt.Sprintf("could not read bd_events_journal's columns: %v", err))
		hasUnfixableFinding = true
	} else if len(missingOptional) > 0 {
		findings = append(findings, fmt.Sprintf(
			"bd_events_journal is missing optional column(s): %s (affected payloads are dropped on write)",
			strings.Join(missingOptional, ", ")))
		hasMissingColumns = true
	}

	// The counter-behind finding is deliberately NOT claimed as --fix-able,
	// but that is a scoping choice, not a capability gap: `--fix` (what
	// fix.EventsJournalShape/DatabaseVersion does) does not target this
	// finding directly, yet it is repaired as a side effect whenever the same
	// open replays ignored/0022 (any sentinel contradiction — see
	// schema.go's bd_events_journal sentinels), by the same GREATEST raise
	// the writer's own heal uses. A finding about the counter alone, with no
	// column also missing, triggers no such replay. The plan's design note
	// (f) only promises "--fix applies the converging DDL" — it says nothing
	// about reseeding a counter on its own, so this stays a report-only
	// finding in A2. Making it fixable on its own is an optional follow-up.
	counterBehind, counterDetail, err := journalCounterBehindMaxSeq(ctx, conn.db)
	if err != nil {
		findings = append(findings, fmt.Sprintf("could not compare bd_events_seq to MAX(seq): %v", err))
		hasUnfixableFinding = true
	} else if counterBehind {
		findings = append(findings, counterDetail+" (not repaired by 'bd doctor --fix'; needs manual investigation)")
		hasUnfixableFinding = true
	}

	if len(findings) == 0 {
		return DoctorCheck{
			Name:     "Events Journal Shape",
			Status:   StatusOK,
			Message:  "bd_events_journal shape converged",
			Category: CategoryDolt,
		}
	}

	check := DoctorCheck{
		Name:     "Events Journal Shape",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d finding(s)", len(findings)),
		Detail:   strings.Join(findings, "\n"),
		Category: CategoryDolt,
	}
	switch {
	case hasMissingColumns && hasUnfixableFinding:
		check.Fix = "Run 'bd doctor --fix' to add the missing column(s); the remaining finding(s) above need manual investigation"
	case hasMissingColumns:
		check.Fix = "Run 'bd doctor --fix' to apply the converging ignored-plane migration (adds comment_json/actor, widens columns, creates indexes)"
	}
	return check
}

// tableExists reports whether table exists in the connected database. A
// read-only probe (COUNT with LIMIT 1), matching checkSchemaWithDB's idiom
// elsewhere in this package.
func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s LIMIT 1", table)).Scan(&count) //nolint:gosec // table is one of this file's own constant strings, never user input
	if err != nil {
		// A missing table is not this function's error to report: every other
		// SQL failure is.
		if strings.Contains(strings.ToLower(err.Error()), "doesn't exist") ||
			strings.Contains(strings.ToLower(err.Error()), "not found") ||
			strings.Contains(strings.ToLower(err.Error()), "unknown table") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// missingJournalOptionalColumns runs the same INFORMATION_SCHEMA.COLUMNS
// probe internal/storage/issueops.ProbeJournalShape uses (over a plain
// connection, with no side-effecting warning), and returns which of
// journalOptionalColumns bd_events_journal does not carry.
func missingJournalOptionalColumns(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	present := make(map[string]bool, len(journalOptionalColumns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var missing []string
	for _, col := range journalOptionalColumns {
		if !present[col] {
			missing = append(missing, col)
		}
	}
	return missing, nil
}

// journalCounterBehindMaxSeq reports whether bd_events_seq's counter trails
// the highest seq actually recorded in bd_events_journal. bd_events_seq may
// not exist on a database that predates ignored/0019 (the events table) or
// has never journaled; that reads as "not behind" (nothing to compare),
// never as a finding.
func journalCounterBehindMaxSeq(ctx context.Context, db *sql.DB) (bool, string, error) {
	counterExists, err := tableExists(ctx, db, "bd_events_seq")
	if err != nil {
		return false, "", err
	}
	if !counterExists {
		return false, "", nil
	}

	var counter, maxSeq int64
	err = db.QueryRowContext(ctx, `
		SELECT
			(SELECT COALESCE(next_seq, 0) FROM bd_events_seq WHERE id = 0),
			(SELECT COALESCE(MAX(seq), 0) FROM bd_events_journal)
	`).Scan(&counter, &maxSeq)
	if err != nil {
		return false, "", err
	}

	if counter < maxSeq {
		return true, fmt.Sprintf(
			"bd_events_seq counter (%d) is behind bd_events_journal's MAX(seq) (%d): "+
				"the counter was likely lost and reseeded without the rows written after it",
			counter, maxSeq), nil
	}
	return false, "", nil
}
