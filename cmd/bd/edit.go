package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("edit")

		evt := metrics.NewCommandEvent("edit")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		id := args[0]
		ctx := rootCtx

		// Resolve ID with prefix routing (supports cross-rig edits like `bd edit xe-5ls`)
		result, err := resolveAndGetIssueForMutation(ctx, store, id)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}
		defer result.Close()
		id = result.ResolvedID
		issueStore := result.Store

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

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			for _, defaultEditor := range []string{"vim", "vi", "nano", "emacs"} {
				if _, err := exec.LookPath(defaultEditor); err == nil {
					editor = defaultEditor
					break
				}
			}
		}
		if editor == "" {
			return HandleErrorRespectJSON("no editor found. Set $EDITOR or $VISUAL environment variable")
		}

		issue := result.Issue

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

		tmpFile, err := os.CreateTemp("", fmt.Sprintf("bd-edit-%s-*.txt", fieldToEdit))
		if err != nil {
			return HandleErrorRespectJSON("creating temp file: %v", err)
		}
		tmpPath := tmpFile.Name()
		editSaved := false
		defer func() {
			if editSaved {
				_ = os.Remove(tmpPath)
			}
		}()

		if _, err := tmpFile.WriteString(currentValue); err != nil {
			_ = tmpFile.Close()
			return HandleErrorRespectJSON("writing to temp file: %v", err)
		}
		_ = tmpFile.Close()

		editorParts := strings.Fields(editor)
		editorArgs := append(editorParts[1:], tmpPath)
		editorCmd := exec.Command(editorParts[0], editorArgs...) //nolint:gosec // G204: editor from trusted $EDITOR/$VISUAL env or known defaults
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		if err := editorCmd.Run(); err != nil {
			return HandleErrorRespectJSON("running editor: %v", err)
		}

		// #nosec G304 -- tmpPath was created earlier in this function
		editedContent, err := os.ReadFile(tmpPath)
		if err != nil {
			return HandleErrorRespectJSON("reading edited file: %v", err)
		}

		newValue := strings.TrimSpace(string(editedContent))

		if newValue == currentValue {
			editSaved = true
			fmt.Println("No changes made")
			return nil
		}

		if fieldToEdit == "title" && newValue == "" {
			return HandleErrorRespectJSON("title cannot be empty")
		}

		updates := map[string]interface{}{
			fieldToEdit: newValue,
		}

		err = issueStore.UpdateIssue(ctx, id, updates, actor)
		if err != nil {
			if accessor, ok := storage.UnwrapStore(issueStore).(storage.RawDBAccessor); ok {
				if pingErr := accessor.DB().PingContext(ctx); pingErr != nil {
					accessor.DB().SetConnMaxIdleTime(0)
					_ = accessor.DB().PingContext(ctx)
				}
			}
			err = issueStore.UpdateIssue(ctx, id, updates, actor)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Your edits are preserved in: %s\n", tmpPath)
			return HandleErrorRespectJSON("updating issue: %v", err)
		}
		editSaved = true
		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "edit",
			IssueIDs: []string{id},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Your edits are preserved in: %s\n", tmpPath)
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		displayTitle := issue.Title
		if fieldToEdit == "title" {
			displayTitle = newValue
		}

		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", ui.RenderPass("✓"), fieldName, formatFeedbackID(id, displayTitle))
		return nil
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
