package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// This file is the insert-time half of the content-derived aux-row ids
// (bd-ri8bd, Protocol v0.1 C-OQ3). The rekey backfill in
// internal/storage/schema converged the rows that existed when it shipped;
// every new events/comments/compaction_snapshots row now mints the same
// deterministic id at insert, so logically identical rows created
// independently on two replicas land on the same primary key and converge
// under merge-free (newest-wins) replication exactly as they do under the
// versioned union merge.
//
// The digest inputs are the FROZEN per-table column lists the backfill
// established (see auxRekeyTables in internal/storage/schema): deriving from
// any other column set would fork the id space the backfill converged.
// created_at participates as the DATETIME(0) text rendering, so insert sites
// stamp it app-side via NowAuxTime/FormatAuxTime and bind the string itself —
// what is stored is byte-for-byte what was digested.

// AuxTimeLayout renders a UTC timestamp exactly as Dolt renders
// CAST(created_at AS CHAR) for a DATETIME(0) column. It is part of the id
// derivation and frozen with the column lists.
const AuxTimeLayout = "2006-01-02 15:04:05"

// NowAuxTime returns the current UTC time in AuxTimeLayout.
func NowAuxTime() string {
	return time.Now().UTC().Format(AuxTimeLayout)
}

// FormatAuxTime renders t for digesting and binding as an aux-row created_at.
func FormatAuxTime(t time.Time) string {
	return t.UTC().Format(AuxTimeLayout)
}

// ParseAuxTime is the inverse of FormatAuxTime, for returning the stored
// timestamp to callers as a time.Time.
func ParseAuxTime(s string) (time.Time, error) {
	return time.ParseInLocation(AuxTimeLayout, s, time.UTC)
}

// AuxEvent is one events-plane row (events or wisp_events) in the frozen
// digest column order. OldValue/NewValue/Comment distinguish NULL from the
// empty string — the digest does too, so sites must pass exactly what they
// previously stored.
type AuxEvent struct {
	IssueID   string
	EventType types.EventType
	Actor     string
	OldValue  sql.NullString
	NewValue  sql.NullString
	Comment   sql.NullString
	CreatedAt string // AuxTimeLayout UTC; empty means "stamp now"
}

func str(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}

// firstFreeDerivedID returns the deterministic id for the lowest ordinal not
// already held by a same-content row. taken may include legacy random ids
// (rows minted before the derivation shipped, converged later by the rekey
// pass); those never collide with derived candidates, so the loop terminates
// within len(taken)+1 probes.
func firstFreeDerivedID(table, digest string, taken map[string]bool) string {
	for ordinal := 0; ; ordinal++ {
		id := rowid.New(table, ordinal, digest)
		if !taken[id] {
			return id
		}
	}
}

// InsertDerivedEvent inserts one events-plane row under its content-derived
// id. Exact-duplicate rows already in table (same digest) push the new row to
// the next free ordinal, preserving local multiplicity; across replicas an
// identical (digest, ordinal) pair is the same id, which is what makes
// independently-created identical rows converge (a deliberate set-union
// collapse, Protocol v0.1 C2.3).
//
// Ordinal selection assumes writes to ONE replica are serialized — true
// under both current topologies (the shared sql-server and the single-process
// embedded store). Two truly concurrent same-content transactions on one
// replica could pick the same free ordinal; the loser fails on the duplicate
// key rather than silently minting a colliding id, matching the pre-existing
// behavior of any duplicate primary-key insert. A future multi-writer
// embedding must revisit this (per the wyvern C-OQ3 review): the
// cross-replica collapse is by design, a within-replica collision is not.
//
//nolint:gosec // G201: table is a hardcoded routing constant at every call site.
func InsertDerivedEvent(ctx context.Context, tx DBTX, table string, e AuxEvent) error {
	_, err := InsertDerivedEventReturningID(ctx, tx, table, e)
	return err
}

