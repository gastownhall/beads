package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
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
	Args: cobra.MinimumNArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("update")

		// If no IDs provided, use last touched issue
		if len(args) == 0 {
			lastTouched := GetLastTouchedID()
			if lastTouched == "" {
				FatalErrorRespectJSON("no issue ID provided and no last touched issue")
			}
			args = []string{lastTouched}
		}

		updates := make(map[string]interface{})

		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetString("status")
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
				FatalErrorRespectJSON("%v", err)
			}
			updates["priority"] = priority
		}
		if cmd.Flags().Changed("title") {
			title, _ := cmd.Flags().GetString("title")
			updates["title"] = title
		}
		if cmd.Flags().Changed("assignee") {
			assignee, _ := cmd.Flags().GetString("assignee")
			updates["assignee"] = assignee
		}
		description, descChanged := getDescriptionFlag(cmd)
		if descChanged {
			updates["description"] = description
		}
		if cmd.Flags().Changed("design") {
			design, _ := cmd.Flags().GetString("design")
			updates["design"] = design
		}
		if cmd.Flags().Changed("notes") && cmd.Flags().Changed("append-notes") {
			FatalErrorRespectJSON("cannot specify both --notes and --append-notes")
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
			updates["external_ref"] = externalRef
		}
		if cmd.Flags().Changed("spec-id") {
			specID, _ := cmd.Flags().GetString("spec-id")
			updates["spec_id"] = specID
		}
		if cmd.Flags().Changed("estimate") {
			estimate, _ := cmd.Flags().GetInt("estimate")
			if estimate < 0 {
				FatalErrorRespectJSON("estimate must be a non-negative number of minutes")
			}
			updates["estimated_minutes"] = estimate
		}
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Normalize aliases (e.g., "enhancement" -> "feature") before validating
			issueType = utils.NormalizeIssueType(issueType)
			var customTypes []string
			if store != nil {
				ct, err := store.GetCustomTypes(cmd.Context())
				if err != nil {
					// Log DB error but continue with YAML fallback (GH#1499 bd-2ll)
					if !jsonOutput {
						fmt.Fprintf(os.Stderr, "%s Failed to get custom types from DB: %v (falling back to config.yaml)\n",
							ui.RenderWarn("!"), err)
					}
				} else {
					customTypes = ct
				}
			}
			// Fallback to config.yaml when store returns no custom types.
			if len(customTypes) == 0 {
				customTypes = config.GetCustomTypesFromYAML()
			}
			if !types.IssueType(issueType).IsValidWithCustom(customTypes) {
				validTypes := "bug, feature, task, epic, chore, decision"
				if len(customTypes) > 0 {
					validTypes += ", " + joinStrings(customTypes, ", ")
				}
				FatalErrorRespectJSON("invalid issue type %q. Valid types: %s", issueType, validTypes)
			}
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
					FatalErrorRespectJSON("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
				}
				updates["due_at"] = t
			}
		}
		if cmd.Flags().Changed("defer") {
			deferStr, _ := cmd.Flags().GetString("defer")
			if deferStr == "" {
				// Empty string clears the defer_until
				updates["defer_until"] = nil
			} else {
				t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
				if err != nil {
					FatalErrorRespectJSON("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
				}
				// Warn if defer date is in the past (user probably meant future)
				if t.Before(time.Now()) && !jsonOutput {
					fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
						ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
					fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
				}
				updates["defer_until"] = t
			}
		}
		// Ephemeral/persistent flags
		// Note: storage layer uses "wisp" field name, maps to "ephemeral" column
		ephemeralChanged := cmd.Flags().Changed("ephemeral")
		persistentChanged := cmd.Flags().Changed("persistent")
		if ephemeralChanged && persistentChanged {
			FatalErrorRespectJSON("cannot specify both --ephemeral and --persistent flags")
		}
		if ephemeralChanged {
			updates["wisp"] = true
		}
		if persistentChanged {
			updates["wisp"] = false
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
					FatalErrorRespectJSON("failed to read metadata file %s: %v", filePath, err)
				}
				metadataJSON = string(data)
			} else {
				metadataJSON = metadataValue
			}
			// Validate JSON
			if !json.Valid([]byte(metadataJSON)) {
				FatalErrorRespectJSON("invalid JSON in --metadata: must be valid JSON")
			}
			updates["metadata"] = json.RawMessage(metadataJSON)
		}

		// Get claim flag
		claimFlag, _ := cmd.Flags().GetBool("claim")

		if len(updates) == 0 && !claimFlag {
			fmt.Println("No updates specified")
			return
		}

		ctx := rootCtx

		updatedIssues := []*types.Issue{}
		var firstUpdatedID string // Track first successful update for last-touched
		for _, id := range args {
			// Resolve and get issue with routing (e.g., gt-xyz routes to gastown)
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
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
				result.Close()
				continue
			}

			// Handle claim operation atomically using compare-and-swap semantics
			if claimFlag {
				if err := issueStore.ClaimIssue(ctx, result.ResolvedID, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
					result.Close()
					continue
				}
			}

			// Apply regular field updates if any
			regularUpdates := make(map[string]interface{})
			for k, v := range updates {
				if k != "add_labels" && k != "remove_labels" && k != "set_labels" && k != "parent" && k != "append_notes" {
					regularUpdates[k] = v
				}
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
					result.Close()
					continue
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
					result.Close()
					continue
				}
			}

			// Handle parent reparenting
			if newParent, ok := updates["parent"].(string); ok {
				// Validate new parent exists (unless empty string to remove parent)
				if newParent != "" {
					parentIssue, err := issueStore.GetIssue(ctx, newParent)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error getting parent %s: %v\n", newParent, err)
						result.Close()
						continue
					}
					if parentIssue == nil {
						fmt.Fprintf(os.Stderr, "Error: parent issue %s not found\n", newParent)
						result.Close()
						continue
					}
				}

				// Find and remove existing parent-child dependency
				deps, err := issueStore.GetDependencyRecords(ctx, result.ResolvedID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting dependencies for %s: %v\n", id, err)
					result.Close()
					continue
				}
				for _, dep := range deps {
					if dep.Type == types.DepParentChild {
						if err := issueStore.RemoveDependency(ctx, result.ResolvedID, dep.DependsOnID, actor); err != nil {
							fmt.Fprintf(os.Stderr, "Error removing old parent dependency: %v\n", err)
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
						result.Close()
						continue
					}
				}
			}

			// Run update hook
			updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
			if updatedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventUpdate, updatedIssue)
			}

			if jsonOutput {
				if updatedIssue != nil {
					updatedIssues = append(updatedIssues, updatedIssue)
				}
			} else {
				fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), result.ResolvedID)
			}

			// Track first successful update for last-touched
			if firstUpdatedID == "" {
				firstUpdatedID = result.ResolvedID
			}
			result.Close()
		}

		// Set last touched after all updates complete
		if firstUpdatedID != "" {
			SetLastTouchedID(firstUpdatedID)
		}

		if jsonOutput && len(updatedIssues) > 0 {
			outputJSON(updatedIssues)
		}
	},
}

func init() {
	updateCmd.Flags().StringP("status", "s", "", "New status")
	registerPriorityFlag(updateCmd, "")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("type", "t", "", "New type (bug|feature|task|epic|chore|decision); custom types require types.custom config")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().String("spec-id", "", "Link to specification document")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria") // Only fails if flag missing (caught in tests)
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	updateCmd.Flags().String("parent", "", "New parent issue ID (reparents the issue, use empty string to remove parent)")
	updateCmd.Flags().Bool("claim", false, "Atomically claim the issue (sets assignee to you, status to in_progress; fails if already claimed)")
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
	// Metadata flag (GH#1413)
	updateCmd.Flags().String("metadata", "", "Set custom metadata (JSON string or @file.json to read from file)")
	updateCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(updateCmd)
}
