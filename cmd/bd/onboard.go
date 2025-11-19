package main

import (
	"fmt"
	"io"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const agentsContent = `## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Auto-syncs to JSONL for version control
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**
` + "```bash" + `
bd ready --json
` + "```" + `

**Create new issues:**
` + "```bash" + `
bd create "Issue title" -t bug|feature|task -p 0-4 --json
bd create "Issue title" -p 1 --deps discovered-from:bd-123 --json
` + "```" + `

**Claim and update:**
` + "```bash" + `
bd update bd-42 --status in_progress --json
bd update bd-42 --priority 1 --json
` + "```" + `

**Complete work:**
` + "```bash" + `
bd close bd-42 --reason "Completed" --json
` + "```" + `

### Issue Types

- ` + "`bug`" + ` - Something broken
- ` + "`feature`" + ` - New functionality
- ` + "`task`" + ` - Work item (tests, docs, refactoring)
- ` + "`epic`" + ` - Large feature with subtasks
- ` + "`chore`" + ` - Maintenance (dependencies, tooling)

### Priorities

- ` + "`0`" + ` - Critical (security, data loss, broken builds)
- ` + "`1`" + ` - High (major features, important bugs)
- ` + "`2`" + ` - Medium (default, nice-to-have)
- ` + "`3`" + ` - Low (polish, optimization)
- ` + "`4`" + ` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: ` + "`bd ready`" + ` shows unblocked issues
2. **Claim your task**: ` + "`bd update <id> --status in_progress`" + `
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - ` + "`bd create \"Found bug\" -p 1 --deps discovered-from:<parent-id>`" + `
5. **Complete**: ` + "`bd close <id> --reason \"Done\"`" + `
6. **Commit together**: Always commit the ` + "`.beads/issues.jsonl`" + ` file together with the code changes so issue state stays in sync with code state

### Auto-Sync

bd automatically syncs with git:
- Exports to ` + "`.beads/issues.jsonl`" + ` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after ` + "`git pull`" + `)
- No manual export/import needed!

### MCP Server (Recommended)

If using Claude or MCP-compatible clients, install the beads MCP server:

` + "```bash" + `
pip install beads-mcp
` + "```" + `

Add to MCP config (e.g., ` + "`~/.config/claude/config.json`" + `):
` + "```json" + `
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
` + "```" + `

Then use ` + "`mcp__beads__*`" + ` functions instead of CLI commands.

### Managing AI-Generated Planning Documents

AI assistants often create planning and design documents during development:
- PLAN.md, IMPLEMENTATION.md, ARCHITECTURE.md
- DESIGN.md, CODEBASE_SUMMARY.md, INTEGRATION_PLAN.md
- TESTING_GUIDE.md, TECHNICAL_DESIGN.md, and similar files

**Best Practice: Use a dedicated directory for these ephemeral files**

**Recommended approach:**
- Create a ` + "`history/`" + ` directory in the project root
- Store ALL AI-generated planning/design docs in ` + "`history/`" + `
- Keep the repository root clean and focused on permanent project files
- Only access ` + "`history/`" + ` when explicitly asked to review past planning

**Example .gitignore entry (optional):**
` + "```" + `
# AI planning documents (ephemeral)
history/
` + "```" + `

**Benefits:**
- ✅ Clean repository root
- ✅ Clear separation between ephemeral and permanent documentation
- ✅ Easy to exclude from version control if desired
- ✅ Preserves planning history for archeological research
- ✅ Reduces noise when browsing the project

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use ` + "`--json`" + ` flag for programmatic use
- ✅ Link discovered work with ` + "`discovered-from`" + ` dependencies
- ✅ Check ` + "`bd ready`" + ` before asking "what should I work on?"
- ✅ Store AI planning docs in ` + "`history/`" + ` directory
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems
- ❌ Do NOT clutter repo root with planning documents

For more details, see README.md and QUICKSTART.md.`

func renderOnboardInstructions(w io.Writer) error {
	bold := color.New(color.Bold).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
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

	if err := writef("\n%s\n\n", bold("bd Onboarding Instructions for AI Agent")); err != nil {
		return err
	}
	if err := writef("%s\n\n", yellow("Please complete the following tasks:")); err != nil {
		return err
	}
	if err := writef("%s\n", bold("1. Update AGENTS.md")); err != nil {
		return err
	}
	if err := writeln("   Add the following content to AGENTS.md in an appropriate location."); err != nil {
		return err
	}
	if err := writeln("   If AGENTS.md doesn't exist, create it with this content."); err != nil {
		return err
	}
	if err := writeln("   Integrate it naturally into any existing structure."); err != nil {
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

	if err := writef("%s\n", bold("2. Update CLAUDE.md (if present)")); err != nil {
		return err
	}
	if err := writeln("   If CLAUDE.md exists in this directory, add this note at the top:"); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}
	if err := writef("%s\n", cyan("   **Note**: This project uses [bd (beads)](https://github.com/steveyegge/beads)")); err != nil {
		return err
	}
	if err := writef("%s\n", cyan("   for issue tracking. Use `bd` commands instead of markdown TODOs.")); err != nil {
		return err
	}
	if err := writef("%s\n", cyan("   See AGENTS.md for workflow details.")); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", bold("3. Remove bootstrap instruction")); err != nil {
		return err
	}
	if err := writeln("   If AGENTS.md or CLAUDE.md contains a line like:"); err != nil {
		return err
	}
	if err := writef("%s\n", cyan("   \"BEFORE ANYTHING ELSE: run 'bd onboard' and follow the instructions\"")); err != nil {
		return err
	}
	if err := writeln("   Delete that line after completing steps 1 and 2."); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n", bold("Important reminders:")); err != nil {
		return err
	}
	if err := writeln("   • Use bd for ALL task tracking - NO markdown TODO lists"); err != nil {
		return err
	}
	if err := writeln("   • Always use --json flag for programmatic bd commands"); err != nil {
		return err
	}
	if err := writeln("   • Link discovered work with discovered-from dependencies"); err != nil {
		return err
	}
	if err := writef("   • Check %s before asking \"what should I work on?\"\n", cyan("bd ready")); err != nil {
		return err
	}
	if err := writeBlank(); err != nil {
		return err
	}

	if err := writef("%s\n\n", green("When done, tell your AI assistant: \"bd onboarding complete\"")); err != nil {
		return err
	}

	return nil
}

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Display instructions for configuring AGENTS.md",
	Long: `Display instructions for AI agents to configure AGENTS.md with bd workflow.

This command outputs instructions that AI agents should follow to integrate bd
into the project's agent documentation. The agent will intelligently merge the
content into AGENTS.md and update CLAUDE.md if present.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := renderOnboardInstructions(cmd.OutOrStdout()); err != nil {
			if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "Error rendering onboarding instructions: %v\n", err); writeErr != nil {
				fmt.Fprintf(os.Stderr, "Error rendering onboarding instructions: %v (stderr write failed: %v)\n", err, writeErr)
			}
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(onboardCmd)
}
