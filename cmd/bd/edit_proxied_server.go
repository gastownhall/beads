package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/ui"
)

func runEditProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	id := args[0]

	fieldToEdit := "description"
	switch {
	case cmd.Flags().Changed("title"):
		fieldToEdit = "title"
	case cmd.Flags().Changed("design"):
		fieldToEdit = "design"
	case cmd.Flags().Changed("notes"):
		fieldToEdit = "notes"
	case cmd.Flags().Changed("acceptance"):
		fieldToEdit = "acceptance_criteria"
	}

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	issue, _, err := proxiedGetIssueOrWisp(ctx, uw, id)
	if err != nil {
		uw.Close(ctx)
		return HandleErrorRespectJSON("resolving %s: %v", id, err)
	}
	if issue == nil {
		uw.Close(ctx)
		return HandleErrorRespectJSON("issue %s not found", id)
	}
	uw.Close(ctx)
	id = issue.ID

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

	updated, err := proxiedUpdateIssueFields(ctx, id, "bd: edit "+id, map[string]any{fieldToEdit: newValue})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Your edits are preserved in: %s\n", tmpPath) //nolint:gosec // G705: stderr, not a browser context
		return HandleErrorRespectJSON("updating issue: %v", err)
	}
	editSaved = true

	displayTitle := issue.Title
	if fieldToEdit == "title" {
		displayTitle = newValue
	}
	if updated != nil {
		displayTitle = updated.Title
	}

	fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
	fmt.Printf("%s Updated %s for issue: %s\n", ui.RenderPass("✓"), fieldName, formatFeedbackID(id, displayTitle))
	return nil
}
