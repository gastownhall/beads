package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
)

// ExitPreconditionFailed is the exit code returned when a `bd cas` conditional
// write is CHECKED and MISMATCHED (compare-and-swap failure). It is distinct
// from the generic error code (1) so scripts and agents can reliably detect a
// lost race and re-read+retry. Verified free: 1 (generic), 2 (usage), 10-12
// (init safety).
const ExitPreconditionFailed = 9

var casCmd = &cobra.Command{
	Use:     "cas",
	GroupID: "issues",
	Short:   "Conditional (compare-and-swap) metadata writes",
	Long: `Atomic compare-and-swap on a single metadata key.

Use 'cas set' to set a key only if its current value matches an expectation —
the building block for lock-free fences, epoch counters, and claim-once
reservations across concurrent writers.

On a precondition mismatch the command exits ` + fmt.Sprint(ExitPreconditionFailed) + ` (distinct from the generic
error code 1) with the current value, so a caller can re-read and retry.`,
}

var casSetCmd = &cobra.Command{
	Use:   "set <id> <key> <value>",
	Short: "Set a metadata key only if its current value matches",
	Long: `Atomically set a metadata key iff a precondition holds.

Exactly one precondition is required:
  --if <value>   set only if the key currently equals <value>
  --if-absent    set only if the key is currently absent (claim-once)

Examples:
  bd cas set bd-1 gc.control_epoch 5 --if 4     # advance epoch 4 -> 5
  bd cas set bd-1 gc.drain.reserved_by me --if-absent   # claim once

Exit codes: 0 success, ` + fmt.Sprint(ExitPreconditionFailed) + ` precondition mismatch, 1 other error.`,
	Args:          cobra.ExactArgs(3),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runCasSet,
}

func init() {
	casSetCmd.Flags().String("if", "", "set only if the key currently equals this value")
	casSetCmd.Flags().Bool("if-absent", false, "set only if the key is currently absent (claim-once)")
	casSetCmd.MarkFlagsMutuallyExclusive("if", "if-absent")
	casSetCmd.MarkFlagsOneRequired("if", "if-absent")

	casUnsetCmd.Flags().String("if", "", "remove only if the key currently equals this value")
	_ = casUnsetCmd.MarkFlagRequired("if")

	casCmd.AddCommand(casSetCmd)
	casCmd.AddCommand(casUnsetCmd)
	rootCmd.AddCommand(casCmd)
}

var casUnsetCmd = &cobra.Command{
	Use:   "unset <id> <key>",
	Short: "Remove a metadata key only if its current value matches (guarded release)",
	Long: `Atomically remove a metadata key iff it currently equals --if <value>.

The symmetric release for 'cas set --if-absent': release a reservation or lease
you hold. Idempotent — removing an already-absent key succeeds.

Example:
  bd cas unset bd-1 gc.drain.reserved_by --if me   # release my reservation

Exit codes: 0 success, ` + fmt.Sprint(ExitPreconditionFailed) + ` held by a different value, 1 other error.`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runCasUnset,
}

func runCasUnset(cmd *cobra.Command, args []string) error {
	CheckReadonly("cas unset")
	id, key := args[0], args[1]
	expected, _ := cmd.Flags().GetString("if")

	ctx := rootCtx
	result, err := resolveAndGetIssueForMutation(ctx, store, id)
	if err != nil {
		if result != nil {
			result.Close()
		}
		return HandleErrorRespectJSON("resolving %s: %v", id, err)
	}
	if result == nil || result.Issue == nil {
		if result != nil {
			result.Close()
		}
		return HandleErrorRespectJSON("issue %s not found", id)
	}
	defer result.Close()

	cw, ok := result.Store.(storage.ConditionalWriter)
	if !ok {
		return HandleErrorWithHintRespectJSON(
			"this store does not support conditional writes",
			"conditional writes require a Dolt or embedded store; the resolved store lacks the capability")
	}

	if _, err := cw.CompareAndClearMetadataKey(ctx, result.ResolvedID, key, expected, actor); err != nil {
		var pfe *storage.PreconditionFailedError
		if errors.As(err, &pfe) {
			return reportCasConflict(pfe)
		}
		return HandleErrorRespectJSON("cas unset %s %s: %v", id, key, err)
	}

	if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
		Command:  "cas",
		IssueIDs: []string{result.ResolvedID},
	}); err != nil {
		return HandleErrorRespectJSON("committing cas %s: %v", id, err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{"id": result.ResolvedID, "key": key, "cleared": true})
	}
	fmt.Printf("Cleared %s.%s\n", result.ResolvedID, key)
	return nil
}

