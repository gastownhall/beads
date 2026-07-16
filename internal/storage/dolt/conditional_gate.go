package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// AllowUnsafeCASEnv, when set to a boolean true ("1", "true", ...), forces
// whole-row conditional writes despite the topology gate below. It exists for an
// operator who knows their deployment is a single linear writer even though a
// remote is configured (e.g. a disaster-recovery mirror that is never pulled
// from). Consulted only when the gate would otherwise fire.
const AllowUnsafeCASEnv = "BD_ALLOW_UNSAFE_CAS"

// ConditionalWriteGateError reports that whole-row compare-and-swap
// (UpdateIssueIfMatch/DeleteIssueIfMatch) was refused because the store is
// federating: a remote is configured, so `bd dolt pull` can 3-way-merge a foreign
// write, and until the revision-cell merge resolver lands that merge can slip past
// the guard — the load-bearing scope boundary of CAS (single linear timeline, not
// cross-replica consensus). It unwraps to storage.ErrConditionalWriteUnsupported,
// so callers detect it with errors.Is(err, storage.ErrConditionalWriteUnsupported)
// and may render it distinctly from a precondition (412) failure.
//
// It gates on federation only, NOT on the branch name or a mere extra local
// branch: a single writer on one working set is safe regardless of which branch
// it is named, and gating on branch presence would refuse CAS for the common
// case of a leftover or working branch (red-team amendment). An explicit
// cross-branch merge is covered by the revision-cell merge resolver, not this gate.
//
// Metadata-key CAS (B0) deliberately does NOT use this gate: it is session-local
// and its guard cell (the metadata JSON blob) conflicts whole-cell on a merge, so
// it stays sound across branches without a nonce.
//
// The gate is a coarse, best-effort, fail-closed guard evaluated on the write
// transaction's own connection just before the guarded write. It bounds the
// federation topology; it is NOT atomic against a remote configured concurrently
// after the check.
type ConditionalWriteGateError struct {
	Reason string
}

func (e *ConditionalWriteGateError) Error() string {
	return fmt.Sprintf("whole-row compare-and-swap unsupported here: %s (set %s=1 to override if you are the single linear writer)",
		e.Reason, AllowUnsafeCASEnv)
}

// Unwrap lets errors.Is(err, storage.ErrConditionalWriteUnsupported) match.
func (e *ConditionalWriteGateError) Unwrap() error { return storage.ErrConditionalWriteUnsupported }

// checkConditionalWriteGate returns a *ConditionalWriteGateError when whole-row
// CAS cannot be guaranteed for this store's topology, or nil to proceed. It runs
// on tx (the write transaction's own connection).
func (s *DoltStore) checkConditionalWriteGate(ctx context.Context, tx *sql.Tx) error {
	if allowUnsafeCAS() {
		return nil
	}

	// A configured remote means pulls can merge a foreign write past the guard.
	// dolt_remotes reads empty at cold start before CLI remotes sync from
	// .dolt/config (GH#2315), so fall back to the on-disk probe — the same
	// belt-and-suspenders the remote-migrate gate uses.
	hasRemote, err := doltRemoteConfigured(ctx, tx)
	if err != nil {
		return fmt.Errorf("conditional-write gate: read remotes: %w", err)
	}
	if !hasRemote {
		hasRemote = s.HasPersistedRemote()
	}
	if hasRemote {
		return &ConditionalWriteGateError{
			Reason: "a remote is configured; a pull can merge a foreign write past the revision guard",
		}
	}
	return nil
}

func allowUnsafeCAS() bool {
	v := os.Getenv(AllowUnsafeCASEnv)
	if v == "" {
		return false
	}
	allowed, err := strconv.ParseBool(v)
	return err == nil && allowed
}

// doltRemoteConfigured reports whether dolt_remotes has any row on this
// connection. A missing dolt_remotes table is treated as "no remote" so the gate
// can never wedge on a database without that system table.
func doltRemoteConfigured(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_remotes").Scan(&count); err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
