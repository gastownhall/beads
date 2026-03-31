package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
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

		// Resolve ID with prefix routing (supports cross-rig edits like `bd edit xe-5ls`)
		result, err := resolveAndGetIssueWithRouting(ctx, store, id)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}
		defer result.Close()
		id = result.ResolvedID
		issueStore := result.Store

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

		issue := result.Issue

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
		editSaved := false
		defer func() {
			if editSaved {
				_ = os.Remove(tmpPath)
			}
		}()

		// Write current value to temp file
		if _, err := tmpFile.WriteString(currentValue); err != nil {
			_ = tmpFile.Close()
			FatalErrorRespectJSON("writing to temp file: %v", err)
		}
		_ = tmpFile.Close()

		// Open the editor - parse command and args (handles "vim -w" or "zeditor --wait")
		editorParts := strings.Fields(editor)
		editorArgs := append(editorParts[1:], tmpPath)
		editorCmd := exec.Command(editorParts[0], editorArgs...) //nolint:gosec // G204: editor from trusted $EDITOR/$VISUAL env or known defaults
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

		newValue := strings.TrimSpace(string(editedContent))

		// Check if the value changed
		if newValue == currentValue {
			editSaved = true // no changes — safe to remove temp file
			fmt.Println("No changes made")
			return
		}

		// Validate title if editing title
		if fieldToEdit == "title" && newValue == "" {
			FatalErrorRespectJSON("title cannot be empty")
		}

		// Update the issue — retry once if the DB connection went stale
		// during a long editor session (GH-2267).
		// Use the routed store for cross-rig mutations.
		updates := map[string]interface{}{
			fieldToEdit: newValue,
		}

		err = issueStore.UpdateIssue(ctx, id, updates, actor)
		if err != nil {
			// Connection may have gone stale while the editor was open.
			// Ping to force the pool to discard dead connections, then retry.
			if accessor, ok := storage.UnwrapStore(issueStore).(storage.RawDBAccessor); ok {
				if pingErr := accessor.DB().PingContext(ctx); pingErr != nil {
					// Ping failed — try to force a fresh connection via sql.DB pool reset.
					accessor.DB().SetConnMaxIdleTime(0)
					_ = accessor.DB().PingContext(ctx)
				}
			}
			err = issueStore.UpdateIssue(ctx, id, updates, actor)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Your edits are preserved in: %s\n", tmpPath)
			FatalErrorRespectJSON("updating issue: %v", err)
		}
		editSaved = true

		// Embedded mode: flush Dolt commit.
		if isEmbeddedMode() {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		displayTitle := issue.Title
		if fieldToEdit == "title" {
			displayTitle = newValue
		}

		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", ui.RenderPass("✓"), fieldName, formatFeedbackID(id, displayTitle))
	},
}

func init() {
	editCmd.Flags().Bool("title", false, "Edit the title")
	editCmd.Flags().Bool("description", false, "Edit the description (default)")
	editCmd.Flags().Bool("design", false, "Edit the design notes")
	editCmd.Flags().Bool("notes", false, "Edit the notes")
	editCmd.Flags().Bool("acceptance", false, "Edit the acceptance criteria")
	editCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(editCmd)
}
