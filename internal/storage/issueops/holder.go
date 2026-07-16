package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// EventOwnershipAdvisory is recorded (advisory enforcement mode) when an
// in-place mutation of a claimed row is made by something other than the
// current holder. Its new_value carries the AdvisoryClass so the require-mode
// rollout can be gated on the class distribution — token_mismatch (real
// zombies) driven to zero, cross_actor_infra converted to guarded verbs.
const EventOwnershipAdvisory types.EventType = "ownership_advisory"

// AdvisoryClass labels why an in-place mutation did not come from the current
// holder.
type AdvisoryClass string

const (
	// AdvisoryTokenMismatch: the caller's actor equals the assignee but its
	// holder token differs from the recorded one — the genuine zombie signal
	// (a stale incarnation of a re-used session name).
	AdvisoryTokenMismatch AdvisoryClass = "token_mismatch"
	// AdvisoryCrossActor: the caller's actor differs from the assignee — an
	// orchestrator/infra write. Noise for the zombie question; the require
	// flip requires these converted to guarded transition verbs.
	AdvisoryCrossActor AdvisoryClass = "cross_actor_infra"
	// AdvisoryEmptyTokenLegacy: the row carries no holder token (claimed
	// before tokens) but a token-bearing caller mutated it — the upgrade
	// class, tracked separately from a genuine mismatch.
	AdvisoryEmptyTokenLegacy AdvisoryClass = "empty_token_legacy"
	// AdvisoryTokenlessCaller: a tokenless caller (a human via bd, or any
	// tool without BEADS_HOLDER_TOKEN) mutated a token-bearing claim. A stale
	// incarnation carries its own token, so an EMPTY caller token is not a
	// zombie signal — it is separated from token_mismatch so the require-flip
	// gate reads a clean zombie count.
	AdvisoryTokenlessCaller AdvisoryClass = "tokenless_caller"
)

// EnforcementConfigKey governs holder-token enforcement. off (default): the
// token is recorded on claim but never checked. advisory: mismatched in-place
// mutations still land, but emit an EventOwnershipAdvisory labeled by class so
// operators can measure the mismatch population before turning on require
// mode (a later slice). require is intentionally NOT handled here.
const EnforcementConfigKey = "claims.enforcement"

// ReservedHolderToken is a sentinel that matches no live incarnation. A
// handoff that has no recipient token yet stamps it so the row refuses every
// token (never an empty actor-only fallback) until a real claim re-stamps.
const ReservedHolderToken = "!"

type holderTokenCtxKey struct{}

// WithHolderToken attaches the caller's ambient incarnation token to the
// context, the same transport WithLeaseTTL/WithGuard use. Claim records it;
// advisory enforcement compares against it. Empty tokens are not attached.
func WithHolderToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, holderTokenCtxKey{}, token)
}

// holderTokenFrom returns the ambient holder token, if any.
func holderTokenFrom(ctx context.Context) string {
	if t, ok := ctx.Value(holderTokenCtxKey{}).(string); ok {
		return t
	}
	return ""
}

// HolderTokenFrom exposes the ambient holder token to the sibling proxied
// (domain/db) claim path so it records the token with the same discipline.
func HolderTokenFrom(ctx context.Context) string { return holderTokenFrom(ctx) }

// enforcementAdvisory reports whether advisory enforcement is enabled for this
// store. A config-read failure is propagated (never guessed): enforcement is a
// safety knob. off/unset/unrecognized ⇒ not advisory.
func enforcementAdvisory(ctx context.Context, tx DBTX) (bool, error) {
	v, err := GetConfigInTx(ctx, tx, EnforcementConfigKey)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", EnforcementConfigKey, err)
	}
	return strings.ToLower(strings.TrimSpace(v)) == "advisory", nil
}

// RecordOwnershipAdvisoryIfMismatch emits an EventOwnershipAdvisory when an
// in-place mutation of a row that was claimed BEFORE the write did not come
// from the current holder, under advisory enforcement. It never blocks — the
// caller has already written. Scope is decided from the pre-mutation
// (assignee, status) so an ownership-transitioning write (which the caller
// excludes) is not misjudged; holder_token is read fresh (an in-place
// mutation leaves it unchanged). Returns nil when enforcement is off, the row
// was not a claimed row, or the caller is the clean holder.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func RecordOwnershipAdvisoryIfMismatch(ctx context.Context, tx DBTX, issueTable, eventTable, id, actor, oldAssignee string, oldStatus types.Status) error {
	// Only rows that were claimed before this write are in scope.
	if oldStatus != types.StatusInProgress || oldAssignee == "" {
		return nil
	}
	advisory, err := EnforcementIsAdvisory(ctx, tx)
	if err != nil {
		return err
	}
	if !advisory {
		return nil
	}
	return recordOwnershipAdvisory(ctx, tx, issueTable, eventTable, id, actor, oldAssignee)
}

// EnforcementIsAdvisory reports whether advisory enforcement is on. Exported
// so a caller that must capture pre-mutation state (close, which loses the
// old status) can gate that read on enforcement without a second config read.
func EnforcementIsAdvisory(ctx context.Context, tx DBTX) (bool, error) {
	return enforcementAdvisory(ctx, tx)
}

// recordOwnershipAdvisory assumes advisory is on and the row was claimed
// before the write; it reads the (unchanged-by-in-place-mutation) holder
// token, classifies, and records the event.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func recordOwnershipAdvisory(ctx context.Context, tx DBTX, issueTable, eventTable, id, actor, oldAssignee string) error {
	var holder string
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT holder_token FROM %s WHERE id = ?`, issueTable), id).Scan(&holder); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("advisory read for %s: %w", id, err)
	}
	class := classifyAdvisory(actor, oldAssignee, holder, holderTokenFrom(ctx))
	if class == "" {
		return nil // clean holder
	}
	// old_value MUST NOT carry the token: events are readable via `bd history`,
	// so recording the holder token there would let any reader (a fenced-out
	// zombie included) recover the current token, defeating the never-surfaced
	// invariant (D12). The class alone is the signal.
	return RecordFullEventInTable(ctx, tx, eventTable, id, EventOwnershipAdvisory, actor, "", string(class))
}

// classifyAdvisory returns the advisory class for an in-place mutation, or ""
// (no event) when the caller is the clean current holder. token_mismatch is
// reserved strictly for two token-bearing incarnations disagreeing — the
// genuine zombie — so the require-flip gate can read a clean count.
func classifyAdvisory(actor, assignee, rowToken, callerToken string) AdvisoryClass {
	if actor != assignee {
		return AdvisoryCrossActor
	}
	// actor == assignee from here.
	if callerToken == rowToken {
		// Matching tokens, including a consistent tokenless deployment
		// (both empty) — the holder writing its own bead. No event.
		return ""
	}
	if rowToken == "" {
		return AdvisoryEmptyTokenLegacy // token-bearing worker on a pre-token claim
	}
	if callerToken == "" {
		return AdvisoryTokenlessCaller // tokenless tool/human on a token-bearing claim
	}
	return AdvisoryTokenMismatch // both non-empty and differ: the genuine zombie
}
