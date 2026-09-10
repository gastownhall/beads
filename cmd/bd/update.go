package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/issueops"
)

// commandIssueUpdater is the lifecycle capability the update command needs.
// issueops.Lifecycle satisfies it without an adapter.
type commandIssueUpdater interface {
	Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error)
}

// commandUpdateMutation contains the command-derived values for one lifecycle
// update. Both routes of `bd update` fill it in: the direct one below, and the
// proxied-server claim path in update_proxied_server.go.
type commandUpdateMutation struct {
	actor            string
	issueID          string
	patch            issueops.IssuePatch
	claim            bool
	force            bool
	expectedAssignee *string
	expectedStatus   *issueops.Status
	// provenance names the history entry the write records. Empty takes the
	// backend's default, which is what the direct route wants; the proxied
	// route spells the message it has always written.
	provenance string
}

// runCommandUpdateMutation maps a command update into its lifecycle request and
// returns the lifecycle result unchanged. It is the ONE place the command's
// flag semantics become a request — in particular the one --force that means
// two overrides, whose assignee half only applies to an assignee edit.
func runCommandUpdateMutation(ctx context.Context, updater commandIssueUpdater, mutation commandUpdateMutation) (issueops.UpdateResult, error) {
	return updater.Update(ctx, issueops.UpdateRequest{
		Actor:                 mutation.actor,
		IssueID:               mutation.issueID,
		Patch:                 mutation.patch,
		Claim:                 mutation.claim,
		ForceAssigneeTransfer: mutation.force && mutation.patch.Assignee.Set,
		ForceClosePolicy:      mutation.force,
		ExpectedAssignee:      mutation.expectedAssignee,
		ExpectedStatus:        mutation.expectedStatus,
		Provenance:            mutation.provenance,
	})
}