func runCasSet(cmd *cobra.Command, args []string) error {
	CheckReadonly("cas set")
	id, key, value := args[0], args[1], args[2]

	ifAbsent, _ := cmd.Flags().GetBool("if-absent")
	var expected *string
	if !ifAbsent {
		v, _ := cmd.Flags().GetString("if")
		expected = &v
	}

	ctx := rootCtx
	result, err := resolveAndGetIssueForMutation(ctx, store, id)
	if err != nil {
		if result != nil {
			result.Close()
		}
		return HandleErrorRespectJSON("resolving %s: %v", id, err)
	}
	if result == nil || result.Issue == nil {
		if result != nil {
			result.Close()
		}
		return HandleErrorRespectJSON("issue %s not found", id)
	}
	defer result.Close()

	cw, ok := result.Store.(storage.ConditionalWriter)
	if !ok {
		return HandleErrorWithHintRespectJSON(
			"this store does not support conditional writes",
			"conditional writes require a Dolt or embedded store; the resolved store lacks the capability")
	}

	issue, err := cw.CompareAndSetMetadataKey(ctx, result.ResolvedID, key, expected, value, actor)
	if err != nil {
		var pfe *storage.PreconditionFailedError
		if errors.As(err, &pfe) {
			return reportCasConflict(pfe)
		}
		return HandleErrorRespectJSON("cas set %s %s: %v", id, key, err)
	}

	if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
		Command:  "cas",
		IssueIDs: []string{result.ResolvedID},
	}); err != nil {
		return HandleErrorRespectJSON("committing cas %s: %v", id, err)
	}

	_ = issue // post-write issue (Revision surfaces here once the B1 column lands)
	if jsonOutput {
		body := map[string]interface{}{
			"id":    result.ResolvedID,
			"key":   key,
			"value": value,
		}
		return outputJSON(body)
	}
	fmt.Printf("Set %s.%s = %s\n", result.ResolvedID, key, value)
	return nil
}

// reportCasConflict renders a precondition mismatch: a machine-readable body
// (error = human message, code = stable slug) and exit code 9.
func reportCasConflict(pfe *storage.PreconditionFailedError) error {
	human := casConflictMessage(pfe)
	if jsonOutput {
		body := casConflictJSON(pfe, human)
		_ = outputJSONRaw(body)
		return &exitError{Code: ExitPreconditionFailed}
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", human)
	return &exitError{Code: ExitPreconditionFailed}
}

func casConflictMessage(pfe *storage.PreconditionFailedError) string {
	exp := "<absent>"
	if pfe.ExpectedValue != nil {
		exp = fmt.Sprintf("%q", *pfe.ExpectedValue)
	}
	cur := "<absent>"
	if pfe.CurrentValue != nil {
		cur = fmt.Sprintf("%q", *pfe.CurrentValue)
	}
	return fmt.Sprintf("precondition failed on %s: metadata key %q expected %s, current %s",
		pfe.ID, pfe.Key, exp, cur)
}

func casConflictJSON(pfe *storage.PreconditionFailedError, human string) interface{} {
	inner := map[string]interface{}{
		"error": human,
		"code":  "precondition_failed",
		"id":    pfe.ID,
		"key":   pfe.Key,
	}
	if pfe.ExpectedValue != nil {
		inner["expected_value"] = *pfe.ExpectedValue
	}
	if pfe.CurrentValue != nil {
		inner["current_value"] = *pfe.CurrentValue
	}
	if jsonEnvelopeEnabled() {
		return map[string]interface{}{
			"schema_version": JSONSchemaVersion,
			"data":           inner,
		}
	}
	inner["schema_version"] = JSONSchemaVersion
	return inner
}
