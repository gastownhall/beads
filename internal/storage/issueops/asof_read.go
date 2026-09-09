package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file implements R7.1 as-of read (gastownhall/beads#5898 revision 9,
// gastownhall/beads#6136, this slice: be-x5jqd.5 / backend/conformance/
// versioned_read_contract.go): given an instant T or a version address, it
// returns one issue's durable state as of that point -- the latest version
// accepted at or before T, with everything later excluded -- marking the
// address it served, or refuses as gone or unknown with the reason (R8,
// R20). It reads issue_versions rows minted by RecordVersionInTx /
// RecordVersionAtInTx (version_history.go); it adds no writer of its own for
// the mutation path.
//
// NOT part of this primitive (R7.1's own text): memory-plane consumer
// policy -- closed-before-T boundary, sibling exclusion, supersession
// exclusion (gastownhall/beads#5877). Those are layered on top by a
// consumer, not implemented here.
//
// ALL THREE LEGS SHARE THIS BODY, the same way CompareAndSetMetadataKeyInTx,
// R16's CompareAndSetVersionInTx, and R20's epoch_cas.go do: the two
// Dolt-backed stores wrap it in their own transaction, and the
// unit-of-work provider reaches it through its own leg-specific adapter.
// Each leg's own contract-test file is the translation boundary to and from
// backend/conformance's Address/Restriction vocabulary -- this file knows
// nothing about the conformance package (same package-boundary precedent as
// epoch_cas.go's EpochRestriction).
//
// removed_at / removed_reason (migration 0067, virgin and unused before this
// slice) plus removed_restriction (migration 0069, added by this slice) are
// the durable "this version row was removed" marker AsOfReadInTx reads.
// R7.1 only READS them here; a future R17 (not yet implemented on this
// branch) will be the production WRITER, via real hold/remove/erase
// primitives -- no collision, since this file never writes them outside the
// test-support seam below.
//
// STOREID IS ACCEPTED BUT NOT A FILTER, the same way CurrentEpochInTx
// accepts one: issue_versions has no store_id column, and a store's rows are
// already exactly the rows in that store's own database (one logical store
// per database, matching production reality). The parameter exists only so
// this function's signature matches AsOfReadFixture.Resolve's hook shape,
// which the conformance suite's own ad-hoc dispatch string requires (Design
// D: the suite invents storeID values to select among N genuinely isolated
// per-leg test stores; the leg fixture, not this function, does that
// isolation).

// AsOfRestriction is this file's own local answer vocabulary for R7.1's
// refusal path -- deliberately not backend/conformance.Restriction (see the
// package-boundary note above). Unlike R20's EpochRestriction (int-backed:
// R20 never persists a restriction, it recomputes GoneReorganization at read
// time from an epoch comparison), this type is STRING-backed: it is written
// directly into issue_versions.removed_restriction and read back with a
// plain cast, no separate parse/format step needed. AsOfRestrictionLive
// exists only for API symmetry with EpochRestriction's shape -- it is never
// persisted (Live is the absence of a value, removed_at IS NULL, not a
// stored one) and AsOfReadInTx never returns it.
type AsOfRestriction string

const (
	AsOfRestrictionLive               AsOfRestriction = "live"
	AsOfRestrictionGoneRetention      AsOfRestriction = "gone_retention"
	AsOfRestrictionGoneErasure        AsOfRestriction = "gone_erasure"
	AsOfRestrictionGoneReorganization AsOfRestriction = "gone_reorganization"
	AsOfRestrictionUnknown            AsOfRestriction = "unknown"
)

// AsOfSelector selects a point in an issue's version history to read: either
// a literal instant (At) or a previously-served version address (Address) --
// exactly one populated, mirroring the duality
// backend/conformance.AsOfSelector exposes to its own callers. This file
// owns its own copy rather than importing conformance's type (see the
// package-boundary note above).
type AsOfSelector struct {
	At      *time.Time
	Address string
}

// AsOfReadResult is AsOfReadInTx's answer. State and Address are populated
// only on a served (non-refused) result; Restriction and Reason only on a
// refused one.
type AsOfReadResult struct {
	Address     string
	State       map[string]any
	Refused     bool
	Restriction AsOfRestriction
	Reason      string
}

// asOfVersionAddress is the address AsOfReadInTx serves for a resolved
// revision: a deterministic token over (issueID, revision). storeID is not
// part of the token -- a version address is meaningful only against its own
// issueID's rows, and Resolve is always called with issueID already known
// as its own separate parameter (matching the conformance fixture's own hook
// shape) -- but issueID is embedded anyway for readability/debuggability,
// per the design's stated rationale.
func asOfVersionAddress(issueID string, revision int64) string {
	return fmt.Sprintf("asof:%s:%d", issueID, revision)
}

// parseAsOfVersionAddress reverses asOfVersionAddress. ok is false for any
// address not shaped like one this file mints, INCLUDING one whose embedded
// issueID does not match the issueID the caller separately supplied -- a
// version address for a different issue is never valid in that caller's
// context.
func parseAsOfVersionAddress(address, issueID string) (revision int64, ok bool) {
	parts := strings.SplitN(address, ":", 3)
	if len(parts) != 3 || parts[0] != "asof" || parts[1] != issueID {
		return 0, false
	}
	rev, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false
	}
	return rev, true
}

