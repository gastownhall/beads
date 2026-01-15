package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	internalbeads "github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// isDaemonAutoSyncing checks if daemon is running with auto-commit and auto-push enabled.
// Returns false if daemon is not running or check fails (fail-safe to show full protocol).
// This is a variable to allow stubbing in tests.
var isDaemonAutoSyncing = func() bool {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		return false
	}
	defer func() { _ = client.Close() }()

	status, err := client.Status()
	if err != nil {
		return false
	}

	// Only check auto-commit and auto-push (auto-pull is separate)
	return status.AutoCommit && status.AutoPush
}

var (
	primeFullMode    bool
	primeMCPMode     bool
	primeStealthMode bool
	primeExportMode  bool
)

var primeCmd = &cobra.Command{
	Use:     "prime",
	GroupID: "setup",
	Short:   "Output AI-optimized workflow context",
	Long: `Output essential Beads workflow context in AI-optimized markdown format.

Automatically detects if MCP server is active and adapts output:
- MCP mode: Brief workflow reminders (~50 tokens)
- CLI mode: Full command reference (~1-2k tokens)

Designed for Claude Code hooks (SessionStart, PreCompact) to prevent
agents from forgetting bd workflow after context compaction.

Config options:
- no-git-ops: When true, outputs stealth mode (no git commands in session close protocol).
  Set via: bd config set no-git-ops true
  Useful when you want to control when commits happen manually.

Workflow customization:
- Place a .beads/PRIME.md file to override the default output entirely.
- Use --export to dump the default content for customization.`,
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

		// Check for stealth mode: flag OR config (GH#593)
		// This allows users to disable git ops in session close protocol via config
		stealthMode := primeStealthMode || config.GetBool("no-git-ops")

		// Check for custom PRIME.md override (unless --export flag)
		// This allows users to fully customize workflow instructions
		// Check local .beads/ first (even if redirected), then redirected location
		if !primeExportMode {
			localPrimePath := filepath.Join(".beads", "PRIME.md")
			redirectedPrimePath := filepath.Join(beadsDir, "PRIME.md")

			// Try local first (user's clone-specific customization)
			// #nosec G304 -- path is relative to cwd
			if content, err := os.ReadFile(localPrimePath); err == nil {
				fmt.Print(string(content))
				return
			}
			// Fall back to redirected location (shared customization)
			// #nosec G304 -- path is constructed from beadsDir which we control
			if content, err := os.ReadFile(redirectedPrimePath); err == nil {
				fmt.Print(string(content))
				return
			}
		}

		// Output workflow context (adaptive based on MCP and stealth mode)
		if err := outputPrimeContext(os.Stdout, mcpMode, stealthMode); err != nil {
			// Suppress all errors - silent exit with success
			// Never write to stderr (breaks Windows compatibility)
			os.Exit(0)
		}
	},
}