var updateCmd = &cobra.Command{
	Use:     "update [id...]",
	GroupID: "issues",
	Short:   "Update one or more issues",
	Long: `Update one or more issues.

If no issue ID is provided, updates the last touched issue (from most recent
create, update, show, or close operation). This fallback only applies in
interactive sessions (stdin is a terminal); in scripts and agent sessions a
missing ID is an error, so a command built from an empty variable cannot
silently mutate an unrelated issue. Set BD_LAST_TOUCHED_FALLBACK=1 to allow
the fallback anywhere, or =0 to disable it entirely.

Updates are applied per issue ID, not atomically across IDs: when some IDs
fail, the remaining issues are still updated, every failed ID is reported on
stderr, and the command exits nonzero.

Exit codes: 1 for general failures; 13 when every failure is a stale
--if-assignee/--if-status guard (the precondition no longer held, nothing was
written — another actor won the race, so retrying the same guard is
pointless).`,
	// The non-interactive no-ID refusal lives in argument validation, which
	// cobra runs before root's PersistentPreRunE — so a scripted `bd update
	// $ID ...` with an empty $ID fails fast, before the pre-run hooks can
	// open the store, run a version-bump migration, or auto-import JSONL
	// (bd-m00pb). The interactive fallback itself is resolved in RunE.
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && !AllowLastTouchedFallback() {
			return HandleErrorRespectJSON("no issue ID provided (the last-touched fallback only applies in interactive sessions; pass an explicit issue ID or set BD_LAST_TOUCHED_FALLBACK=1)")
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("update") // also covers CheckMigrationFreeze (dc-6jaq)

		evt := metrics.NewCommandEvent("update")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		// Refuse a mis-typed flag value that cobra parsed as a positional issue
		// id, before any write path (direct or proxied) can apply a partial
		// update. See bd-5247.
		if err := errStrayFlagValuePositional(args); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		if usesProxiedServer() {
			return runUpdateProxiedServer(cmd, rootCtx, args)
		}

		// If no IDs provided, use last touched issue (interactive only;
		// the non-interactive case was already refused in Args validation)
		if len(args) == 0 {
			lastTouched := GetLastTouchedID()
			if lastTouched == "" {
				return HandleErrorRespectJSON("no issue ID provided and no last touched issue")
			}
			args = []string{lastTouched}
		}

		updates := make(map[string]interface{})
		// clearDeferStatus: set per-issue in the update loop when --defer=""
		// was given without an explicit --status, to flip status=deferred back
		// to open (matches the help text's "show in bd ready immediately").
		var clearDeferStatus bool

		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetString("status")
			var customStatuses []string
			if store != nil {
				cs, err := store.GetCustomStatuses(rootCtx)
				if err != nil {
					if !jsonOutput {
						fmt.Fprintf(os.Stderr, "%s Failed to get custom statuses: %v\n", ui.RenderWarn("!"), err)
					}
				} else {
					customStatuses = cs
				}
			}
			if !types.Status(status).IsValidWithCustom(customStatuses) {
				return HandleErrorRespectJSON("invalid status %q (built-in: open, in_progress, blocked, deferred, closed, pinned, hooked; or configure custom statuses via 'bd config set status.custom')", status)
			}
			updates["status"] = status

			// If status is being set to closed, include session if provided
			if status == "closed" {
				session, _ := cmd.Flags().GetString("session")
				if session == "" {
					session = os.Getenv("CLAUDE_SESSION_ID")
				}
				if session != "" {
					updates["closed_by_session"] = session
				}
			}
		}
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			updates["priority"] = priority
		}
		if cmd.Flags().Changed("title") {
			title, _ := cmd.Flags().GetString("title")
			title = strings.TrimSpace(title)
			if title == "" {
				return HandleErrorRespectJSON("title cannot be empty")
			}
			updates["title"] = title
		}
		if cmd.Flags().Changed("assignee") {
			assignee, _ := cmd.Flags().GetString("assignee")
			updates["assignee"] = assignee
		}
		description, descChanged, err := getDescriptionFlag(cmd)
		if err != nil {
			return err
		}
		if descChanged {
			if err := validateDescriptionUpdate(cmd, description, descChanged); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			updates["description"] = description
		}
		design, designChanged, err := getDesignFlag(cmd)
		if err != nil {
			return err
		}
		if designChanged {
			updates["design"] = design
		}
		if cmd.Flags().Changed("notes") && cmd.Flags().Changed("append-notes") {
			return HandleErrorRespectJSON("cannot specify both --notes and --append-notes")
		}
		if cmd.Flags().Changed("notes") {
			notes, _ := cmd.Flags().GetString("notes")
			updates["notes"] = notes
		}
		if cmd.Flags().Changed("append-notes") {
			appendNotes, _ := cmd.Flags().GetString("append-notes")
			updates[storageissueops.OpAppendNotes] = appendNotes
		}
		if cmd.Flags().Changed("acceptance") || cmd.Flags().Changed("acceptance-criteria") {
			var acceptanceCriteria string
			if cmd.Flags().Changed("acceptance") {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance")
			} else {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance-criteria")
			}
			updates["acceptance_criteria"] = acceptanceCriteria
		}
		if cmd.Flags().Changed("external-ref") {
			externalRef, _ := cmd.Flags().GetString("external-ref")
			// Empty string clears the ref to SQL NULL, mirroring buildCreateIssue's
			// nil-when-empty pointer semantics so cleared refs round-trip as a
			// missing field (omitempty) instead of an empty string. GH#3902.
			if externalRef == "" {
				updates["external_ref"] = nil
			} else {
				updates["external_ref"] = externalRef
			}
		}
		if cmd.Flags().Changed("spec-id") {
			specID, _ := cmd.Flags().GetString("spec-id")
			updates["spec_id"] = specID
		}
		if cmd.Flags().Changed("estimate") {
			estimate, _ := cmd.Flags().GetInt("estimate")
			if estimate < 0 {
				return HandleErrorRespectJSON("estimate must be a non-negative number of minutes")
			}
			updates["estimated_minutes"] = estimate
		}
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Normalize aliases (e.g., "enhancement" -> "feature") before validating.
			// Type validation (including custom types) is handled by the storage
			// layer inside the transaction, matching the create path. (GH#3030)
			issueType = utils.NormalizeIssueType(issueType)
			updates["issue_type"] = issueType
		}
		if cmd.Flags().Changed("add-label") {
			addLabels, _ := cmd.Flags().GetStringSlice("add-label")
			added := utils.NormalizeLabels(addLabels)
			warnLabelsContainingWhitespace(added)
			updates["add_labels"] = added
		}
		if cmd.Flags().Changed("remove-label") {
			removeLabels, _ := cmd.Flags().GetStringSlice("remove-label")
			updates["remove_labels"] = utils.NormalizeLabels(removeLabels)
		}
		if cmd.Flags().Changed("set-labels") {
			setLabels, _ := cmd.Flags().GetStringSlice("set-labels")
			set := utils.NormalizeLabels(setLabels)
			warnLabelsContainingWhitespace(set)
			updates["set_labels"] = set
		}
		if cmd.Flags().Changed("parent") {
			parent, _ := cmd.Flags().GetString("parent")
			updates["parent"] = parent
		}
		// Gate fields (bd-z6kw)
		if cmd.Flags().Changed("await-id") {
			awaitID, _ := cmd.Flags().GetString("await-id")
			updates["await_id"] = awaitID
		}
		// Time-based scheduling flags (GH#820)
		if cmd.Flags().Changed("due") {
			dueStr, _ := cmd.Flags().GetString("due")
			if dueStr == "" {
				// Empty string clears the due date
				updates["due_at"] = nil
			} else {
				t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
				if err != nil {
					return HandleErrorRespectJSON("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
				}
				updates["due_at"] = t
			}
		}
		if cmd.Flags().Changed("defer") {
			deferStr, _ := cmd.Flags().GetString("defer")
			if deferStr == "" {
				// Empty string clears the defer_until and restores ready-work
				// visibility (GH#3233). Explicit --status still wins.
				updates["defer_until"] = nil
				if _, ok := updates["status"]; !ok {
					clearDeferStatus = true
				}
			} else {
				t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
				if err != nil {
					return HandleErrorRespectJSON("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
				}
				// Warn if defer date is in the past (user probably meant future)
				inPast := t.Before(time.Now())
				if inPast && !jsonOutput {
					fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
						ui.RenderWarn("!"), t.Local().Format("2006-01-02 15:04"))
					fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
				}
				updates["defer_until"] = t
				// Align with `bd defer`: set status=deferred so the ❄ icon
				// shows and the issue leaves the ready queue (GH#3233).
				// Skip for past dates so the "appears in bd ready immediately"
				// warning stays truthful, and skip if --status was set explicitly.
				if _, ok := updates["status"]; !ok && !inPast {
					updates["status"] = string(types.StatusDeferred)
				}
			}
		}
		// Ephemeral/persistent flags
		// Note: storage layer uses "wisp" field name, maps to "ephemeral" column
		ephemeralChanged := cmd.Flags().Changed("ephemeral")
		persistentChanged := cmd.Flags().Changed("persistent")
		noHistoryChanged := cmd.Flags().Changed("no-history")
		historyChanged := cmd.Flags().Changed("history")
		if ephemeralChanged && persistentChanged {
			return HandleErrorRespectJSON("cannot specify both --ephemeral and --persistent flags")
		}
		if noHistoryChanged && ephemeralChanged {
			return HandleErrorRespectJSON("cannot specify both --no-history and --ephemeral flags")
		}
		if noHistoryChanged && historyChanged {
			return HandleErrorRespectJSON("cannot specify both --no-history and --history flags")
		}
		if ephemeralChanged {
			updates["wisp"] = true
		}
		if persistentChanged {
			updates["wisp"] = false
		}
		if noHistoryChanged {
			updates["no_history"] = true
		}
		if historyChanged {
			updates["no_history"] = false
		}
		// Metadata flag (GH#1413)
		if cmd.Flags().Changed("metadata") {
			metadataValue, _ := cmd.Flags().GetString("metadata")
			var metadataJSON string
			if strings.HasPrefix(metadataValue, "@") {
				// Read JSON from file
				filePath := metadataValue[1:]
				// #nosec G304 -- user explicitly provides file path via @file.json syntax
				data, err := os.ReadFile(filePath)
				if err != nil {
					return HandleErrorRespectJSON("failed to read metadata file %s: %v", filePath, err)
				}
				metadataJSON = string(data)
			} else {
				metadataJSON = metadataValue
			}
			// Validate JSON
			if !json.Valid([]byte(metadataJSON)) {
				return HandleErrorRespectJSON("invalid JSON in --metadata: must be valid JSON")
			}
			// Passed as a merge OPERATION, not a pre-merged value: the storage
			// layer re-reads and merges inside the mutation transaction so a
			// concurrent writer's keys survive (lost-update fix).
			updates[storageissueops.OpMergeMetadata] = json.RawMessage(metadataJSON)
		}

		// Incremental metadata edits (GH#1406)
		setMetadataFlags, _ := cmd.Flags().GetStringArray("set-metadata")
		unsetMetadataFlags, _ := cmd.Flags().GetStringArray("unset-metadata")
		if (len(setMetadataFlags) > 0 || len(unsetMetadataFlags) > 0) && cmd.Flags().Changed("metadata") {
			return HandleErrorRespectJSON("cannot combine --metadata with --set-metadata or --unset-metadata")
		}
		if len(setMetadataFlags) > 0 {
			updates[storageissueops.OpSetMetadata] = setMetadataFlags
		}
		if len(unsetMetadataFlags) > 0 {
			updates[storageissueops.OpUnsetMetadata] = unsetMetadataFlags
		}

		// Get claim flag
		claimFlag, _ := cmd.Flags().GetBool("claim")
		// --force bypasses the live-claim reassign fence (bd-98s5c); mutually
		// exclusive with --if-assignee at the flag-group level.
		forceFlag, _ := cmd.Flags().GetBool("force")

		if len(updates) == 0 && !claimFlag {
			fmt.Println("No updates specified")
			return nil
		}

		// Conditional-update guards (bd-wsqvw): validated against the same
		// status set as --status, mutually exclusive with --claim (which is
		// its own compare-and-set), and only meaningful with a field update
		// to ride on.
		ifAssignee, ifStatus, err := updateGuardsFromFlags(cmd, claimFlag, updates)
		if err != nil {
			return err
		}
		var expectedStatus *issueops.Status
		if ifStatus != nil {
			expected := issueops.Status(*ifStatus)
			expectedStatus = &expected
		}

		// One typed patch for the whole batch. The flag-derived map is still
		// the source (the guard rules and the notes-overwrite warning read it);
		// this reshapes it once instead of once per issue.
		basePatch, err := buildUpdatePatch(updates)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		ctx := rootCtx
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		updatedIssues := []*types.Issue{}
		var firstUpdatedID string // Track first successful update for last-touched
		var failures []updateIDFailure
		recordFailure := func(id, reason string) {
			failures = append(failures, updateIDFailure{ID: id, Error: reason})
		}
		mutatedStores := map[storage.DoltStorage][]string{}
		notesOverwriteWarnings := map[storage.DoltStorage][]string{}
		mutatedResults := map[*RoutedResult]bool{}
		pendingCloseResults := []*RoutedResult{}
		trackMutation := func(result *RoutedResult) {
			if result == nil || result.Store == nil {
				return
			}
			if !mutatedResults[result] {
				pendingCloseResults = append(pendingCloseResults, result)
				mutatedResults[result] = true
			}
			mutatedStores[result.Store] = append(mutatedStores[result.Store], result.ResolvedID)
		}
		closeIfUnmutated := func(result *RoutedResult) {
			if result == nil {
				return
			}
			if mutatedResults[result] {
				return
			}
			result.Close()
		}
		closePendingResults := func() {
			for _, result := range pendingCloseResults {
				result.Close()
			}
			pendingCloseResults = nil
		}
		for _, id := range args {
			// Resolve and get issue with routing (e.g., gt-xyz routes to another rig)
			result, err := resolveAndGetIssueForMutation(ctx, store, id)
			if err != nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				recordFailure(id, fmt.Sprintf("resolving issue: %v", err))
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				recordFailure(id, "issue not found")
				continue
			}
			issue := result.Issue
			issueStore := result.Store

			if err := validateIssueUpdatable(id, issue); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				recordFailure(id, err.Error())
				closeIfUnmutated(result)
				continue
			}

			// bd-98s5c: an unguarded assignee update must not silently
			// overwrite another actor's live claim. Skipped under
			// --if-assignee: that CAS names the holder explicitly, which is
			// how sanctioned X→Y transfers (park) stay possible without
			// --force. Also skipped under --claim: the claim CAS is itself
			// the anti-steal gate (a foreign live claim fails it with the
			// canonical "already claimed" copy before any field update runs),
			// and an assignee edit that rides a WON claim only ever touches
			// the actor's own fresh claim. A policy refusal, so it exits 1,
			// not 13.
			if newAssignee, ok := updates["assignee"].(string); ok && ifAssignee == nil && !claimFlag {
				if err := validateIssueReassignable(id, issue, actor, newAssignee,
					storeClaimPoolAliases(ctx, issueStore), forceFlag); err != nil {
					fmt.Fprintf(os.Stderr, "%s\n", err)
					recordFailure(id, err.Error())
					closeIfUnmutated(result)
					continue
				}
			}

			// One atomic operation carries the claim, every field edit, the
			// label edits, the metadata edits and the reparent. Metadata edits
			// (--metadata, --set-metadata, --unset-metadata) and --append-notes
			// still resolve against the row re-read inside the mutation
			// transaction: merging against the `issue` snapshot (read in an
			// earlier transaction) silently erased concurrent writers' keys —
			// both processes exited 0, one process's committed write vanished.
			patch := basePatch
			// GH#3233: --defer="" restores ready visibility only if the issue
			// was actually deferred. Other statuses (blocked, in_progress, …)
			// shouldn't be clobbered just because defer_until was stale.
			if clearDeferStatus && issue.Status == types.StatusDeferred {
				patch.Status = issueops.Field[issueops.Status]{Set: true, Value: types.StatusOpen}
			}
			notesOverwritten := replacesExistingNotes(issue.Notes, updates)

			ops, err := writeOps(issueStore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
				recordFailure(id, fmt.Sprintf("updating issue: %v", err))
				closeIfUnmutated(result)
				continue
			}
			// Guards ride the operation itself: a stale assignee/status refuses
			// atomically with a typed mismatch error and MUST surface as a
			// non-zero exit — never collapse it to success (finding #10).
			// One --force, two overrides. The assignee half only applies to an
			// assignee edit — asserting it without one is an invalid request,
			// which is why it is conditioned here rather than passed straight
			// through: `--force -s closed` is now a legitimate way to ask for
			// the close-policy half alone.
			updateResult, updateErr := runCommandUpdateMutation(opsCtx, ops, commandUpdateMutation{
				actor:            actor,
				issueID:          result.ResolvedID,
				patch:            patch,
				claim:            claimFlag,
				force:            forceFlag,
				expectedAssignee: ifAssignee,
				expectedStatus:   expectedStatus,
			})
			if updateErr != nil {
				fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, updateErr)
				failures = append(failures, updateIDFailure{
					ID:            id,
					Error:         fmt.Sprintf("updating issue: %v", updateErr),
					GuardMismatch: isGuardMismatch(updateErr),
				})
				closeIfUnmutated(result)
				continue
			}
			updatedIssue := updateResult.Issue
			trackMutation(result)
			if notesOverwritten {
				notesOverwriteWarnings[issueStore] = append(notesOverwriteWarnings[issueStore], id)
			}
			// Audit log key field changes (survives Dolt GC flatten)
			if patch.Status.Set {
				audit.LogFieldChange(result.ResolvedID, "status", string(issue.Status), string(patch.Status.Value), actor, "")
			}
			if patch.Assignee.Set {
				audit.LogFieldChange(result.ResolvedID, "assignee", issue.Assignee, patch.Assignee.Value, actor, "")
			}
			if patch.Priority.Set {
				audit.LogFieldChange(result.ResolvedID, "priority", fmt.Sprintf("%d", issue.Priority), fmt.Sprintf("%d", patch.Priority.Value), actor, "")
			}

			// The operation's own post-state snapshot replaces the re-read.
			// Dependency records are dropped from it because `bd update` has
			// never printed them: GetIssue did not hydrate them either.
			if updatedIssue != nil {
				updatedIssue.Dependencies = nil
			}
			updateTitle := ""
			if updatedIssue != nil {
				updateTitle = updatedIssue.Title
			}

			if jsonOutput {
				if updatedIssue != nil {
					updatedIssues = append(updatedIssues, updatedIssue)
				}
			} else {
				debug.PrintNormal("%s Updated issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, updateTitle))
			}

			// Track first successful update for last-touched
			if firstUpdatedID == "" {
				firstUpdatedID = result.ResolvedID
			}
			closeIfUnmutated(result)
		}

		if len(mutatedStores) > 0 {
			for s, ids := range mutatedStores {
				if s == nil {
					continue
				}
				if err := commitPendingIfEmbedded(ctx, s, actor, doltAutoCommitParams{
					Command:  "update",
					IssueIDs: ids,
				}); err != nil {
					closePendingResults()
					return HandleErrorRespectJSON("failed to commit: %v", err)
				}
				for _, id := range notesOverwriteWarnings[s] {
					warnNotesReplacement(id)
				}
			}
		}
		closePendingResults()

		// Set last touched after all updates complete
		if firstUpdatedID != "" {
			SetLastTouchedID(firstUpdatedID)
		}

		if jsonOutput && len(updatedIssues) > 0 {
			if jerr := outputJSON(updatedIssues); jerr != nil {
				return jerr
			}
		}

		// Updates are per-ID, not atomic across IDs: successful updates above
		// stay applied (and committed), but any per-ID failure must surface as
		// a nonzero exit so callers can detect a partial batch (GH audit:
		// multi-ID update used to exit 0 after mid-batch failures).
		if len(failures) > 0 {
			return reportUpdateFailures(failures, len(args))
		}
		return nil
	},
}

