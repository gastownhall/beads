// Package ui provides terminal styling and pager support for beads CLI output.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// PagerOptions controls pager behavior
type PagerOptions struct {
	// NoPager disables pager for this command (--no-pager flag)
	NoPager bool
}

// shouldUsePager determines if output should be piped to a pager.
// Returns false if:
// - NoPager option is set
// - BD_NO_PAGER environment variable is set
// - stdout is not a TTY (e.g., piped to another command)
func shouldUsePager(opts PagerOptions) bool {
	if opts.NoPager {
		return false
	}

	if os.Getenv("BD_NO_PAGER") != "" {
		return false
	}

	// Check if stdout is a terminal
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}

	return true
}

// getPagerCommand returns the pager command to use.
// Checks BD_PAGER, then PAGER, defaults to "less".
func getPagerCommand() string {
	if pager := os.Getenv("BD_PAGER"); pager != "" {
		return pager
	}
	if pager := os.Getenv("PAGER"); pager != "" {
		return pager
	}
	return "less"
}

// getTerminalHeight returns the height of the terminal in lines.
// Returns 0 if unable to determine (not a TTY).
func getTerminalHeight() int {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}

	_, height, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return height
}

// contentHeight counts the number of lines in the content.
func contentHeight(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// ToPager pipes content to a pager if appropriate.
// If pager should not be used (not a TTY, --no-pager, etc.), prints directly.
// If content fits in terminal, prints directly without pager.
func ToPager(content string, opts PagerOptions) error {
	if !shouldUsePager(opts) {
		fmt.Print(content)
		return nil
	}

	// Check if content exceeds terminal height
	termHeight := getTerminalHeight()
	if termHeight > 0 && contentHeight(content) <= termHeight-1 {
		// Content fits in terminal, no pager needed
		fmt.Print(content)
		return nil
	}

	// Use pager
	pagerCmd := getPagerCommand()

	// Parse pager command (may include arguments like "less -R")
	parts := strings.Fields(pagerCmd)
	if len(parts) == 0 {
		fmt.Print(content)
		return nil
	}

	cmd := exec.Command(parts[0], parts[1:]...) // #nosec G204 - pager command is user-configurable by design
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Set LESS environment variable for sensible defaults if not already set
	// -R: Allow ANSI color codes
	// -F: Quit if content fits on one screen
	// -X: Don't clear screen on exit
	if os.Getenv("LESS") == "" {
		cmd.Env = append(os.Environ(), "LESS=-RFX")
	} else {
		cmd.Env = os.Environ()
	}

	return cmd.Run()
}
