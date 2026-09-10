package issueops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Provenance event log on types.ProvenanceEvent (migration 0063; see ADR-0003).

// knownProvKinds is the closed set of structurally-valid provenance kinds.
var knownProvKinds = map[types.ProvKind]struct{}{
	types.ProvCut:     {},
	types.ProvClaim:   {},
	types.ProvSuspend: {},
	types.ProvResume:  {},
	types.ProvHandoff: {},
	types.ProvCommit:  {},
	types.ProvLand:    {},
	types.ProvUsed:    {},
}

// knownProvRefKinds is the closed set of structurally-valid ref kinds.
var knownProvRefKinds = map[string]struct{}{
	"git-sha":    {},
	"pr":         {},
	"work-id":    {},
	"transcript": {},
	"branch":     {},
}

// ReservedProvSource is reserved for derived/reconstructed events so a consumer's
// read-first honesty filter can exclude backfilled rows. The record path rejects
// it (case-insensitively): real producers must name their own source.
const ReservedProvSource = "ingest-backfill"

var gitSHARE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateProvenanceEvent checks the structural fields of a provenance event
// before it is recorded: kind, ref_kind (when present), the git-sha ref shape,
// and the reserved source. It never interprets the opaque actor/ref values. It
// is exported so the CLI can fail early with the same rules the store enforces.
func ValidateProvenanceEvent(ev types.ProvenanceEvent) error {
	if strings.TrimSpace(ev.IssueID) == "" {
		return fmt.Errorf("provenance: issue id is required")
	}
	if _, ok := knownProvKinds[ev.Kind]; !ok {
		return fmt.Errorf("provenance: unknown kind %q", ev.Kind)
	}
	if strings.TrimSpace(ev.Source) == "" {
		return fmt.Errorf("provenance: source is required")
	}
	if strings.EqualFold(strings.TrimSpace(ev.Source), ReservedProvSource) {
		return fmt.Errorf("provenance: source %q is reserved for ingest backfill and cannot be recorded directly", ReservedProvSource)
	}
	if ev.RefKind != nil {
		if _, ok := knownProvRefKinds[*ev.RefKind]; !ok {
			return fmt.Errorf("provenance: unknown ref-kind %q", *ev.RefKind)
		}
		if ev.Ref == nil || *ev.Ref == "" {
			return fmt.Errorf("provenance: ref-kind %q requires a ref", *ev.RefKind)
		}
		if *ev.RefKind == "git-sha" {
			if !gitSHARE.MatchString(*ev.Ref) {
				return fmt.Errorf("provenance: ref-kind git-sha requires a 40-character lowercase hex ref")
			}
		}
	}
	// A ref-less event is keyed by occurred_at for its stable id; without either,
	// two distinct events would collapse to the same content-addressed id. Guard
	// at the store boundary so every caller (CLI or library) is covered.
	if (ev.Ref == nil || *ev.Ref == "") && ev.OccurredAt == nil {
		return fmt.Errorf("provenance: event with no ref requires occurred_at (--at) for a stable id")
	}
	return nil
}