// resolveAsOfRevisionInTx resolves selector to the one issue_versions
// revision it names, for issueID: the version-address branch parses the
// revision straight out of the token, the instant branch is
//
//	SELECT revision FROM issue_versions
//	WHERE issue_id = ? AND change_at <= ?
//	ORDER BY change_at DESC, revision DESC LIMIT 1
//
// -- the latest version accepted at or before T, with everything later
// strictly excluded (R7.1's own wording); revision DESC is a deterministic
// tie-break for two versions minted in the same instant. found is false
// when no row resolves either way -- the address named nothing this store
// holds, or no version of issueID existed yet at T -- which the caller
// reports as Unknown, never substituting a neighboring version (R8).
func resolveAsOfRevisionInTx(ctx context.Context, tx DBTX, issueID string, selector AsOfSelector) (revision int64, found bool, err error) {
	if selector.Address != "" {
		rev, ok := parseAsOfVersionAddress(selector.Address, issueID)
		if !ok {
			return 0, false, nil
		}
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM issue_versions WHERE issue_id = ? AND revision = ?`, issueID, rev,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("as-of read: resolve address for %s: %w", issueID, err)
		}
		return rev, true, nil
	}

	if selector.At == nil {
		return 0, false, fmt.Errorf("as-of read: selector for %s has neither an instant nor a version address", issueID)
	}
	var rev int64
	err = tx.QueryRowContext(ctx,
		`SELECT revision FROM issue_versions WHERE issue_id = ? AND change_at <= ? ORDER BY change_at DESC, revision DESC LIMIT 1`,
		issueID, *selector.At,
	).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("as-of read: resolve instant for %s: %w", issueID, err)
	}
	return rev, true, nil
}

// AsOfReadInTx answers R7.1's as-of read for issueID under selector: it
// resolves selector to one issue_versions row (resolveAsOfRevisionInTx),
// then reports that SAME row's own removed_at/removed_reason/
// removed_restriction. Asking as-of an earlier T than when a later removal
// happened is therefore not a refusal -- the earlier row itself was never
// marked removed, only whichever row a later MarkLatestVersionRemovedInTx
// call touched was.
//
// storeID is accepted only for signature parity (see the package doc above);
// it plays no role in resolution or scoping.
func AsOfReadInTx(ctx context.Context, tx DBTX, storeID, issueID string, selector AsOfSelector) (AsOfReadResult, error) {
	_ = storeID

	revision, found, err := resolveAsOfRevisionInTx(ctx, tx, issueID, selector)
	if err != nil {
		return AsOfReadResult{}, err
	}
	if !found {
		return AsOfReadResult{
			Refused:     true,
			Restriction: AsOfRestrictionUnknown,
			Reason:      fmt.Sprintf("no version of %s found at or before the requested point", issueID),
		}, nil
	}

	var durableState []byte
	var removedAt sql.NullTime
	var removedReason sql.NullString
	var removedRestriction sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT durable_state, removed_at, removed_reason, removed_restriction
		 FROM issue_versions WHERE issue_id = ? AND revision = ?`,
		issueID, revision,
	).Scan(&durableState, &removedAt, &removedReason, &removedRestriction)
	if errors.Is(err, sql.ErrNoRows) {
		return AsOfReadResult{
			Refused:     true,
			Restriction: AsOfRestrictionUnknown,
			Reason:      fmt.Sprintf("no version of %s found at or before the requested point", issueID),
		}, nil
	}
	if err != nil {
		return AsOfReadResult{}, fmt.Errorf("as-of read: load revision %d for %s: %w", revision, issueID, err)
	}

	if removedAt.Valid {
		restriction := AsOfRestriction(removedRestriction.String)
		if restriction == "" {
			restriction = AsOfRestrictionUnknown
		}
		return AsOfReadResult{
			Refused:     true,
			Restriction: restriction,
			Reason:      removedReason.String,
		}, nil
	}

	state := map[string]any{}
	if len(durableState) > 0 {
		if err := json.Unmarshal(durableState, &state); err != nil {
			return AsOfReadResult{}, fmt.Errorf("as-of read: unmarshal durable state for %s revision %d: %w", issueID, revision, err)
		}
	}
	return AsOfReadResult{Address: asOfVersionAddress(issueID, revision), State: state}, nil
}

// MarkLatestVersionRemovedInTx is a test-support seam for R7.1's
// MakeUnanswerable fixture hook: it marks issueID's LATEST version row (by
// revision, not by time) as removed under restriction, independent of
// R17/R20's real (not yet implemented) hold/remove/erase primitives.
// "Latest" is correct for every AsOfReadContract case: each mints exactly
// one version before calling this, so the latest row and the row under test
// always coincide. Production code never calls this; only per-leg
// AsOfReadFixture.MakeUnanswerable implementations do.
func MarkLatestVersionRemovedInTx(ctx context.Context, tx DBTX, issueID string, restriction AsOfRestriction, reason string) error {
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1`, issueID,
	).Scan(&revision); err != nil {
		return fmt.Errorf("as-of read: find latest version for %s: %w", issueID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issue_versions SET removed_at = ?, removed_reason = ?, removed_restriction = ? WHERE issue_id = ? AND revision = ?`,
		time.Now().UTC(), reason, string(restriction), issueID, revision,
	); err != nil {
		return fmt.Errorf("as-of read: mark version removed for %s: %w", issueID, err)
	}
	return nil
}
