package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// registerCommonIssueFlags registers flags common to create and update commands.
func registerCommonIssueFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("assignee", "a", "", "Assignee")
	cmd.Flags().StringP("description", "d", "", "Issue description")
	cmd.Flags().String("body", "", "Alias for --description (GitHub CLI convention)")
	_ = cmd.Flags().MarkHidden("body") // Hidden alias for agent/CLI ergonomics
	cmd.Flags().String("body-file", "", "Read description from file (use - for stdin)")
	cmd.Flags().String("description-file", "", "Alias for --body-file")
	_ = cmd.Flags().MarkHidden("description-file") // Hidden alias
	cmd.Flags().String("design", "", "Design notes")
	cmd.Flags().String("acceptance", "", "Acceptance criteria")
	cmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
}

// getDescriptionFlag retrieves the description value, checking --body-file, --description-file,
// --description, and --body (in that order of precedence).
// Returns the value and whether any flag was explicitly changed.
func getDescriptionFlag(cmd *cobra.Command) (string, bool) {
	bodyFileChanged := cmd.Flags().Changed("body-file")
	descFileChanged := cmd.Flags().Changed("description-file")
	descChanged := cmd.Flags().Changed("description")
	bodyChanged := cmd.Flags().Changed("body")

	// Check for conflicting file flags
	if bodyFileChanged && descFileChanged {
		bodyFile, _ := cmd.Flags().GetString("body-file")
		descFile, _ := cmd.Flags().GetString("description-file")
		if bodyFile != descFile {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both --body-file and --description-file with different values\n")
			os.Exit(1)
		}
	}

	// File flags take precedence over string flags
	if bodyFileChanged || descFileChanged {
		var filePath string
		if bodyFileChanged {
			filePath, _ = cmd.Flags().GetString("body-file")
		} else {
			filePath, _ = cmd.Flags().GetString("description-file")
		}

		// Error if both file and string flags are specified
		if descChanged || bodyChanged {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both --body-file and --description/--body\n")
			os.Exit(1)
		}

		content, err := readBodyFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading body file: %v\n", err)
			os.Exit(1)
		}
		return content, true
	}

	// Error if both description and body are specified with different values
	if descChanged && bodyChanged {
		desc, _ := cmd.Flags().GetString("description")
		body, _ := cmd.Flags().GetString("body")
		if desc != body {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both --description and --body with different values\n")
			fmt.Fprintf(os.Stderr, "  --description: %q\n", desc)
			fmt.Fprintf(os.Stderr, "  --body:        %q\n", body)
			os.Exit(1)
		}
	}

	// Return whichever was set (or description's value if neither)
	if bodyChanged {
		body, _ := cmd.Flags().GetString("body")
		return body, true
	}

	desc, _ := cmd.Flags().GetString("description")
	return desc, descChanged
}

// readBodyFile reads the description content from a file.
// If filePath is "-", reads from stdin.
func readBodyFile(filePath string) (string, error) {
	var reader io.Reader

	if filePath == "-" {
		reader = os.Stdin
	} else {
		// #nosec G304 - filePath comes from user flag, validated by caller
		file, err := os.Open(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to open file: %w", err)
		}
		defer file.Close()
		reader = file
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

// registerPriorityFlag registers the priority flag with a specific default value.
func registerPriorityFlag(cmd *cobra.Command, defaultVal string) {
	cmd.Flags().StringP("priority", "p", defaultVal, "Priority (0-4 or P0-P4, 0=highest)")
}