// setField marks a patch field as supplied, including when the value is a zero
// value the caller meant to write.
func setField[T any](value T) issueops.Field[T] {
	return issueops.Field[T]{Set: true, Value: value}
}

// buildUpdatePatch reshapes the flag-derived update map into the facade's typed
// patch. Every key it handles is a key the flag parsing above wrote, so an
// unrecognized one is a bug in this file rather than user input.
func buildUpdatePatch(updates map[string]interface{}) (issueops.IssuePatch, error) {
	var patch issueops.IssuePatch
	var wisp, noHistory *bool
	for key, value := range updates {
		var ok bool
		switch key {
		case "title":
			patch.Title, ok = stringField(value)
		case "description":
			patch.Description, ok = stringField(value)
		case "design":
			patch.Design, ok = stringField(value)
		case "acceptance_criteria":
			patch.AcceptanceCriteria, ok = stringField(value)
		case "notes":
			patch.Notes, ok = stringField(value)
		case storageissueops.OpAppendNotes:
			patch.AppendNotes, ok = stringField(value)
		case "spec_id":
			patch.SpecID, ok = stringField(value)
		case "await_id":
			patch.AwaitID, ok = stringField(value)
		case "closed_by_session":
			patch.ClosedBySession, ok = stringField(value)
		case "assignee":
			patch.Assignee, ok = stringField(value)
		case "parent":
			patch.ParentID, ok = stringField(value)
		case "status":
			var status issueops.Field[string]
			if status, ok = stringField(value); ok {
				patch.Status = setField(issueops.Status(status.Value))
			}
		case "issue_type":
			var issueType issueops.Field[string]
			if issueType, ok = stringField(value); ok {
				patch.IssueType = setField(issueops.IssueType(issueType.Value))
			}
		case "priority":
			var priority int
			if priority, ok = value.(int); ok {
				patch.Priority = setField(priority)
			}
		case "estimated_minutes":
			var estimate int
			if estimate, ok = value.(int); ok {
				patch.EstimatedMinutes = setField(&estimate)
			}
		case "external_ref":
			ok = true
			if value == nil {
				patch.ExternalRef = setField[*string](nil)
			} else if ref, isString := value.(string); isString {
				patch.ExternalRef = setField(&ref)
			} else {
				ok = false
			}
		case "due_at":
			patch.DueAt, ok = optionalTimeField(value)
		case "defer_until":
			patch.DeferUntil, ok = optionalTimeField(value)
		case "add_labels":
			patch.Labels.Add, ok = value.([]string)
		case "remove_labels":
			patch.Labels.Remove, ok = value.([]string)
		case "set_labels":
			var labels []string
			if labels, ok = value.([]string); ok {
				patch.Labels.Replace = setField(labels)
			}
		case storageissueops.OpMergeMetadata:
			var merge json.RawMessage
			if merge, ok = value.(json.RawMessage); ok {
				patch.Metadata.Merge = setField(merge)
			}
		case storageissueops.OpSetMetadata:
			var flags []string
			if flags, ok = value.([]string); ok {
				set, err := parseSetMetadataFlags(flags)
				if err != nil {
					return issueops.IssuePatch{}, err
				}
				patch.Metadata.Set = set
			}
		case storageissueops.OpUnsetMetadata:
			patch.Metadata.Unset, ok = value.([]string)
		case "wisp":
			var flag bool
			if flag, ok = value.(bool); ok {
				wisp = &flag
			}
		case "no_history":
			var flag bool
			if flag, ok = value.(bool); ok {
				noHistory = &flag
			}
		default:
			return issueops.IssuePatch{}, fmt.Errorf("unsupported update field %q", key)
		}
		if !ok {
			return issueops.IssuePatch{}, fmt.Errorf("unsupported value %T for update field %q", value, key)
		}
	}
	// --ephemeral/--persistent/--no-history/--history select one complete
	// persistence state. The flag parsing already rejects the contradictory
	// pairs; the remaining combinations resolve most-specific-first, which
	// reproduces the column pairs the old two-boolean write produced.
	switch {
	case wisp != nil && *wisp:
		patch.Persistence = setField(issueops.PersistenceModeEphemeral)
	case noHistory != nil && *noHistory:
		patch.Persistence = setField(issueops.PersistenceModeNoHistory)
	case wisp != nil || noHistory != nil:
		patch.Persistence = setField(issueops.PersistenceModePersistent)
	}
	return patch, nil
}

