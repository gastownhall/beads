package issueops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// This file implements R16/R17 (gastownhall/beads#5898 revision 9, this
// slice: be-x5jqd.3 / #6133): a whole-of-state, per-record compare-and-set
// write over an id-keyed record store, backed by expected_revision_records
// (migration 0069). It is independent of issue_versions/RecordVersionInTx
// (version_history.go, #6135/#6358/#6379, BDP#18/20/21) — a disjoint
// Phase-2 issue-versioning design track that this file neither reads nor
// writes.
//
// ALL THREE LEGS SHARE THIS BODY, the same way CompareAndSetMetadataKeyInTx
// does: the two Dolt-backed stores wrap it in their own transaction, and the
// unit-of-work provider reaches it through the domain issue repository. Each
// leg's own contract-test file is the translation boundary to and from
// backend/conformance's Address/ChangeAttribution/Refusal vocabulary
// (including the "_attribution" patch-key convention) — this file knows
// nothing about the conformance package.
//
// ADDRESS IS NEVER STORED. It is always sha256 of the record's current
// durable_state bytes, recomputed on demand by expectedRevisionAddress, so
// there is no separate address column that could drift from the bytes it
// names.
//
// WHOLE-OF-STATE (R16-c) IS STRUCTURAL, NOT SPECIAL-CASED: a record is one
// durable_state LONGBLOB holding the JSON encoding of a generic
// map[string]any, and every write path — CompareAndSetVersionInTx's patch
// merge and MutateExpectedRevisionFieldInTx's single-field merge alike —
// merges into and re-persists that SAME blob. There is exactly one durable
// representation per record, so anything that changes it changes the
// Address, regardless of which path touched it.

// ExpectedRevisionAttribution, ExpectedRevisionRefusal and
// CompareAndSetVersionPlan/Result live in internal/storage, not here — see
// internal/storage/expected_revision_cas.go's header comment for the
// dependency-direction rationale (the same one CompareAndSetKeyPlan already
// follows in that package).

// ErrExpectedRevisionNotFound means the write named an id with no current
// version at all — distinct from a Refusal, which requires a current
// version to refuse against (R17-d2).
var ErrExpectedRevisionNotFound = errors.New("expected-revision: id has no current version")

// ErrExpectedRevisionValidation means the write itself was malformed,
// independent of whether its expectation held (R17-d3).
var ErrExpectedRevisionValidation = errors.New("expected-revision: request is invalid")

