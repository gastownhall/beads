package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
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
		if cmd.Flags().Changed("notes") {
			notes, _ := cmd.Flags().GetString("notes")
			updates["notes"] = notes
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
		if cmd.Flags().Changed("estimate") {
			estimate, _ := cmd.Flags().GetInt("estimate")
			if estimate < 0 {
				FatalErrorRespectJSON("estimate must be a non-negative number of minutes")
			}
			updates["estimated_minutes"] = estimate
		}
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Validate issue type
			if !types.IssueType(issueType).IsValid() {
				FatalErrorRespectJSON("invalid issue type %q. Valid types: bug, feature, task, epic, chore, merge-request, molecule, gate", issueType)
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
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Validate issue type
			if _, err := validation.ParseIssueType(issueType); err != nil {
				FatalErrorRespectJSON("%v", err)
			}
			updates["issue_type"] = issueType
		}

		if len(updates) == 0 {
			fmt.Println("No updates specified")
			return
		}

		ctx := rootCtx

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					FatalErrorRespectJSON("resolving ID %s: %v", id, err)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				FatalErrorRespectJSON("%v", err)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			updatedIssues := []*types.Issue{}
			var firstUpdatedID string // Track first successful update for last-touched
			for _, id := range resolvedIDs {
				updateArgs := &rpc.UpdateArgs{ID: id}

				// Map updates to RPC args
				if status, ok := updates["status"].(string); ok {
					updateArgs.Status = &status
				}
				if priority, ok := updates["priority"].(int); ok {
					updateArgs.Priority = &priority
				}
				if title, ok := updates["title"].(string); ok {
					updateArgs.Title = &title
				}
				if assignee, ok := updates["assignee"].(string); ok {
					updateArgs.Assignee = &assignee
				}
				if description, ok := updates["description"].(string); ok {
					updateArgs.Description = &description
				}
				if design, ok := updates["design"].(string); ok {
					updateArgs.Design = &design
				}
				if notes, ok := updates["notes"].(string); ok {
					updateArgs.Notes = &notes
				}
				if acceptanceCriteria, ok := updates["acceptance_criteria"].(string); ok {
					updateArgs.AcceptanceCriteria = &acceptanceCriteria
				}
				if externalRef, ok := updates["external_ref"].(string); ok {
					updateArgs.ExternalRef = &externalRef
				}
				if estimate, ok := updates["estimated_minutes"].(int); ok {
					updateArgs.EstimatedMinutes = &estimate
				}
				if issueType, ok := updates["issue_type"].(string); ok {
					updateArgs.IssueType = &issueType
				}
				if addLabels, ok := updates["add_labels"].([]string); ok {
					updateArgs.AddLabels = addLabels
				}
				if removeLabels, ok := updates["remove_labels"].([]string); ok {
					updateArgs.RemoveLabels = removeLabels
				}
				if setLabels, ok := updates["set_labels"].([]string); ok {
					updateArgs.SetLabels = setLabels
				}
				if issueType, ok := updates["issue_type"].(string); ok {
					updateArgs.IssueType = &issueType
				}
				if parent, ok := updates["parent"].(string); ok {
					updateArgs.Parent = &parent
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					continue
				}

				var issue types.Issue
				if err := json.Unmarshal(resp.Data, &issue); err == nil {
					// Run update hook
					if hookRunner != nil {
						hookRunner.Run(hooks.EventUpdate, &issue)
					}
					if jsonOutput {
						updatedIssues = append(updatedIssues, &issue)
					}
				}
				if !jsonOutput {
					fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), id)
				}

				// Track first successful update for last-touched
				if firstUpdatedID == "" {
					firstUpdatedID = id
				}
			}

			if jsonOutput && len(updatedIssues) > 0 {
				outputJSON(updatedIssues)
			}

			// Set last touched after all updates complete
			if firstUpdatedID != "" {
				SetLastTouchedID(firstUpdatedID)
			}
			return
		}

		// Direct mode
		updatedIssues := []*types.Issue{}
		var firstUpdatedID string // Track first successful update for last-touched
		for _, id := range resolvedIDs {
			// Check if issue is a template: templates are read-only
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", id, err)
				continue
			}
			if err := validateIssueUpdatable(id, issue); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Apply regular field updates if any
			regularUpdates := make(map[string]interface{})
			for k, v := range updates {
				if k != "add_labels" && k != "remove_labels" && k != "set_labels" && k != "parent" {
					regularUpdates[k] = v
				}
			}
			if len(regularUpdates) > 0 {
				if err := store.UpdateIssue(ctx, id, regularUpdates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
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
				if err := applyLabelUpdates(ctx, store, id, actor, setLabels, addLabels, removeLabels); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating labels for %s: %v\n", id, err)
					continue
				}
			}

			// Handle parent reparenting
			if newParent, ok := updates["parent"].(string); ok {
				// Validate new parent exists (unless empty string to remove parent)
				if newParent != "" {
					parentIssue, err := store.GetIssue(ctx, newParent)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error getting parent %s: %v\n", newParent, err)
						continue
					}
					if parentIssue == nil {
						fmt.Fprintf(os.Stderr, "Error: parent issue %s not found\n", newParent)
						continue
					}
				}

				// Find and remove existing parent-child dependency
				deps, err := store.GetDependencyRecords(ctx, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting dependencies for %s: %v\n", id, err)
					continue
				}
				for _, dep := range deps {
					if dep.Type == types.DepParentChild {
						if err := store.RemoveDependency(ctx, id, dep.DependsOnID, actor); err != nil {
							fmt.Fprintf(os.Stderr, "Error removing old parent dependency: %v\n", err)
						}
						break
					}
				}

				// Add new parent-child dependency (if not removing parent)
				if newParent != "" {
					newDep := &types.Dependency{
						IssueID:     id,
						DependsOnID: newParent,
						Type:        types.DepParentChild,
					}
					if err := store.AddDependency(ctx, newDep, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error adding parent dependency: %v\n", err)
						continue
					}
				}
			}

			// Run update hook
			updatedIssue, _ := store.GetIssue(ctx, id)
			if updatedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventUpdate, updatedIssue)
			}

			if jsonOutput {
				if updatedIssue != nil {
					updatedIssues = append(updatedIssues, updatedIssue)
				}
			} else {
				fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), id)
			}

			// Track first successful update for last-touched
			if firstUpdatedID == "" {
				firstUpdatedID = id
			}
		}

		// Set last touched after all updates complete
		if firstUpdatedID != "" {
			SetLastTouchedID(firstUpdatedID)
		}

		// Schedule auto-flush if any issues were updated
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
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
	updateCmd.Flags().StringP("type", "t", "", "New type (bug|feature|task|epic|chore|merge-request|molecule|gate)")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().String("notes", "", "Additional notes")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria") // Only fails if flag missing (caught in tests)
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	updateCmd.Flags().String("parent", "", "New parent issue ID (reparents the issue, use empty string to remove parent)")
	rootCmd.AddCommand(updateCmd)
}
