package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var editCmd = &cobra.Command{
	Use:     "edit [id]",
	GroupID: "issues",
	Short:   "Edit an issue field in $EDITOR",
	Long: `Edit an issue field using your configured $EDITOR.

By default, edits the description. Use flags to edit other fields.

Examples:
  bd edit bd-42                    # Edit description
  bd edit bd-42 --title            # Edit title
  bd edit bd-42 --design           # Edit design notes
  bd edit bd-42 --notes            # Edit notes
  bd edit bd-42 --acceptance       # Edit acceptance criteria`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("edit")
		id := args[0]
		ctx := rootCtx

		// Resolve partial ID if in direct mode
		if daemonClient == nil {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				FatalErrorRespectJSON("resolving %s: %v", id, err)
			}
			id = fullID
		}

		// Determine which field to edit
		fieldToEdit := "description"
		if cmd.Flags().Changed("title") {
			fieldToEdit = "title"
		} else if cmd.Flags().Changed("design") {
			fieldToEdit = "design"
		} else if cmd.Flags().Changed("notes") {
			fieldToEdit = "notes"
		} else if cmd.Flags().Changed("acceptance") {
			fieldToEdit = "acceptance_criteria"
		}

		// Get the editor from environment
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			// Try common defaults
			for _, defaultEditor := range []string{"vim", "vi", "nano", "emacs"} {
				if _, err := exec.LookPath(defaultEditor); err == nil {
					editor = defaultEditor
					break
				}
			}
		}
		if editor == "" {
			FatalErrorRespectJSON("no editor found. Set $EDITOR or $VISUAL environment variable")
		}

		// Get the current issue
		var issue *types.Issue
		var err error

		if daemonClient != nil {
			// Daemon mode
			showArgs := &rpc.ShowArgs{ID: id}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				FatalErrorRespectJSON("fetching issue %s: %v", id, err)
			}

			issue = &types.Issue{}
			if err := json.Unmarshal(resp.Data, issue); err != nil {
				FatalErrorRespectJSON("parsing issue data: %v", err)
			}
		} else {
			// Direct mode
			issue, err = store.GetIssue(ctx, id)
			if err != nil {
				FatalErrorRespectJSON("fetching issue %s: %v", id, err)
			}
			if issue == nil {
				FatalErrorRespectJSON("issue %s not found", id)
			}
		}

		// Get the current field value
		var currentValue string
		switch fieldToEdit {
		case "title":
			currentValue = issue.Title
		case "description":
			currentValue = issue.Description
		case "design":
			currentValue = issue.Design
		case "notes":
			currentValue = issue.Notes
		case "acceptance_criteria":
			currentValue = issue.AcceptanceCriteria
		}

		// Create a temporary file with the current value
		tmpFile, err := os.CreateTemp("", fmt.Sprintf("bd-edit-%s-*.txt", fieldToEdit))
		if err != nil {
			FatalErrorRespectJSON("creating temp file: %v", err)
		}
		tmpPath := tmpFile.Name()
		defer func() { _ = os.Remove(tmpPath) }()

		// Write current value to temp file
		if _, err := tmpFile.WriteString(currentValue); err != nil {
			_ = tmpFile.Close()
			FatalErrorRespectJSON("writing to temp file: %v", err)
		}
		_ = tmpFile.Close()

		// Open the editor
		editorCmd := exec.Command(editor, tmpPath) //nolint:gosec // G204: editor from trusted $EDITOR/$VISUAL env or known defaults
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		if err := editorCmd.Run(); err != nil {
			FatalErrorRespectJSON("running editor: %v", err)
		}

		// Read the edited content
		// #nosec G304 -- tmpPath was created earlier in this function
		editedContent, err := os.ReadFile(tmpPath)
		if err != nil {
			FatalErrorRespectJSON("reading edited file: %v", err)
		}

		newValue := string(editedContent)

		// Check if the value changed
		if newValue == currentValue {
			fmt.Println("No changes made")
			return
		}

		// Validate title if editing title
		if fieldToEdit == "title" && strings.TrimSpace(newValue) == "" {
			FatalErrorRespectJSON("title cannot be empty")
		}

		// Update the issue
		updates := map[string]interface{}{
			fieldToEdit: newValue,
		}

		if daemonClient != nil {
			// Daemon mode
			updateArgs := &rpc.UpdateArgs{ID: id}

			switch fieldToEdit {
			case "title":
				updateArgs.Title = &newValue
			case "description":
				updateArgs.Description = &newValue
			case "design":
				updateArgs.Design = &newValue
			case "notes":
				updateArgs.Notes = &newValue
			case "acceptance_criteria":
				updateArgs.AcceptanceCriteria = &newValue
			}

			_, err := daemonClient.Update(updateArgs)
			if err != nil {
				FatalErrorRespectJSON("updating issue: %v", err)
			}
		} else {
			// Direct mode
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				FatalErrorRespectJSON("updating issue: %v", err)
			}
			markDirtyAndScheduleFlush()
		}

		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", ui.RenderPass("✓"), fieldName, id)
	},
}

func init() {
	editCmd.Flags().Bool("title", false, "Edit the title")
	editCmd.Flags().Bool("description", false, "Edit the description (default)")
	editCmd.Flags().Bool("design", false, "Edit the design notes")
	editCmd.Flags().Bool("notes", false, "Edit the notes")
	editCmd.Flags().Bool("acceptance", false, "Edit the acceptance criteria")
	rootCmd.AddCommand(editCmd)
}
