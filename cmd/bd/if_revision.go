package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
)

// ExitConditionalWriteUnsupported is the exit code returned when a --if-revision
// write is REFUSED because whole-row compare-and-swap cannot be guaranteed: the
// resolved store lacks the capability (e.g. the proxied server), or the enforcing
// store's degrade gate fired (a non-single-timeline Dolt topology). It is DISTINCT
// from ExitPreconditionFailed (9, a lost race a retry can win) and the generic
// error (1): a gate refusal NEVER falls back to an unconditional write, and a
// caller must not retry it as if it were a race. Verified free: 1 (generic),
// 2 (usage), 9 (precondition), 10-12 (init safety).
const ExitConditionalWriteUnsupported = 13

// ifRevisionFlag is the flag shared by the guarded mutation commands
// (update/close/assign/delete).
const ifRevisionFlag = "if-revision"

// registerIfRevisionFlag adds --if-revision to a guarded mutation command.
func registerIfRevisionFlag(cmd *cobra.Command) {
	cmd.Flags().Int64(ifRevisionFlag, 0,
		"only apply if the issue's current revision equals N (whole-row compare-and-swap; exits 9 on mismatch)")
}

// ifRevisionPrecondition returns the --if-revision value as a *int64 — the
// whole-row CAS token to guard the write on — or nil when the flag was not set.
// A flag value of 0 is honored (assert the row is unwritten/pristine): Changed,
// not the value, distinguishes "set to 0" from "unset".
func ifRevisionPrecondition(cmd *cobra.Command) *int64 {
	if !cmd.Flags().Changed(ifRevisionFlag) {
		return nil
	}
	v, _ := cmd.Flags().GetInt64(ifRevisionFlag)
	return &v
}

// asConditionalWriter returns the store as a storage.ConditionalWriter, or a gate
// refusal (ExitConditionalWriteUnsupported) when the store lacks the capability —
// never a silent fallback to an unconditional write.
func asConditionalWriter(s any) (storage.ConditionalWriter, error) {
	cw, ok := s.(storage.ConditionalWriter)
	if !ok {
		return nil, reportConditionalWriteUnsupported(fmt.Errorf(
			"%w: the resolved store does not support conditional writes (whole-row CAS requires a Dolt or embedded store)",
			storage.ErrConditionalWriteUnsupported))
	}
	return cw, nil
}

// mapConditionalWriteError renders a --if-revision write failure with the right
// exit code: a precondition mismatch → machine body + exit 9; a gate refusal
// (ErrConditionalWriteUnsupported) → machine body + exit 13; anything else →
// HandleErrorRespectJSON (generic). A nil err returns nil.
func mapConditionalWriteError(context string, err error) error {
	if err == nil {
		return nil
	}
	var pfe *storage.PreconditionFailedError
	if errors.As(err, &pfe) {
		return reportRevisionConflict(pfe)
	}
	if errors.Is(err, storage.ErrConditionalWriteUnsupported) {
		return reportConditionalWriteUnsupported(err)
	}
	return HandleErrorRespectJSON("%s: %v", context, err)
}

// reportRevisionConflict renders a whole-row revision mismatch: a machine-readable
// body (code = precondition_failed, expected/current revision) and exit code 9,
// symmetric with reportCasConflict for the metadata-key CAS.
func reportRevisionConflict(pfe *storage.PreconditionFailedError) error {
	human := fmt.Sprintf("precondition failed on %s: revision expected %d, current %d",
		pfe.ID, pfe.ExpectedRevision, pfe.CurrentRevision)
	if jsonOutput {
		_ = outputJSONRaw(revisionConflictJSON(pfe, human))
		return &exitError{Code: ExitPreconditionFailed}
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", human)
	return &exitError{Code: ExitPreconditionFailed}
}

func revisionConflictJSON(pfe *storage.PreconditionFailedError, human string) interface{} {
	inner := map[string]interface{}{
		"error":             human,
		"code":              "precondition_failed",
		"id":                pfe.ID,
		"expected_revision": pfe.ExpectedRevision,
		"current_revision":  pfe.CurrentRevision,
	}
	if jsonEnvelopeEnabled() {
		return map[string]interface{}{"schema_version": JSONSchemaVersion, "data": inner}
	}
	inner["schema_version"] = JSONSchemaVersion
	return inner
}

// reportConditionalWriteUnsupported renders a gate refusal with the distinct
// ExitConditionalWriteUnsupported code, so a caller never mistakes it for a
// retryable race or a silent unconditional write.
func reportConditionalWriteUnsupported(err error) error {
	human := err.Error()
	if jsonOutput {
		inner := map[string]interface{}{"error": human, "code": "conditional_write_unsupported"}
		var body interface{} = inner
		if jsonEnvelopeEnabled() {
			body = map[string]interface{}{"schema_version": JSONSchemaVersion, "data": inner}
		} else {
			inner["schema_version"] = JSONSchemaVersion
		}
		_ = outputJSONRaw(body)
		return &exitError{Code: ExitConditionalWriteUnsupported}
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", human)
	return &exitError{Code: ExitConditionalWriteUnsupported}
}
