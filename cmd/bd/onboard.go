package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
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

	if err := writef("\n%s\n\n", ui.RenderBold("bd Onboarding")); err != nil {
		return err
	}
	if err := writeln("Add this minimal snippet to AGENTS.md (or create it):"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", ui.RenderAccent("--- BEGIN AGENTS.MD CONTENT ---")); err != nil {
		return err
	}
	if err := writeln(agentsContent); err != nil {
		return err
	}
	if err := writef("%s\n\n", ui.RenderAccent("--- END AGENTS.MD CONTENT ---")); err != nil {
		return err
	}

	if err := writef("%s\n", ui.RenderBold("For GitHub Copilot users:")); err != nil {
		return err
	}
	if err := writeln("Add the same content to .github/copilot-instructions.md"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", ui.RenderBold("How it works:")); err != nil {
		return err
	}
	if err := writef("   • %s provides dynamic workflow context (~80 lines)\n", ui.RenderAccent("bd prime")); err != nil {
		return err
	}
	if err := writef("   • %s auto-injects bd prime at session start\n", ui.RenderAccent("bd hooks install")); err != nil {
		return err
	}
	if err := writeln("   • AGENTS.md only needs this minimal pointer, not full instructions"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n\n", ui.RenderPass("This keeps AGENTS.md lean while bd prime provides up-to-date workflow details.")); err != nil {
		return err
	}

	return nil
}

var onboardCmd = &cobra.Command{
	Use:     "onboard",
	GroupID: "setup",
	Short:   "Display minimal snippet for AGENTS.md",
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