func init() {
	primeCmd.Flags().BoolVar(&primeFullMode, "full", false, "Force full CLI output (ignore MCP detection)")
	primeCmd.Flags().BoolVar(&primeMCPMode, "mcp", false, "Force MCP mode (minimal output)")
	primeCmd.Flags().BoolVar(&primeStealthMode, "stealth", false, "Stealth mode (no git operations, flush only)")
	primeCmd.Flags().BoolVar(&primeExportMode, "export", false, "Output default content (ignores PRIME.md override)")
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

// isEphemeralBranch detects if current branch has no upstream (ephemeral/local-only)
var isEphemeralBranch = func() bool {
	// git rev-parse --abbrev-ref --symbolic-full-name @{u}
	// Returns error code 128 if no upstream configured
	rc, err := internalbeads.GetRepoContext()
	if err != nil {
		return true // Default to ephemeral if we can't determine context
	}
	cmd := rc.GitCmdCWD(context.Background(), "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return cmd.Run() != nil
}

// primeHasGitRemote detects if any git remote is configured (stubbable for tests)
var primeHasGitRemote = func() bool {
	return syncbranch.HasGitRemote(context.Background())
}

// getRedirectNotice returns a notice string if beads is redirected
func getRedirectNotice(verbose bool) string {
	redirectInfo := beads.GetRedirectInfo()
	if !redirectInfo.IsRedirected {
		return ""
	}

	if verbose {
		return fmt.Sprintf(`> ⚠️ **Redirected**: Local .beads → %s
> You share issues with other clones using this redirect.

`, redirectInfo.TargetDir)
	}
	return fmt.Sprintf("**Note**: Beads redirected to %s (shared with other clones)\n\n", redirectInfo.TargetDir)
}

// outputPrimeContext outputs workflow context in markdown format
func outputPrimeContext(w io.Writer, mcpMode bool, stealthMode bool) error {
	if mcpMode {
		return outputMCPContext(w, stealthMode)
	}
	return outputCLIContext(w, stealthMode)
}

// outputMCPContext outputs minimal context for MCP users
func outputMCPContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := config.GetBool("no-push")
	autoSync := isDaemonAutoSyncing()
	localOnly := !primeHasGitRemote()

	var closeProtocol string
	if stealthMode || localOnly {
		// Stealth mode or local-only: only flush to JSONL, no git operations
		closeProtocol = "Before saying \"done\": bd sync --flush-only"
	} else if autoSync && !ephemeral && !noPush {
		// Daemon is auto-syncing - no bd sync needed
		closeProtocol = "Before saying \"done\": git status → git add → git commit → git push (beads auto-synced by daemon)"
	} else if ephemeral {
		closeProtocol = "Before saying \"done\": git status → git add → bd sync --from-main → git commit (no push - ephemeral branch)"
	} else if noPush {
		closeProtocol = "Before saying \"done\": git status → git add → bd sync → git commit (push disabled - run git push manually)"
	} else {
		closeProtocol = "Before saying \"done\": git status → git add → bd sync → git commit → bd sync → git push"
	}

	redirectNotice := getRedirectNotice(false)

	context := `# Beads Issue Tracker Active

` + redirectNotice + `# 🚨 SESSION CLOSE PROTOCOL 🚨

` + closeProtocol + `

## Core Rules
- Track strategic work in beads (multi-session, dependencies, discovered work)
- TodoWrite is fine for simple single-session linear tasks
- When in doubt, prefer bd—persistence you don't need beats lost context

Start: Check ` + "`ready`" + ` tool for available work.
`
	_, _ = fmt.Fprint(w, context)
	return nil
}

// outputCLIContext outputs full CLI reference for non-MCP users
func outputCLIContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := config.GetBool("no-push")
	autoSync := isDaemonAutoSyncing()
	localOnly := !primeHasGitRemote()

	var closeProtocol string
	var closeNote string
	var syncSection string
	var completingWorkflow string
	var gitWorkflowRule string

	if stealthMode || localOnly {
		// Stealth mode or local-only: only flush to JSONL, no git operations
		closeProtocol = `[ ] bd sync --flush-only    (export beads to JSONL only)`
		syncSection = `### Sync & Collaboration
- ` + "`bd sync --flush-only`" + ` - Export to JSONL`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
bd sync --flush-only        # Export to JSONL
` + "```"
		// Only show local-only note if not in stealth mode (stealth is explicit user choice)
		if localOnly && !stealthMode {
			closeNote = "**Note:** No git remote configured. Issues are saved locally only."
			gitWorkflowRule = "Git workflow: local-only (no git remote)"
		} else {
			gitWorkflowRule = "Git workflow: stealth mode (no git ops)"
		}
	} else if autoSync && !ephemeral && !noPush {
		// Daemon is auto-syncing - simplified protocol (no bd sync needed)
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. git commit -m "..."     (commit code)
[ ] 4. git push                (push to remote)`
		closeNote = "**Note:** Daemon is auto-syncing beads changes. No manual `bd sync` needed."
		syncSection = `### Sync & Collaboration
- Daemon handles beads sync automatically (auto-commit + auto-push + auto-pull enabled)
- ` + "`bd sync --status`" + ` - Check sync status`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git push                    # Push to remote (beads auto-synced by daemon)
` + "```"
		gitWorkflowRule = "Git workflow: daemon auto-syncs beads changes"
	} else if ephemeral {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. bd sync --from-main     (pull beads updates from main)
[ ] 4. git commit -m "..."     (commit code changes)`
		closeNote = "**Note:** This is an ephemeral branch (no upstream). Code is merged to main locally, not pushed."
		syncSection = `### Sync & Collaboration
- ` + "`bd sync --from-main`" + ` - Pull beads updates from main (for ephemeral branches)
- ` + "`bd sync --status`" + ` - Check sync status without syncing`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
bd sync --from-main         # Pull latest beads from main
git add . && git commit -m "..."  # Commit your changes
# Merge to main when ready (local merge, not push)
` + "```"
		gitWorkflowRule = "Git workflow: run `bd sync --from-main` at session end"
	} else if noPush {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. bd sync                 (commit beads changes)
[ ] 4. git commit -m "..."     (commit code)
[ ] 5. bd sync                 (commit any new beads changes)`
		closeNote = "**Note:** Push disabled via config. Run `git push` manually when ready."
		syncSection = `### Sync & Collaboration
- ` + "`bd sync`" + ` - Sync with git remote (run at session end)
- ` + "`bd sync --status`" + ` - Check sync status without syncing`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
bd sync                     # Sync beads (push disabled)
# git push                  # Run manually when ready
` + "```"
		gitWorkflowRule = "Git workflow: run `bd sync` at session end (push disabled)"
	} else {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. bd sync                 (commit beads changes)
[ ] 4. git commit -m "..."     (commit code)
[ ] 5. bd sync                 (commit any new beads changes)
[ ] 6. git push                (push to remote)`
		closeNote = "**NEVER skip this.** Work is not done until pushed."
		syncSection = `### Sync & Collaboration
- ` + "`bd sync`" + ` - Sync with git remote (run at session end)
- ` + "`bd sync --status`" + ` - Check sync status without syncing`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
bd sync                     # Push to remote
` + "```"
		gitWorkflowRule = "Git workflow: hooks auto-sync, run `bd sync` at session end"
	}

	redirectNotice := getRedirectNotice(true)

	context := `# Beads Workflow Context

> **Context Recovery**: Run ` + "`bd prime`" + ` after compaction, clear, or new session
> Hooks auto-call this in Claude Code when .beads/ detected

` + redirectNotice + `# 🚨 SESSION CLOSE PROTOCOL 🚨

**CRITICAL**: Before saying "done" or "complete", you MUST run this checklist:

` + "```" + `
` + closeProtocol + `
` + "```" + `

` + closeNote + `

## Core Rules
- Track strategic work in beads (multi-session, dependencies, discovered work)
- Use ` + "`bd create`" + ` for issues, TodoWrite for simple single-session execution
- When in doubt, prefer bd—persistence you don't need beats lost context
- ` + gitWorkflowRule + `
- Session management: check ` + "`bd ready`" + ` for available work

## Essential Commands

### Finding Work
- ` + "`bd ready`" + ` - Show issues ready to work (no blockers)
- ` + "`bd list --status=open`" + ` - All open issues
- ` + "`bd list --status=in_progress`" + ` - Your active work
- ` + "`bd show <id>`" + ` - Detailed issue view with dependencies

### Creating & Updating
- ` + "`bd create --title=\"...\" --type=task|bug|feature --priority=2`" + ` - New issue
  - Priority: 0-4 or P0-P4 (0=critical, 2=medium, 4=backlog). NOT "high"/"medium"/"low"
- ` + "`bd update <id> --status=in_progress`" + ` - Claim work
- ` + "`bd update <id> --assignee=username`" + ` - Assign to someone
- ` + "`bd close <id>`" + ` - Mark complete
- ` + "`bd close <id1> <id2> ...`" + ` - Close multiple issues at once (more efficient)
- ` + "`bd close <id> --reason=\"explanation\"`" + ` - Close with reason
- **Tip**: When creating multiple issues/tasks/epics, use parallel subagents for efficiency

### Dependencies & Blocking
- ` + "`bd dep add <issue> <depends-on>`" + ` - Add dependency (issue depends on depends-on)
- ` + "`bd blocked`" + ` - Show all blocked issues
- ` + "`bd show <id>`" + ` - See what's blocking/blocked by this issue

` + syncSection + `

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

` + completingWorkflow + `

**Creating dependent work:**
` + "```bash" + `
# Run bd create commands in parallel (use subagents for many items)
bd create --title="Implement feature X" --type=feature
bd create --title="Write tests for X" --type=task
bd dep add beads-yyy beads-xxx  # Tests depend on Feature (Feature blocks tests)
` + "```" + `
`
	_, _ = fmt.Fprint(w, context)
	return nil
}
