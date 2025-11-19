package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
)

var (
	primeFullMode bool
	primeMCPMode  bool
)

var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Output AI-optimized workflow context",
	Long: `Output essential Beads workflow context in AI-optimized markdown format.

Automatically detects if MCP server is active and adapts output:
- MCP mode: Brief workflow reminders (~50 tokens)
- CLI mode: Full command reference (~1-2k tokens)

Designed for Claude Code hooks (SessionStart, PreCompact) to prevent
agents from forgetting bd workflow after context compaction.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Find .beads/ directory (supports both database and JSONL-only mode)
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			// Not in a beads project - silent exit with success
			// CRITICAL: No stderr output, exit 0
			// This enables cross-platform hook integration
			os.Exit(0)
		}

		// Detect MCP mode (unless overridden by flags)
		mcpMode := isMCPActive()
		if primeFullMode {
			mcpMode = false
		}
		if primeMCPMode {
			mcpMode = true
		}

		// Output workflow context (adaptive based on MCP)
		if err := outputPrimeContext(mcpMode); err != nil {
			// Suppress all errors - silent exit with success
			// Never write to stderr (breaks Windows compatibility)
			os.Exit(0)
		}
	},
}

func init() {
	primeCmd.Flags().BoolVar(&primeFullMode, "full", false, "Force full CLI output (ignore MCP detection)")
	primeCmd.Flags().BoolVar(&primeMCPMode, "mcp", false, "Force MCP mode (minimal output)")
	rootCmd.AddCommand(primeCmd)
}

// isMCPActive detects if MCP server is currently active
func isMCPActive() bool {
	// Get home directory with fallback
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to HOME environment variable
		home = os.Getenv("HOME")
		if home == "" {
			// Can't determine home directory, assume no MCP
			return false
		}
	}

	settingsPath := filepath.Join(home, ".claude/settings.json")
	// #nosec G304 -- settings path derived from user home directory
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	// Check mcpServers section for beads
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for beads server (any key containing "beads")
	for key := range mcpServers {
		if strings.Contains(strings.ToLower(key), "beads") {
			return true
		}
	}

	return false
}

// outputPrimeContext outputs workflow context in markdown format
func outputPrimeContext(mcpMode bool) error {
	if mcpMode {
		return outputMCPContext()
	}
	return outputCLIContext()
}

// outputMCPContext outputs minimal context for MCP users
func outputMCPContext() error {
	context := `# Beads Issue Tracker Active

## Core Rules
- Track ALL work in beads (no TodoWrite tool, no markdown TODOs)
- Use bd MCP tools (mcp__plugin_beads_beads__*), not TodoWrite or markdown

Start: Check ` + "`ready`" + ` tool for available work.
`
	fmt.Print(context)
	return nil
}

// outputCLIContext outputs full CLI reference for non-MCP users
func outputCLIContext() error {
	context := `# Beads Workflow Context

> **Context Recovery**: Run ` + "`bd prime`" + ` after compaction, clear, or new session
> Hooks auto-call this in Claude Code when .beads/ detected

## Core Rules
- Track ALL work in beads (no TodoWrite tool, no markdown TODOs)
- Use ` + "`bd create`" + ` to create issues, not TodoWrite tool
- Git workflow: hooks auto-sync, run ` + "`bd sync`" + ` at session end
- Session management: check ` + "`bd ready`" + ` for available work

## Essential Commands

### Finding Work
- ` + "`bd ready`" + ` - Show issues ready to work (no blockers)
- ` + "`bd list --status=open`" + ` - All open issues
- ` + "`bd list --status=in_progress`" + ` - Your active work
- ` + "`bd show <id>`" + ` - Detailed issue view with dependencies

### Creating & Updating
- ` + "`bd create --title=\"...\" --type=task|bug|feature`" + ` - New issue
- ` + "`bd update <id> --status=in_progress`" + ` - Claim work
- ` + "`bd update <id> --assignee=username`" + ` - Assign to someone
- ` + "`bd close <id>`" + ` - Mark complete
- ` + "`bd close <id> --reason=\"explanation\"`" + ` - Close with reason

### Dependencies & Blocking
- ` + "`bd dep <from> <to>`" + ` - Add blocker dependency (from blocks to)
- ` + "`bd blocked`" + ` - Show all blocked issues
- ` + "`bd show <id>`" + ` - See what's blocking/blocked by this issue

### Sync & Collaboration
- ` + "`bd sync`" + ` - Sync with git remote (run at session end)
- ` + "`bd sync --status`" + ` - Check sync status without syncing

### Project Health
- ` + "`bd stats`" + ` - Project statistics (open/closed/blocked counts)
- ` + "`bd doctor`" + ` - Check for issues (sync problems, missing hooks)

## Common Workflows

**Starting work:**
` + "```bash" + `
bd ready           # Find available work
bd show <id>       # Review issue details
bd update <id> --status=in_progress  # Claim it
` + "```" + `

**Completing work:**
` + "```bash" + `
bd close <id>      # Mark done
bd sync            # Push to remote
` + "```" + `

**Creating dependent work:**
` + "```bash" + `
bd create --title="Implement feature X" --type=feature
bd create --title="Write tests for X" --type=task
bd dep beads-xxx beads-yyy  # Feature blocks tests
` + "```" + `
`
	fmt.Print(context)
	return nil
}