func stringField(value any) (issueops.Field[string], bool) {
	text, ok := value.(string)
	if !ok {
		return issueops.Field[string]{}, false
	}
	return setField(text), true
}

func optionalTimeField(value any) (issueops.Field[*time.Time], bool) {
	if value == nil {
		return setField[*time.Time](nil), true
	}
	at, ok := value.(time.Time)
	if !ok {
		return issueops.Field[*time.Time]{}, false
	}
	return setField(&at), true
}

// parseSetMetadataFlags splits --set-metadata key=value pairs, matching
// storage.ApplyMetadataEdits' parsing so the CLI contract is unchanged. Values
// are always stored as JSON strings (GH#4146).
func parseSetMetadataFlags(flags []string) (map[string]json.RawMessage, error) {
	set := make(map[string]json.RawMessage, len(flags))
	for _, flag := range flags {
		key, value, ok := strings.Cut(flag, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --set-metadata: expected key=value, got %q", flag)
		}
		set[key] = storage.MetadataEditValue(value)
	}
	return set, nil
}

func replacesExistingNotes(existing string, fields map[string]any) bool {
	newNotes, replacing := fields["notes"].(string)
	return replacing && existing != "" && newNotes != existing
}

func warnNotesReplacement(id string) {
	fmt.Fprintf(os.Stderr, "warning: %s: --notes replaced existing notes (use --append-notes to preserve history)\n", id) //nolint:gosec // G705: stderr, not a browser context
}

