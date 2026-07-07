package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

var updateCmd = &cobra.Command{
	Use:     "update [id...]",
	GroupID: "issues",
	Short:   "Update one or more issues",
	Long: `Update one or more issues.

If no issue ID is provided, updates the last touched issue (from most recent
create, update, show, or close operation).`,
	Args:          cobra.MinimumNArgs(0),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("update")

		evt := metrics.NewCommandEvent("update")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			runUpdateProxiedServer(cmd, rootCtx, args)
			return nil
		}

		// If no IDs provided, use last touched issue
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
			updates["append_notes"] = appendNotes
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
			updates["add_labels"] = addLabels
		}
		if cmd.Flags().Changed("remove-label") {
			removeLabels, _ := cmd.Flags().GetStringSlice("remove-label")
			updates["remove_labels"] = removeLabels
		}
		if cmd.Flags().Changed("set-labels") {
			setLabels, _ := cmd.Flags().GetStringSlice("set-labels")
			updates["set_labels"] = setLabels
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
						ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
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
			updates["metadata"] = json.RawMessage(metadataJSON)
		}

		// Incremental metadata edits (GH#1406)
		setMetadataFlags, _ := cmd.Flags().GetStringArray("set-metadata")
		unsetMetadataFlags, _ := cmd.Flags().GetStringArray("unset-metadata")
		if (len(setMetadataFlags) > 0 || len(unsetMetadataFlags) > 0) && cmd.Flags().Changed("metadata") {
			return HandleErrorRespectJSON("cannot combine --metadata with --set-metadata or --unset-metadata")
		}
		if len(setMetadataFlags) > 0 || len(unsetMetadataFlags) > 0 {
			updates["_set_metadata"] = setMetadataFlags
			updates["_unset_metadata"] = unsetMetadataFlags
		}

		// Get claim flag
		claimFlag, _ := cmd.Flags().GetBool("claim")

		if len(updates) == 0 && !claimFlag {
			fmt.Println("No updates specified")
			return nil
		}

		ctx := rootCtx

		updatedIssues := []*types.Issue{}
		var firstUpdatedID string // Track first successful update for last-touched
		mutatedStores := map[storage.DoltStorage][]string{}
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
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}
			issue := result.Issue
			issueStore := result.Store

			if err := validateIssueUpdatable(id, issue); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				closeIfUnmutated(result)
				continue
			}

			// Handle claim operation atomically using compare-and-swap semantics
			if claimFlag {
				if err := issueStore.ClaimIssue(ctx, result.ResolvedID, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
					closeIfUnmutated(result)
					continue
				}
				trackMutation(result)
			}

			// Apply regular field updates if any
			regularUpdates := make(map[string]interface{})
			for k, v := range updates {
				if k != "add_labels" && k != "remove_labels" && k != "set_labels" && k != "parent" && k != "append_notes" &&
					k != "_set_metadata" && k != "_unset_metadata" {
					regularUpdates[k] = v
				}
			}
			// GH#3233: --defer="" restores ready visibility only if the issue
			// was actually deferred. Other statuses (blocked, in_progress, …)
			// shouldn't be clobbered just because defer_until was stale.
			if clearDeferStatus && issue.Status == types.StatusDeferred {
				regularUpdates["status"] = string(types.StatusOpen)
			}

			// Handle --metadata: merge with existing metadata instead of replacing
			if newMeta, ok := regularUpdates["metadata"].(json.RawMessage); ok && len(issue.Metadata) > 0 {
				merged, err := mergeMetadata(issue.Metadata, newMeta)
				if err != nil {
					return HandleErrorRespectJSON("metadata merge failed for %s: %v", id, err)
				}
				regularUpdates["metadata"] = merged
			}
			// Handle incremental metadata edits (GH#1406)
			if setMeta, ok := updates["_set_metadata"].([]string); ok {
				unsetMeta, _ := updates["_unset_metadata"].([]string)
				merged, err := applyMetadataEdits(issue.Metadata, setMeta, unsetMeta)
				if err != nil {
					return HandleErrorRespectJSON("metadata edit failed for %s: %v", id, err)
				}
				regularUpdates["metadata"] = merged
			}
			// Handle append_notes: combine existing notes with new content
			if appendNotes, ok := updates["append_notes"].(string); ok {
				combined := issue.Notes
				if combined != "" {
					combined += "\n"
				}
				combined += appendNotes
				regularUpdates["notes"] = combined
			}
			if len(regularUpdates) > 0 {
				if err := issueStore.UpdateIssue(ctx, result.ResolvedID, regularUpdates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					closeIfUnmutated(result)
					continue
				}
				trackMutation(result)
				// Audit log key field changes (survives Dolt GC flatten)
				if s, ok := regularUpdates["status"].(string); ok {
					audit.LogFieldChange(result.ResolvedID, "status", string(issue.Status), s, actor, "")
				}
				if a, ok := regularUpdates["assignee"].(string); ok {
					audit.LogFieldChange(result.ResolvedID, "assignee", issue.Assignee, a, actor, "")
				}
				if p, ok := regularUpdates["priority"].(int); ok {
					audit.LogFieldChange(result.ResolvedID, "priority", fmt.Sprintf("%d", issue.Priority), fmt.Sprintf("%d", p), actor, "")
				}
			}

			// Handle label operations
			var setLabels, addLabels, removeLabels []string
			if v, ok := updates["set_labels"].([]string); ok {
				setLabels = v
			}
			if v, ok := updates["add_labels"].([]string); ok {
				addLabels = v
			}
			if v, ok := updates["remove_labels"].([]string); ok {
				removeLabels = v
			}
			if len(setLabels) > 0 || len(addLabels) > 0 || len(removeLabels) > 0 {
				if err := applyLabelUpdates(ctx, issueStore, result.ResolvedID, actor, setLabels, addLabels, removeLabels); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating labels for %s: %v\n", id, err)
					closeIfUnmutated(result)
					continue
				}
				trackMutation(result)
			}

			// Handle parent reparenting
			if newParent, ok := updates["parent"].(string); ok {
				// Validate new parent exists (unless empty string to remove parent)
				if newParent != "" {
					parentIssue, err := issueStore.GetIssue(ctx, newParent)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error getting parent %s: %v\n", newParent, err)
						closeIfUnmutated(result)
						continue
					}
					if parentIssue == nil {
						fmt.Fprintf(os.Stderr, "Error: parent issue %s not found\n", newParent)
						closeIfUnmutated(result)
						continue
					}
				}

				// Find and remove existing parent-child dependency
				deps, err := issueStore.GetDependencyRecords(ctx, result.ResolvedID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting dependencies for %s: %v\n", id, err)
					closeIfUnmutated(result)
					continue
				}
				for _, dep := range deps {
					if dep.Type == types.DepParentChild {
						if err := issueStore.RemoveDependency(ctx, result.ResolvedID, dep.DependsOnID, actor); err != nil {
							fmt.Fprintf(os.Stderr, "Error removing old parent dependency: %v\n", err)
						} else {
							trackMutation(result)
						}
						break
					}
				}

				// Add new parent-child dependency (if not removing parent)
				if newParent != "" {
					newDep := &types.Dependency{
						IssueID:     result.ResolvedID,
						DependsOnID: newParent,
						Type:        types.DepParentChild,
					}
					if err := issueStore.AddDependency(ctx, newDep, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error adding parent dependency: %v\n", err)
						closeIfUnmutated(result)
						continue
					}
					trackMutation(result)
				}
			}

			// Re-fetch for display
			updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
			updateTitle := ""
			if updatedIssue != nil {
				updateTitle = updatedIssue.Title
			}

			if jsonOutput {
				if updatedIssue != nil {
					updatedIssues = append(updatedIssues, updatedIssue)
				}
			} else {
				fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, updateTitle))
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

		if len(args) > 0 && firstUpdatedID == "" {
			return SilentExit()
		}
		return nil
	},
}