// InsertDerivedEventReturningID is InsertDerivedEvent for callers that need the
// stored row's id — the events journal records it in a comment payload so a
// consumer can replay the audit comment idempotently without re-deriving bd's
// content digest.
//
//nolint:gosec // G201: table is a hardcoded routing constant at every call site.
func InsertDerivedEventReturningID(ctx context.Context, tx DBTX, table string, e AuxEvent) (string, error) {
	if e.CreatedAt == "" {
		e.CreatedAt = NowAuxTime()
	}
	if table == "wisp_events" {
		// wisp_events value columns DEFAULT '' (0021) where events defaults
		// them NULL; the pre-derivation mint sites omitted unset columns and
		// stored the default. Keep storing "" so the row — and its digest —
		// matches legacy wisp rows and the rekey pass's re-derivation.
		for _, p := range []*sql.NullString{&e.OldValue, &e.NewValue, &e.Comment} {
			if !p.Valid {
				*p = str("")
			}
		}
	}
	digest := rowid.Digest([]sql.NullString{
		str(e.IssueID), str(string(e.EventType)), str(e.Actor),
		e.OldValue, e.NewValue, e.Comment, str(e.CreatedAt),
	})
	taken := make(map[string]bool)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s
		WHERE issue_id = ? AND event_type = ? AND actor = ?
		  AND old_value <=> ? AND new_value <=> ? AND comment <=> ?
		  AND created_at = ?`, table),
		e.IssueID, string(e.EventType), e.Actor, e.OldValue, e.NewValue, e.Comment, e.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("scan same-content events in %s: %w", table, err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("scan same-content events in %s: %w", table, err)
		}
		taken[id] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan same-content events in %s: %w", table, err)
	}

	id := firstFreeDerivedID(table, digest, taken)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, table),
		id,
		e.IssueID, string(e.EventType), e.Actor, e.OldValue, e.NewValue, e.Comment, e.CreatedAt); err != nil {
		return "", fmt.Errorf("record event in %s: %w", table, err)
	}
	return id, nil
}

// NextLiveCommentTime returns the created_at to stamp on a comment being added
// live (as opposed to imported), given the wall-clock instant the caller
// observed. The result is always truncated to whole seconds — the created_at
// column's DATETIME(0) precision — and is advanced to one second past the
// issue's newest existing comment when that comment is at or after `now`.
//
// Why: comments read back in (created_at ASC, id ASC) order, created_at holds
// whole seconds, and since bd-ri8bd a comment's id is a content digest rather
// than a time-ordered UUIDv7. Two comments added to one issue inside the same
// wall-clock second therefore tie on the primary sort key and then order by
// hash — arbitrarily with respect to the order they were written. Keeping
// (issue_id, created_at) unique on the live path is what restores insertion
// order for the reader without putting ordering information into the id, which
// content-derivation cannot carry.
//
// This deliberately does NOT apply to the import path: an import carries the
// original timestamps and must not invent new ones. Same-second groups
// therefore still occur (imports, seeded/legacy rows, independently created
// rows on another replica), and the (created_at, id) keyset walk in
// GetIssueCommentsPageInTx remains the mechanism that keeps those groups
// consistent between paged and full reads.
//
// The cost is a bounded forward skew: a burst of N comments on one issue inside
// one second reads back spanning N seconds. That is a smaller distortion than N
// identical stamps in scrambled order, and it drains as wall-clock advances.
//
//nolint:gosec // G201: table is a hardcoded routing constant at every call site.
func NextLiveCommentTime(ctx context.Context, tx DBTX, table, issueID string, now time.Time) (time.Time, error) {
	now = now.UTC().Truncate(time.Second)
	var latest sql.NullTime
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT MAX(created_at) FROM %s WHERE issue_id = ?`, table), issueID).Scan(&latest); err != nil {
		return time.Time{}, fmt.Errorf("read newest comment time from %s: %w", table, err)
	}
	if !latest.Valid {
		return now, nil
	}
	newest := latest.Time.UTC().Truncate(time.Second)
	if newest.Before(now) {
		return now, nil
	}
	return newest.Add(time.Second), nil
}

// InsertDerivedComment inserts a comment under its content-derived id, or
// collapses onto an existing identical comment: a same-content row already in
// table (any id — it may predate the derivation) is the same logical comment,
// and the import path has always existence-checked exactly this column set
// (issue_id, author, text, created_at) rather than insert a duplicate. It
// returns the surviving row's id and whether it already existed.
//
//nolint:gosec // G201: table is a hardcoded routing constant at every call site.
func InsertDerivedComment(ctx context.Context, tx DBTX, table, issueID, author, text, createdAt string) (id string, existed bool, err error) {
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s
		WHERE issue_id = ? AND author = ? AND text = ? AND created_at = ?
		ORDER BY id LIMIT 1`, table),
		issueID, author, text, createdAt).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, fmt.Errorf("check comment existence in %s: %w", table, err)
	}
	digest := rowid.Digest([]sql.NullString{str(issueID), str(author), str(text), str(createdAt)})
	id = rowid.New(table, 0, digest)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)`, table),
		id, issueID, author, text, createdAt); err != nil {
		return "", false, fmt.Errorf("add comment to %s: %w", table, err)
	}
	return id, false, nil
}

// InsertDerivedCompactionSnapshot inserts a compaction_snapshots row under
// its content-derived id, with the same ordinal discipline as events. Two
// clones compacting the same issue at the same tier in the same second
// produce byte-identical snapshots and therefore the same id.
func InsertDerivedCompactionSnapshot(ctx context.Context, tx DBTX, issueID string, level int, snapshotJSON []byte, createdAt string) error {
	if createdAt == "" {
		createdAt = NowAuxTime()
	}
	snap := string(snapshotJSON)
	digest := rowid.Digest([]sql.NullString{
		str(issueID), str(fmt.Sprintf("%d", level)), str(snap), str(createdAt),
	})
	taken := make(map[string]bool)
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM compaction_snapshots
		WHERE issue_id = ? AND compaction_level = ? AND snapshot_json = ? AND created_at = ?`,
		issueID, level, snap, createdAt)
	if err != nil {
		return fmt.Errorf("scan same-content compaction snapshots: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan same-content compaction snapshots: %w", err)
		}
		taken[id] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan same-content compaction snapshots: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO compaction_snapshots (id, issue_id, compaction_level, snapshot_json, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		firstFreeDerivedID("compaction_snapshots", digest, taken),
		issueID, level, snap, createdAt); err != nil {
		return fmt.Errorf("insert compaction snapshot: %w", err)
	}
	return nil
}
