package main

import (
	"fmt"
	"io"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const copilotInstructionsContent = `# GitHub Copilot Instructions

## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run ` + "`bd prime`" + ` for workflow context, or install hooks (` + "`bd hooks install`" + `) for auto-injection.

**Quick reference:**
- ` + "`bd ready`" + ` - Find unblocked work
- ` + "`bd create \"Title\" --type task --priority 2`" + ` - Create issue
- ` + "`bd close <id>`" + ` - Complete work
- ` + "`bd sync`" + ` - Sync with git (run at session end)

For full workflow details: ` + "`bd prime`" + ``

const agentsContent = `## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run ` + "`bd prime`" + ` for workflow context, or install hooks (` + "`bd hooks install`" + `) for auto-injection.

**Quick reference:**
- ` + "`bd ready`" + ` - Find unblocked work
- ` + "`bd create \"Title\" --type task --priority 2`" + ` - Create issue
- ` + "`bd close <id>`" + ` - Complete work
- ` + "`bd sync`" + ` - Sync with git (run at session end)

For full workflow details: ` + "`bd prime`" + ``

func renderOnboardInstructions(w io.Writer) error {
	bold := color.New(color.Bold).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()

	writef := func(format string, args ...interface{}) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	writeln := func(text string) error {
		_, err := fmt.Fprintln(w, text)
		return err
	}
	writeBlank := func() error {
		_, err := fmt.Fprintln(w)
		return err
	}

	if err := writef("\n%s\n\n", bold("bd Onboarding")); err != nil {
		return err
	}
	if err := writeln("Add this minimal snippet to AGENTS.md (or create it):"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", cyan("--- BEGIN AGENTS.MD CONTENT ---")); err != nil {
		return err
	}
	if err := writeln(agentsContent); err != nil {
		return err
	}
	if err := writef("%s\n\n", cyan("--- END AGENTS.MD CONTENT ---")); err != nil {
		return err
	}

	if err := writef("%s\n", bold("For GitHub Copilot users:")); err != nil {
		return err
	}
	if err := writeln("Add the same content to .github/copilot-instructions.md"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", bold("How it works:")); err != nil {
		return err
	}
	if err := writef("   • %s provides dynamic workflow context (~80 lines)\n", cyan("bd prime")); err != nil {
		return err
	}
	if err := writef("   • %s auto-injects bd prime at session start\n", cyan("bd hooks install")); err != nil {
		return err
	}
	if err := writeln("   • AGENTS.md only needs this minimal pointer, not full instructions"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n\n", green("This keeps AGENTS.md lean while bd prime provides up-to-date workflow details.")); err != nil {
		return err
	}

	return nil
}

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Display minimal snippet for AGENTS.md",
	Long: `Display a minimal snippet to add to AGENTS.md for bd integration.

This outputs a small (~10 line) snippet that points to 'bd prime' for full
workflow context. This approach:

  • Keeps AGENTS.md lean (doesn't bloat with instructions)
  • bd prime provides dynamic, always-current workflow details
  • Hooks auto-inject bd prime at session start

The old approach of embedding full instructions in AGENTS.md is deprecated
because it wasted tokens and got stale when bd upgraded.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := renderOnboardInstructions(cmd.OutOrStdout()); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(onboardCmd)
}
