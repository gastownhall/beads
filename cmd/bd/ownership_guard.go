package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Exit codes for the guarded-ownership contract. The unmerged CAS branch
// (cas/optimistic-concurrency) declares the same names/values in cmd/bd; it
// rebases onto this branch, drops its duplicate declarations, and extends
// storage.PreconditionFailedError with its revision fields — a deliberate,
// trivially-resolved conflict, coordinated in gascity epic ga-furrj5.
const (
	// ExitPreconditionFailed: a guarded mutation's --if-assignee/--if-fence
	// precondition no longer matched the row at write time.
	ExitPreconditionFailed = 9
	// ExitConditionalWriteUnsupported: the caller supplied a guard that this
	// path cannot enforce (proxied-server mode until guards are threaded
	// through the UOW protocol, or a sub-operation with no guarded form).
	// Refusing loudly is the contract — silently dropping a guard is the one
	// forbidden outcome.
	ExitConditionalWriteUnsupported = 13
)

// registerOwnershipGuardFlags adds the guarded-mutation flags to a verb.
// Semantics live in issueops.Guard: --if-assignee compares
// blank-insensitively (” and NULL are both unassigned), --if-fence compares
// the monotonic claim_fence snapshot. On an ownership-transition verb
// (unclaim) a satisfied guard authorizes a cross-actor release; --force never
// skips a supplied guard. Guards scope to exactly ONE issue: the command's
// primary target.
func registerOwnershipGuardFlags(cmd *cobra.Command) {
	cmd.Flags().String("if-assignee", "", "Only mutate while the issue is still assigned to this actor; empty asserts unassigned (mismatch: exit 9, unsupported path: exit 13)")
	cmd.Flags().Int64("if-fence", 0, "Only mutate while claim_fence still equals this snapshot value (mismatch: exit 9, unsupported path: exit 13)")
}

// ownershipGuardFromFlags builds the guard from explicitly-set flags.
// Changed() gates each axis so 0 and "" are valid assertion values.
func ownershipGuardFromFlags(cmd *cobra.Command) (issueops.Guard, bool) {
	var g issueops.Guard
	if cmd.Flags().Changed("if-assignee") {
		v, _ := cmd.Flags().GetString("if-assignee")
		g.Assignee = &v
	}
	if cmd.Flags().Changed("if-fence") {
		v, _ := cmd.Flags().GetInt64("if-fence")
		g.Fence = &v
	}
	return g, !g.IsZero()
}

// validateGuardInvocation enforces the guard's scope rules at the CLI edge:
// one target issue (a guard is a snapshot of one row's ownership), and a
// store path that can enforce it. Returns nil when no guard is supplied.
//
// IMPORTANT for callers: the guard must be attached (issueops.WithGuard)
// ONLY around the single mutation of the target issue — never on the
// command-wide context. Cascading writes to other issues (molecule
// auto-close, auto-advance claims, comments) must run unguarded, or the
// target's guard would be wrongly evaluated against unrelated rows.
func validateGuardInvocation(cmd *cobra.Command, targetCount int) error {
	_, hasGuard := ownershipGuardFromFlags(cmd)
	if !hasGuard {
		return nil
	}
	if usesProxiedServer() {
		return reportGuardUnsupported(cmd, "proxied-server mode does not enforce ownership guards yet")
	}
	if targetCount > 1 {
		return HandleErrorRespectJSON("--if-assignee/--if-fence apply to a single issue (a guard is one row's ownership snapshot); got %d targets", targetCount)
	}
	return nil
}

// emitTypedStderr writes a typed JSON body to stderr, honoring the envelope
// convention: {schema_version, data:{...}} under BD_JSON_ENVELOPE, flat with
// an embedded schema_version otherwise (matching buildJSONError).
func emitTypedStderr(inner map[string]interface{}) {
	var body interface{}
	if jsonEnvelopeEnabled() {
		body = map[string]interface{}{
			"schema_version": JSONSchemaVersion,
			"data":           inner,
		}
	} else {
		inner["schema_version"] = JSONSchemaVersion
		body = inner
	}
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

// reportOwnershipConflict emits the typed conflict — JSON
// {code:"ownership_conflict", id, expected_*, current_*} in --json mode,
// human text otherwise — and returns the exit-9 error.
func reportOwnershipConflict(cmd *cobra.Command, pf *storage.PreconditionFailedError) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		inner := map[string]interface{}{
			"code":             "ownership_conflict",
			"id":               pf.ID,
			"current_assignee": pf.CurrentAssignee,
			"current_fence":    pf.CurrentFence,
		}
		if pf.ExpectedAssignee != nil {
			inner["expected_assignee"] = *pf.ExpectedAssignee
		}
		if pf.ExpectedFence != nil {
			inner["expected_fence"] = *pf.ExpectedFence
		}
		emitTypedStderr(inner)
	} else {
		fmt.Fprintf(os.Stderr, "Error: %v\n", pf)
	}
	return &exitError{Code: ExitPreconditionFailed}
}

// reportGuardUnsupported emits the typed refusal
// {code:"conditional_write_unsupported"} and returns the exit-13 error.
func reportGuardUnsupported(cmd *cobra.Command, reason string) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		emitTypedStderr(map[string]interface{}{
			"code":  "conditional_write_unsupported",
			"error": reason,
		})
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", reason)
	}
	return &exitError{Code: ExitConditionalWriteUnsupported}
}

// maybeReportOwnershipConflict maps a guarded-mutation error to the typed
// exit-9 contract; returns (handled, err) so callers fall through to their
// existing error handling for everything else.
func maybeReportOwnershipConflict(cmd *cobra.Command, err error) (bool, error) {
	var pf *storage.PreconditionFailedError
	if errors.As(err, &pf) {
		return true, reportOwnershipConflict(cmd, pf)
	}
	return false, nil
}

// emitAlreadyClaimedJSON writes the typed claim-conflict body
// {code:"already_claimed", id, holder} to stderr in --json mode, ALONGSIDE
// the frozen human-readable message — existing substring-matching consumers
// keep working; new consumers parse this line. Callers gate on
// storage.ErrAlreadyClaimed so transient/not-claimable failures are never
// mislabeled. holder may be empty when the current holder could not be
// re-read.
func emitAlreadyClaimedJSON(cmd *cobra.Command, id, holder string) {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if !jsonOut {
		return
	}
	inner := map[string]interface{}{
		"code": "already_claimed",
		"id":   id,
	}
	if holder != "" {
		inner["holder"] = holder
	}
	emitTypedStderr(inner)
}