// expectedRevisionAddress is a record's Address: a content-derived token
// (R5.1) over exactly the bytes durable_state holds. LONGBLOB (not JSON)
// backs the column so these are always the bytes this function hashes, with
// no renormalization between write and read.
func expectedRevisionAddress(durableState []byte) string {
	sum := sha256.Sum256(durableState)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// normalizedExpectedRevisionAt substitutes time.Now().UTC() for a zero-value
// time.Time. attribution.At defaults to time.Time{} (year 1) whenever a
// caller supplies no explicit attribution, and time.Time.UnixNano() is
// undefined behavior outside ~1678-2262 — silently wrapping a default
// attribution into change_at_nanos would corrupt the row.
func normalizedExpectedRevisionAt(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at
}

// expectedRevisionRow is one record as read from expected_revision_records.
type expectedRevisionRow struct {
	durableState []byte
	attribution  storage.ExpectedRevisionAttribution
}

// readExpectedRevisionRowInTx reads id's current row, if any. found is false
// (with a zero-value row and nil error) when id has no current version —
// distinguishing "not found" from an actual query error is the caller's job,
// since the two mean different things to CompareAndSetVersionInTx and to
// CurrentExpectedRevisionInTx.
func readExpectedRevisionRowInTx(ctx context.Context, tx DBTX, id string) (expectedRevisionRow, bool, error) {
	var state []byte
	var actor, agent, message sql.NullString
	var atNanos int64
	err := tx.QueryRowContext(ctx,
		`SELECT durable_state, change_actor, change_agent, change_message, change_at_nanos
		 FROM expected_revision_records WHERE id = ?`, id,
	).Scan(&state, &actor, &agent, &message, &atNanos)
	if errors.Is(err, sql.ErrNoRows) {
		return expectedRevisionRow{}, false, nil
	}
	if err != nil {
		return expectedRevisionRow{}, false, fmt.Errorf("expected-revision CAS: read %s: %w", id, err)
	}
	return expectedRevisionRow{
		durableState: state,
		attribution: storage.ExpectedRevisionAttribution{
			Actor:   nullExpectedRevisionString(actor),
			Agent:   nullExpectedRevisionString(agent),
			Message: nullExpectedRevisionString(message),
			At:      time.Unix(0, atNanos),
		},
	}, true, nil
}

func nullExpectedRevisionString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func expectedRevisionNullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

// upsertExpectedRevisionRowInTx writes id's row via an explicit exists-check
// branch (SELECT already ran in the caller; exists says which branch to
// take) rather than ON DUPLICATE KEY UPDATE, whose support is unverified on
// this codebase's Dolt/GMS version.
func upsertExpectedRevisionRowInTx(ctx context.Context, tx DBTX, id string, state []byte, attribution storage.ExpectedRevisionAttribution, exists bool) error {
	actor := expectedRevisionNullString(attribution.Actor)
	agent := expectedRevisionNullString(attribution.Agent)
	message := expectedRevisionNullString(attribution.Message)
	atNanos := attribution.At.UnixNano()

	if exists {
		if _, err := tx.ExecContext(ctx,
			`UPDATE expected_revision_records
			 SET durable_state = ?, change_actor = ?, change_agent = ?, change_message = ?, change_at_nanos = ?
			 WHERE id = ?`,
			state, actor, agent, message, atNanos, id,
		); err != nil {
			return fmt.Errorf("expected-revision CAS: update %s: %w", id, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO expected_revision_records
			(id, durable_state, change_actor, change_agent, change_message, change_at_nanos)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, state, actor, agent, message, atNanos,
	); err != nil {
		return fmt.Errorf("expected-revision CAS: insert %s: %w", id, err)
	}
	return nil
}

// CompareAndSetVersionInTx is the body behind R16's whole-of-state
// per-record CAS write, shared by all three legs the same way
// CompareAndSetMetadataKeyInTx is: plan.ID's entire durable state (not one
// watched field) is read, compared, and rewritten within ONE transaction.
//
// VALIDATION RUNS FIRST, before the not-found and nil-expected branches
// (R17-d3): an empty plan.ID is ErrExpectedRevisionValidation even when
// plan.Expected is also nil.
//
// A nil plan.Expected means "no expectation" (R16-b), which reaches two
// different outcomes depending on whether id already has a row: over a
// fresh id it CREATES one (this is the only write path that can mint a
// record's first version), and over an existing row it is an unconditional
// accept. A non-nil plan.Expected against a fresh id cannot be a Refusal —
// R17-d2 requires a current version to refuse against — so it is
// ErrExpectedRevisionNotFound instead.
func CompareAndSetVersionInTx(
	ctx context.Context,
	tx DBTX,
	plan storage.CompareAndSetVersionPlan,
) (storage.CompareAndSetVersionResult, error) {
	if plan.ID == "" {
		return storage.CompareAndSetVersionResult{}, fmt.Errorf("expected-revision CAS: %w: id is empty", ErrExpectedRevisionValidation)
	}

	row, found, err := readExpectedRevisionRowInTx(ctx, tx, plan.ID)
	if err != nil {
		return storage.CompareAndSetVersionResult{}, err
	}
	if !found && plan.Expected != nil {
		return storage.CompareAndSetVersionResult{}, fmt.Errorf("expected-revision CAS: %w: %s", ErrExpectedRevisionNotFound, plan.ID)
	}

	state := map[string]any{}
	if found {
		currentAddress := expectedRevisionAddress(row.durableState)
		if plan.Expected != nil && *plan.Expected != currentAddress {
			return storage.CompareAndSetVersionResult{
				Refusal: &storage.ExpectedRevisionRefusal{
					RefusingVersion: currentAddress,
					Attribution:     row.attribution,
				},
			}, nil
		}
		if err := json.Unmarshal(row.durableState, &state); err != nil {
			return storage.CompareAndSetVersionResult{}, fmt.Errorf("expected-revision CAS: decode durable state for %s: %w", plan.ID, err)
		}
	}
	for k, v := range plan.Patch {
		state[k] = v
	}

	newState, err := json.Marshal(state)
	if err != nil {
		return storage.CompareAndSetVersionResult{}, fmt.Errorf("expected-revision CAS: encode durable state for %s: %w", plan.ID, err)
	}

	attribution := plan.Attribution
	attribution.At = normalizedExpectedRevisionAt(attribution.At)
	if err := upsertExpectedRevisionRowInTx(ctx, tx, plan.ID, newState, attribution, found); err != nil {
		return storage.CompareAndSetVersionResult{}, err
	}

	return storage.CompareAndSetVersionResult{Accepted: true, NewVersion: expectedRevisionAddress(newState)}, nil
}

// CurrentExpectedRevisionInTx reports the Address currently current for id.
func CurrentExpectedRevisionInTx(ctx context.Context, tx DBTX, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("expected-revision CAS: %w: id is empty", ErrExpectedRevisionValidation)
	}
	row, found, err := readExpectedRevisionRowInTx(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("expected-revision CAS: %w: %s", ErrExpectedRevisionNotFound, id)
	}
	return expectedRevisionAddress(row.durableState), nil
}

// MutateExpectedRevisionFieldInTx writes field=value into id's durable state
// through a path NOT gated by the whole-of-state precondition — test-only,
// used to probe R16-c (the whole-of-state precondition must cover fields
// outside any single watched subset). It merges into the SAME durable_state
// blob CompareAndSetVersionInTx reads and writes, which is what makes the
// Address structurally forced to advance regardless of which path changed
// the record; it bypasses only the compare-expected check, not the storage
// location. The existing row's attribution is preserved (an out-of-band
// write asserts no attribution of its own); a never-seeded id gets a fresh
// row with a zero attribution, its At normalized the same as any other
// write.
func MutateExpectedRevisionFieldInTx(ctx context.Context, tx DBTX, id, field, value string) error {
	row, found, err := readExpectedRevisionRowInTx(ctx, tx, id)
	if err != nil {
		return err
	}
	state := map[string]any{}
	if found {
		if err := json.Unmarshal(row.durableState, &state); err != nil {
			return fmt.Errorf("expected-revision CAS: decode durable state for %s: %w", id, err)
		}
	}
	state[field] = value

	newState, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("expected-revision CAS: encode durable state for %s: %w", id, err)
	}

	row.attribution.At = normalizedExpectedRevisionAt(row.attribution.At)
	return upsertExpectedRevisionRowInTx(ctx, tx, id, newState, row.attribution, found)
}