// ProvenanceEventID computes the deterministic, idempotent id for a provenance
// event from source:issue:kind:(ref or occurred_at). A producer firing twice
// with the same facts yields the same id, so the INSERT IGNORE in
// RecordProvenanceEventInTx is a harmless no-op the second time. The
// discriminator is the ref when present, otherwise the fixed-width occurred_at,
// which is why a ref-less event requires --at (so the id is caller-owned, never
// clock-derived at insert time). ValidateProvenanceEvent enforces that.
func ProvenanceEventID(ev types.ProvenanceEvent) string {
	disc := ""
	switch {
	case ev.Ref != nil && *ev.Ref != "":
		disc = *ev.Ref
	case ev.OccurredAt != nil:
		// Truncate to whole seconds: occurred_at is stored as bare DATETIME
		// (second precision in dolt/MySQL), so the id basis must match the stored
		// value or an id re-derived from a stored row would diverge from the input.
		// Fixed-width .000000000 (not RFC3339Nano's variable width) on purpose:
		// a stable, deterministic discriminator regardless of trailing zeros.
		disc = ev.OccurredAt.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	key := strings.Join([]string{ev.Source, ev.IssueID, string(ev.Kind), disc}, ":")
	sum := sha256.Sum256([]byte(key))
	// Format the digest as a UUID-shaped CHAR(36) so it fits the id column.
	h := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// RecordProvenanceEventInTx validates and appends a provenance event. The id is
// always derived deterministically (content-addressed) — a caller-supplied
// ev.ID is ignored so the idempotency invariant cannot be bypassed. A duplicate
// insert is a no-op: inserted is false when the id already existed. Append-only
// at the event level — there is no UPDATE or DELETE of an individual event; an
// event is removed only if its issue is (ON DELETE CASCADE), like the events table.
func RecordProvenanceEventInTx(ctx context.Context, tx *sql.Tx, ev types.ProvenanceEvent) (id string, inserted bool, err error) {
	if err := ValidateProvenanceEvent(ev); err != nil {
		return "", false, err
	}
	id = ProvenanceEventID(ev)

	var occurredAt any
	if ev.OccurredAt != nil {
		// Truncate to whole seconds so the stored value matches the id basis
		// (the occurred_at column is bare DATETIME, second precision).
		occurredAt = ev.OccurredAt.UTC().Truncate(time.Second)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO provenance_events
			(id, issue_id, kind, actor, ref, ref_kind, payload, source, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, ev.IssueID, string(ev.Kind), NullStringPtr(ev.Actor), NullStringPtr(ev.Ref),
		NullStringPtr(ev.RefKind), NullStringPtr(ev.Payload), ev.Source, occurredAt)
	if err != nil {
		return "", false, fmt.Errorf("recording provenance event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("provenance rows affected: %w", err)
	}
	return id, affected == 1, nil
}

// GetProvenanceEventsInTx returns the provenance events for an issue, ordered by
// occurred_at (nulls last) then id. When kindFilter is non-empty, only events of
// that kind are returned.
func GetProvenanceEventsInTx(ctx context.Context, tx *sql.Tx, issueID, kindFilter string) ([]types.ProvenanceEvent, error) {
	query := `
		SELECT id, issue_id, kind, actor, ref, ref_kind, payload, source, occurred_at, created_at
		FROM provenance_events
		WHERE issue_id = ?
	`
	args := []any{issueID}
	if kindFilter != "" {
		query += " AND kind = ?"
		args = append(args, kindFilter)
	}
	query += " ORDER BY occurred_at IS NULL, occurred_at ASC, id ASC"

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying provenance events: %w", err)
	}
	defer rows.Close()
	return scanProvenanceEvents(rows)
}

// GetProvenanceByRefInTx returns every provenance event bound to the opaque ref,
// ordered by occurred_at (nulls last) then id.
func GetProvenanceByRefInTx(ctx context.Context, tx *sql.Tx, ref string) ([]types.ProvenanceEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, kind, actor, ref, ref_kind, payload, source, occurred_at, created_at
		FROM provenance_events
		WHERE ref = ?
		ORDER BY occurred_at IS NULL, occurred_at ASC, id ASC
	`, ref)
	if err != nil {
		return nil, fmt.Errorf("querying provenance by ref: %w", err)
	}
	defer rows.Close()
	return scanProvenanceEvents(rows)
}

func scanProvenanceEvents(rows *sql.Rows) ([]types.ProvenanceEvent, error) {
	var events []types.ProvenanceEvent
	for rows.Next() {
		var (
			ev         types.ProvenanceEvent
			kind       string
			actor      sql.NullString
			ref        sql.NullString
			refKind    sql.NullString
			payload    sql.NullString
			occurredAt sql.NullTime
		)
		if err := rows.Scan(&ev.ID, &ev.IssueID, &kind, &actor, &ref, &refKind,
			&payload, &ev.Source, &occurredAt, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning provenance event: %w", err)
		}
		ev.Kind = types.ProvKind(kind)
		if actor.Valid {
			ev.Actor = &actor.String
		}
		if ref.Valid {
			ev.Ref = &ref.String
		}
		if refKind.Valid {
			ev.RefKind = &refKind.String
		}
		if payload.Valid {
			ev.Payload = &payload.String
		}
		if occurredAt.Valid {
			t := occurredAt.Time
			ev.OccurredAt = &t
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}
