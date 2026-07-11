package setup

import (
	"encoding/json"
	"fmt"
	"os"
)

const junieGuidelinesTemplate = `# Beads Issue Tracking Instructions

This project uses **Beads (bd)** for issue tracking. Use the bd CLI or MCP tools for all task management.

## Core Workflow Rules

1. **Track ALL work in bd** - Never use markdown TODOs or comment-based task lists
2. **Check ready work first** - Run ` + "`bd ready`" + ` to find unblocked issues
3. **Always include descriptions** - Provide meaningful context when creating issues
4. **Link discovered work** - Use ` + "`discovered-from`" + ` dependencies for issues found during work
5. **Sync at session end** - Run ` + "`bd dolt push`" + ` before ending your session

## Quick Command Reference

### Finding Work
` + "```bash" + `
bd ready              # Show unblocked issues ready for work
bd list --status open # List all open issues
bd show <id>          # View issue details
bd blocked            # Show blocked issues and their blockers
` + "```" + `

### Creating Issues
` + "```bash" + `
bd create "Title" --description="Details" -t bug|feature|task -p 0-4 --json
bd create "Found bug" --description="Details" --deps discovered-from:bd-42 --json
` + "```" + `

### Working on Issues
` + "```bash" + `
bd update <id> --claim               # Claim work atomically
bd update <id> --priority 1          # Change priority
bd close <id> --reason "Completed"   # Mark complete
bd unclaim <id>                    # Release stuck issue (agent crashed)
` + "```" + `

### Dependencies
` + "```bash" + `
bd dep add <issue> <depends-on>      # Add dependency (issue depends on depends-on)
bd dep add <issue> <depends-on> --type=related  # Soft link
` + "```" + `

### Syncing
` + "```bash" + `
bd dolt push  # ALWAYS run at session end - commits and pushes changes
` + "```" + `

## Issue Types

- ` + "`bug`" + ` - Something broken that needs fixing
- ` + "`feature`" + ` - New functionality
- ` + "`task`" + ` - Work item (tests, docs, refactoring)
- ` + "`epic`" + ` - Large feature composed of multiple issues
- ` + "`chore`" + ` - Maintenance work (dependencies, tooling)

## Priorities

- ` + "`0`" + ` - Critical (security, data loss, broken builds)
- ` + "`1`" + ` - High (major features, important bugs)
- ` + "`2`" + ` - Medium (default, nice-to-have)
- ` + "`3`" + ` - Low (polish, optimization)
- ` + "`4`" + ` - Backlog (future ideas)

## MCP Tools Available

If the MCP server is configured, you can use these tools directly:
- ` + "`mcp_beads_ready`" + ` - Find ready tasks
- ` + "`mcp_beads_list`" + ` - List issues with filters
- ` + "`mcp_beads_show`" + ` - Show issue details
- ` + "`mcp_beads_create`" + ` - Create new issues
- ` + "`mcp_beads_update`" + ` - Update issue status/priority
- ` + "`mcp_beads_close`" + ` - Close completed issues
- ` + "`mcp_beads_dep`" + ` - Manage dependencies
- ` + "`mcp_beads_blocked`" + ` - Show blocked issues
- ` + "`mcp_beads_stats`" + ` - Get issue statistics

## Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use ` + "`--json`" + ` flag for programmatic use
- ✅ Link discovered work with ` + "`discovered-from`" + ` dependencies
- ✅ Check ` + "`bd ready`" + ` before asking "what should I work on?"
- ✅ Run ` + "`bd dolt push`" + ` at end of session
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, run ` + "`bd --help`" + ` or see the project's AGENTS.md file.
`

// junieMCPConfig generates the MCP configuration for Junie
func junieMCPConfig() map[string]interface{} {
	return map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"beads": map[string]interface{}{
				"command": "bd",
				"args":    []string{"mcp"},
			},
		},
	}
}

func InstallJunie() error {
	guidelinesPath := ".junie/guidelines.md"
	mcpPath := ".junie/mcp/mcp.json"

	fmt.Println("Installing Junie integration...")

	if err := EnsureDir(".junie", 0755); err != nil {
		return HandleError("%v", err)
	}

	if err := EnsureDir(".junie/mcp", 0755); err != nil {
		return HandleError("%v", err)
	}

	if err := atomicWriteFile(guidelinesPath, []byte(junieGuidelinesTemplate)); err != nil {
		return HandleError("write guidelines: %v", err)
	}

	mcpConfig := junieMCPConfig()
	mcpData, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return HandleError("marshal MCP config: %v", err)
	}

	if err := atomicWriteFile(mcpPath, mcpData); err != nil {
		return HandleError("write MCP config: %v", err)
	}

	fmt.Printf("\n✓ Junie integration installed\n")
	fmt.Printf("  Guidelines: %s (agent instructions)\n", guidelinesPath)
	fmt.Printf("  MCP Config: %s (MCP server configuration)\n", mcpPath)
	fmt.Println("\nJunie will automatically read these files on session start.")
	fmt.Println("The MCP server provides direct access to beads tools.")
	return nil
}

func CheckJunie() error {
	guidelinesPath := ".junie/guidelines.md"
	mcpPath := ".junie/mcp/mcp.json"

	guidelinesExists := false
	mcpExists := false

	if _, err := os.Stat(guidelinesPath); err == nil {
		guidelinesExists = true
	}
	if _, err := os.Stat(mcpPath); err == nil {
		mcpExists = true
	}

	if guidelinesExists && mcpExists {
		fmt.Println("✓ Junie integration installed")
		fmt.Printf("  Guidelines: %s\n", guidelinesPath)
		fmt.Printf("  MCP Config: %s\n", mcpPath)
		return nil
	}

	if guidelinesExists {
		fmt.Println("⚠ Partial Junie integration (guidelines only)")
		fmt.Printf("  Guidelines: %s\n", guidelinesPath)
		fmt.Println("  Missing: MCP config")
		return HandleErrorWithHint("partial Junie integration", "Run: bd setup junie (to complete installation)")
	}

	if mcpExists {
		fmt.Println("⚠ Partial Junie integration (MCP only)")
		fmt.Printf("  MCP Config: %s\n", mcpPath)
		fmt.Println("  Missing: Guidelines")
		return HandleErrorWithHint("partial Junie integration", "Run: bd setup junie (to complete installation)")
	}

	return HandleErrorWithHint("Junie integration not installed", "Run: bd setup junie")
}

func RemoveJunie() error {
	guidelinesPath := ".junie/guidelines.md"
	mcpPath := ".junie/mcp/mcp.json"
	mcpDir := ".junie/mcp"
	junieDir := ".junie"

	fmt.Println("Removing Junie integration...")

	removed := false

	if err := os.Remove(guidelinesPath); err != nil {
		if !os.IsNotExist(err) {
			return HandleError("failed to remove guidelines: %v", err)
		}
	} else {
		removed = true
	}

	if err := os.Remove(mcpPath); err != nil {
		if !os.IsNotExist(err) {
			return HandleError("failed to remove MCP config: %v", err)
		}
	} else {
		removed = true
	}

	_ = os.Remove(mcpDir)
	_ = os.Remove(junieDir)

	if !removed {
		fmt.Println("No Junie integration files found")
		return nil
	}

	fmt.Println("✓ Removed Junie integration")
	return nil
}