// ExitGuardMismatch is the exit code when a `bd update` run failed solely
// because --if-assignee/--if-status guards did not match: the precondition no
// longer held, nothing was written, and retrying is pointless — another actor
// won the race. Scripts branch on it to tell "racer won, skip gracefully"
// (13) from infra failure (1, retry/abort). Mixed batches — any failure that
// is NOT a guard mismatch — exit 1, the conservative "something needs a
// retry" verdict. The stderr line carries the machine-greppable sentinel
// text ("assignee mismatch" / "status mismatch") either way.
const ExitGuardMismatch = 13

// isGuardMismatch reports whether err is a bd-wsqvw conditional-update guard
// refusal (stale --if-assignee/--if-status), the failure class that exits
// ExitGuardMismatch instead of 1.
func isGuardMismatch(err error) bool {
	return errors.Is(err, storage.ErrAssigneeMismatch) || errors.Is(err, storage.ErrStatusMismatch)
}

// updateIDFailure records one issue ID that could not be updated and why.
// GuardMismatch marks a --if-assignee/--if-status refusal so JSON consumers
// can distinguish it without parsing the error text.
type updateIDFailure struct {
	ID            string `json:"id"`
	Error         string `json:"error"`
	GuardMismatch bool   `json:"guard_mismatch,omitempty"`
}

