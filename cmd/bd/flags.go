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
	cmd.Flags().StringP("message", "m", "", "Alias for --description (git commit convention)")
	_ = cmd.Flags().MarkHidden("message") // Hidden alias for muscle memory from git commit -m
	cmd.Flags().String("body-file", "", "Read description from file (use - for stdin)")
	cmd.Flags().String("description-file", "", "Alias for --body-file")
	_ = cmd.Flags().MarkHidden("description-file") // Hidden alias
	cmd.Flags().String("design", "", "Design notes")
	cmd.Flags().String("acceptance", "", "Acceptance criteria")
	cmd.Flags().String("notes", "", "Additional notes")
	cmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
}

// getDescriptionFlag retrieves the description value, checking --body-file, --description-file,
// --description, and --body (in that order of precedence).
// Supports reading from stdin via --description=- or --body=- (useful when description
// contains apostrophes or other characters that are hard to escape in shell).
// Returns the value and whether any flag was explicitly changed.
func getDescriptionFlag(cmd *cobra.Command) (string, bool) {
	bodyFileChanged := cmd.Flags().Changed("body-file")
	descFileChanged := cmd.Flags().Changed("description-file")
	descChanged := cmd.Flags().Changed("description")
	bodyChanged := cmd.Flags().Changed("body")
	messageChanged := cmd.Flags().Changed("message")

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
		if descChanged || bodyChanged || messageChanged {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both --body-file and --description/--body/--message\n")
			os.Exit(1)
		}

		content, err := readBodyFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading body file: %v\n", err)
			os.Exit(1)
		}
		return content, true
	}

	// Check if description, body, or message is "-" (read from stdin)
	// This provides a convenient shorthand: --description=- instead of --body-file=-
	desc, _ := cmd.Flags().GetString("description")
	body, _ := cmd.Flags().GetString("body")
	message, _ := cmd.Flags().GetString("message")

	if desc == "-" || body == "-" || message == "-" {
		// Error if multiple are set to different values
		values := make(map[string]string)
		if descChanged {
			values["--description"] = desc
		}
		if bodyChanged {
			values["--body"] = body
		}
		if messageChanged {
			values["--message"] = message
		}
		if len(values) > 1 {
			var firstVal string
			for _, v := range values {
				if firstVal == "" {
					firstVal = v
				} else if v != firstVal {
					fmt.Fprintf(os.Stderr, "Error: cannot specify multiple description flags with different values\n")
					for flag, val := range values {
						fmt.Fprintf(os.Stderr, "  %s: %q\n", flag, val)
					}
					os.Exit(1)
				}
			}
		}
		content, err := readBodyFile("-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading from stdin: %v\n", err)
			os.Exit(1)
		}
		return content, true
	}

	// Error if multiple description flags are specified with different values
	changedCount := 0
	var firstVal string
	var firstFlag string
	if descChanged {
		changedCount++
		firstVal = desc
		firstFlag = "--description"
	}
	if bodyChanged {
		changedCount++
		if firstVal == "" {
			firstVal = body
			firstFlag = "--body"
		} else if body != firstVal {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both %s and --body with different values\n", firstFlag)
			fmt.Fprintf(os.Stderr, "  %s: %q\n", firstFlag, firstVal)
			fmt.Fprintf(os.Stderr, "  --body:        %q\n", body)
			os.Exit(1)
		}
	}
	if messageChanged {
		changedCount++
		if firstVal == "" {
			firstVal = message
			firstFlag = "--message"
		} else if message != firstVal {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both %s and --message with different values\n", firstFlag)
			fmt.Fprintf(os.Stderr, "  %s: %q\n", firstFlag, firstVal)
			fmt.Fprintf(os.Stderr, "  --message:     %q\n", message)
			os.Exit(1)
		}
	}

	// Return whichever was set (priority: description > body > message)
	if descChanged {
		return desc, true
	}
	if bodyChanged {
		return body, true
	}
	if messageChanged {
		return message, true
	}

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