// mergeMetadata merges new metadata JSON into existing metadata.
// Keys from newMeta overwrite keys in existing; keys only in existing are preserved.
func mergeMetadata(existing, newMeta json.RawMessage) (json.RawMessage, error) {
	base := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &base); err != nil {
				return nil, fmt.Errorf("existing metadata is not a JSON object: %w", err)
			}
		}
	}

	incoming := make(map[string]json.RawMessage)
	if err := json.Unmarshal(newMeta, &incoming); err != nil {
		return nil, fmt.Errorf("new metadata is not a JSON object: %w", err)
	}

	for k, v := range incoming {
		base[k] = v
	}

	result, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged metadata: %w", err)
	}
	return json.RawMessage(result), nil
}

// applyMetadataEdits applies --set-metadata and --unset-metadata edits to existing metadata.
// Returns the merged JSON as json.RawMessage.
func applyMetadataEdits(existing json.RawMessage, setFlags, unsetFlags []string) (json.RawMessage, error) {
	// Parse existing metadata (or start with empty object)
	data := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &data); err != nil {
				return nil, fmt.Errorf("existing metadata is not a JSON object: %w", err)
			}
		}
	}

	// Apply --set-metadata key=value pairs
	for _, kv := range setFlags {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --set-metadata: expected key=value, got %q", kv)
		}
		if err := storage.ValidateMetadataKey(k); err != nil {
			return nil, err
		}
		// Store as JSON value: try to preserve type (number, bool, null)
		data[k] = toJSONValue(v)
	}

	// Apply --unset-metadata keys
	for _, k := range unsetFlags {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return nil, err
		}
		delete(data, k)
	}

	result, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return json.RawMessage(result), nil
}

// toJSONValue converts a string value to its most appropriate JSON representation.
// Recognizes numbers, booleans, and null; everything else becomes a JSON string.
func toJSONValue(s string) json.RawMessage {
	// Check for null
	if s == "null" {
		return json.RawMessage("null")
	}
	// Check for booleans
	if s == "true" || s == "false" {
		return json.RawMessage(s)
	}
	// Check for numbers (integer or float)
	if _, err := fmt.Sscanf(s, "%f", new(float64)); err == nil {
		// Verify it round-trips cleanly (not NaN, Inf, etc.)
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
	}
	// Default to JSON string
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func init() {
	updateCmd.Flags().StringP("status", "s", "", "New status")
	registerPriorityFlag(updateCmd, "")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("type", "t", "", "New type (bug|feature|task|epic|chore|decision); custom types require types.custom config")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	updateCmd.Flags().String("spec-id", "", "Link to specification document")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria") // Only fails if flag missing (caught in tests)
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	updateCmd.Flags().String("parent", "", "New parent issue ID (reparents the issue, use empty string to remove parent)")
	updateCmd.Flags().Bool("claim", false, "Atomically claim the issue (sets assignee to you, status to in_progress; idempotent if already claimed by you)")
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
	updateCmd.Flags().String("defer", "", "Defer until date (empty to clear). Issue hidden from bd ready until then")
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