// errStrayFlagValuePositional refuses, before any write, a positional argument
// that contains '='. --set-metadata takes ONE key=value per flag, so
// `--set-metadata a=1 b=2` silently turns `b=2` into a positional issue id;
// no issue id contains '=', so such a positional is unambiguously a mis-typed
// flag value. Rejecting it up front prevents a partial write that would apply
// only the pairs that happened to bind to a flag (bd-5247).
func errStrayFlagValuePositional(args []string) error {
	for _, arg := range args {
		if strings.Contains(arg, "=") {
			return fmt.Errorf("positional argument %q contains '=', which no issue id does — this is a mis-typed flag value; repeat the flag per pair, e.g. --set-metadata a=1 --set-metadata b=2", arg)
		}
	}
	return nil
}

// reportUpdateFailures emits a per-ID failure report on stderr and returns a
// nonzero exit error — ExitGuardMismatch when every failure is a
// --if-assignee/--if-status guard refusal, 1 otherwise. In --json mode the
// report is a single compact JSON line — the last line on stderr — so
// callers can parse which IDs failed while stdout keeps the plain
// array-of-updated-issues success shape. In text mode the individual errors
// were already printed inline; this adds a summary naming every failed ID.
func reportUpdateFailures(failures []updateIDFailure, total int) error {
	msg := fmt.Sprintf("%d of %d issues failed to update", len(failures), total)
	if jsonOutput {
		inner := map[string]interface{}{
			"error":  msg,
			"failed": failures,
		}
		var payload interface{}
		if jsonEnvelopeEnabled() {
			payload = map[string]interface{}{
				"schema_version": JSONSchemaVersion,
				"data":           inner,
			}
		} else {
			inner["schema_version"] = JSONSchemaVersion
			payload = inner
		}
		data, err := json.Marshal(payload)
		if err != nil {
			// Marshaling flat strings cannot realistically fail; fall back to
			// the text summary rather than exiting silently.
			fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		} else {
			fmt.Fprintln(os.Stderr, string(data))
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		for _, f := range failures {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", f.ID, f.Error)
		}
	}
	allGuard := len(failures) > 0
	for _, f := range failures {
		if !f.GuardMismatch {
			allGuard = false
			break
		}
	}
	if allGuard {
		return &exitError{Code: ExitGuardMismatch}
	}
	return &exitError{Code: 1}
}

// mergeMetadata merges new metadata JSON into existing metadata.
// Keys from newMeta overwrite keys in existing; keys only in existing are preserved.
// Thin alias over the shared storage helper (also used in-transaction by issueops).
func mergeMetadata(existing, newMeta json.RawMessage) (json.RawMessage, error) {
	return storage.MergeMetadataJSON(existing, newMeta)
}

// applyMetadataEdits applies --set-metadata and --unset-metadata edits to existing metadata.
// Thin alias over the shared storage helper (also used in-transaction by issueops).
func applyMetadataEdits(existing json.RawMessage, setFlags, unsetFlags []string) (json.RawMessage, error) {
	return storage.ApplyMetadataEdits(existing, setFlags, unsetFlags)
}

// toJSONValue stores a CLI metadata value as a JSON string.
// Previous behavior inferred types (numbers, booleans) from content,
// which silently broke map[string]string round-trips (GH#4146).
func toJSONValue(s string) json.RawMessage {
	return storage.MetadataEditValue(s)
}

// updateGuardsFromFlags reads the bd-wsqvw conditional-update guards
// (--if-assignee/--if-status) with presence detected via Changed(), so
// `--if-assignee ""` is a real guard meaning "expected unassigned" rather than
// "no guard" (the unclaim.go idiom). It rejects combining guards with --claim
// (--claim is its own compare-and-set with claim-pool semantics; the guards
// would silently duplicate or contradict it) and guards with no regular field
// update to ride on (the CAS applies to the issues-row UPDATE; label and
// parent edits run outside it and would not be guarded). An --if-status value
// is validated against the same built-in + custom status set as --status, so a
// typo fails fast instead of mismatching forever.
func updateGuardsFromFlags(cmd *cobra.Command, claimFlag bool, updates map[string]interface{}) (ifAssignee, ifStatus *string, err error) {
	if cmd.Flags().Changed("if-assignee") {
		v, _ := cmd.Flags().GetString("if-assignee")
		ifAssignee = &v
	}
	if cmd.Flags().Changed("if-status") {
		v, _ := cmd.Flags().GetString("if-status")
		var customStatuses []string
		if store != nil {
			if cs, csErr := store.GetCustomStatuses(rootCtx); csErr == nil {
				customStatuses = cs
			}
		}
		if !types.Status(v).IsValidWithCustom(customStatuses) {
			return nil, nil, HandleErrorRespectJSON("invalid --if-status %q (built-in: open, in_progress, blocked, deferred, closed, pinned, hooked; or configure custom statuses via 'bd config set status.custom')", v)
		}
		ifStatus = &v
	}
	if ifAssignee == nil && ifStatus == nil {
		return nil, nil, nil
	}
	if claimFlag {
		return nil, nil, HandleErrorRespectJSON("cannot combine --if-assignee/--if-status with --claim (--claim is already an atomic compare-and-set)")
	}
	hasFieldUpdate := false
	for k := range updates {
		switch k {
		case "add_labels", "remove_labels", "set_labels", "parent":
		default:
			hasFieldUpdate = true
		}
	}
	if !hasFieldUpdate {
		return nil, nil, HandleErrorRespectJSON("--if-assignee/--if-status require at least one field update (e.g. -a, -s); label and parent edits are not covered by the guard")
	}
	return ifAssignee, ifStatus, nil
}

func init() {
	updateCmd.Flags().StringP("status", "s", "", "New status")
	registerPriorityFlag(updateCmd, "")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("type", "t", "", "New type (bug|feature|task|epic|chore|decision|spike|story|milestone); custom types require types.custom config; aliases: enhancement/feat→feature, dec/adr→decision")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().Lookup("notes").Usage = "Additional notes (replaces existing notes; use --append-notes to append)"
	updateCmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	updateCmd.Flags().String("spec-id", "", "Link to specification document")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria") // Only fails if flag missing (caught in tests)
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	updateCmd.Flags().String("parent", "", "New parent issue ID (reparents the issue, use empty string to remove parent)")
	updateCmd.Flags().Bool("claim", false, "Atomically claim the issue (sets assignee to you, status to in_progress; idempotent if already claimed by you; issues assigned to a pool alias listed in the claim.pools config are claimable too)")
	// Overrides the live-claim reassign fence (bd-98s5c) and close policy.
	updateCmd.Flags().Bool("force", false, "Override two refusals: let -a/--assignee overwrite another actor's live in_progress claim (use only for abandoned claims — crashed agent, expired lease; prefer bd reclaim), and let -s/--status move the issue into closed (or a configured done status) despite open children or a live blocker (same as bd close --force)")
	// Conditional (compare-and-set) update guards (bd-wsqvw)
	updateCmd.Flags().String("if-assignee", "", "Apply the update only if the current assignee equals this value (--if-assignee '' requires unassigned); a mismatch writes nothing and exits 13 (vs 1 for other failures). Requires a field update; cannot combine with --claim")
	updateCmd.Flags().String("if-status", "", "Apply the update only if the current status equals this value; a mismatch writes nothing and exits 13 (vs 1 for other failures). Requires a field update; cannot combine with --claim")
	// --force (unconditional bypass of the reassign fence) and --if-assignee
	// (write only while a specific assignee still holds it) encode
	// contradictory intent — same rationale as unclaim's pairing. Rejecting the
	// combination stops a script that habitually passes --force from silently
	// dropping its --if-assignee guard.
	updateCmd.MarkFlagsMutuallyExclusive("force", "if-assignee")
	updateCmd.Flags().String("session", "", "Claude Code session ID for status=closed (or set CLAUDE_SESSION_ID env var)")
	// Time-based scheduling flags (GH#820)
	// Examples:
	//   --due=+6h           Due in 6 hours
	//   --due=tomorrow      Due tomorrow
	//   --due="next monday" Due next Monday
	//   --due=2025-01-15    Due on specific date
	//   --due=""            Clear due date
	//   --defer=+1h         Hidden from bd ready for 1 hour
	//   --defer=""          Clear defer (show in bd ready immediately)
	updateCmd.Flags().String("due", "", "Due date/time (empty to clear). Formats: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15")
	updateCmd.Flags().String("defer", "", "Defer until date (empty to clear). Issue hidden from bd ready until then, then auto-wakes to open")
	// Gate fields (bd-z6kw)
	updateCmd.Flags().String("await-id", "", "Set gate await_id (e.g., GitHub run ID for gh:run gates)")
	// Ephemeral/persistent flags
	updateCmd.Flags().Bool("ephemeral", false, "Mark issue as ephemeral (wisp) - not exported to JSONL")
	updateCmd.Flags().Bool("persistent", false, "Mark issue as persistent (promote wisp to regular issue)")
	updateCmd.Flags().Bool("no-history", false, "Mark issue as no-history (skip Dolt commits, not GC-eligible)")
	updateCmd.Flags().Bool("history", false, "Clear no-history flag (re-enable Dolt commit history)")
	// Metadata flag (GH#1413)
	updateCmd.Flags().String("metadata", "", "Set custom metadata (JSON string or @file.json to read from file)")
	// Incremental metadata edits (GH#1406)
	updateCmd.Flags().StringArray("set-metadata", nil, "Set metadata key=value (repeatable, e.g., --set-metadata team=platform)")
	updateCmd.Flags().StringArray("unset-metadata", nil, "Remove metadata key (repeatable, e.g., --unset-metadata team)")
	updateCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(updateCmd)
}
