package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

var quickstartCmd = &cobra.Command{
	Use:     "quickstart",
	GroupID: "setup",
	Short:   "Quick start guide for bd",
	Long:  `Display a quick start guide showing common bd workflows and patterns.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("\n%s\n\n", ui.RenderBold("bd - Dependency-Aware Issue Tracker"))
		fmt.Printf("Issues chained together like beads.\n\n")

		fmt.Printf("%s\n", ui.RenderBold("GETTING STARTED"))
		fmt.Printf("  %s   Initialize bd in your project\n", ui.RenderAccent("bd init"))
		fmt.Printf("            Creates .beads/ directory with project-specific database\n")
		fmt.Printf("            Auto-detects prefix from directory name (e.g., myapp-1, myapp-2)\n\n")

		fmt.Printf("  %s   Initialize with custom prefix\n", ui.RenderAccent("bd init --prefix api"))
		fmt.Printf("            Issues will be named: api-<hash> (e.g., api-a3f2dd)\n\n")

		fmt.Printf("%s\n", ui.RenderBold("CREATING ISSUES"))
		fmt.Printf("  %s\n", ui.RenderAccent("bd create \"Fix login bug\""))
		fmt.Printf("  %s\n", ui.RenderAccent("bd create \"Add auth\" -p 0 -t feature"))
		fmt.Printf("  %s\n\n", ui.RenderAccent("bd create \"Write tests\" -d \"Unit tests for auth\" --assignee alice"))

		fmt.Printf("%s\n", ui.RenderBold("VIEWING ISSUES"))
		fmt.Printf("  %s       List all issues\n", ui.RenderAccent("bd list"))
		fmt.Printf("  %s  List by status\n", ui.RenderAccent("bd list --status open"))
		fmt.Printf("  %s  List by priority (0-4, 0=highest)\n", ui.RenderAccent("bd list --priority 0"))
		fmt.Printf("  %s       Show issue details\n\n", ui.RenderAccent("bd show bd-1"))

		fmt.Printf("%s\n", ui.RenderBold("MANAGING DEPENDENCIES"))
		fmt.Printf("  %s     Add dependency (bd-2 blocks bd-1)\n", ui.RenderAccent("bd dep add bd-1 bd-2"))
		fmt.Printf("  %s  Visualize dependency tree\n", ui.RenderAccent("bd dep tree bd-1"))
		fmt.Printf("  %s      Detect circular dependencies\n\n", ui.RenderAccent("bd dep cycles"))

		fmt.Printf("%s\n", ui.RenderBold("DEPENDENCY TYPES"))
		fmt.Printf("  %s  Task B must complete before task A\n", ui.RenderWarn("blocks"))
		fmt.Printf("  %s  Soft connection, doesn't block progress\n", ui.RenderWarn("related"))
		fmt.Printf("  %s  Epic/subtask hierarchical relationship\n", ui.RenderWarn("parent-child"))
		fmt.Printf("  %s  Auto-created when AI discovers related work\n\n", ui.RenderWarn("discovered-from"))

		fmt.Printf("%s\n", ui.RenderBold("READY WORK"))
		fmt.Printf("  %s       Show issues ready to work on\n", ui.RenderAccent("bd ready"))
		fmt.Printf("            Ready = status is 'open' AND no blocking dependencies\n")
		fmt.Printf("            Perfect for agents to claim next work!\n\n")

		fmt.Printf("%s\n", ui.RenderBold("UPDATING ISSUES"))
		fmt.Printf("  %s\n", ui.RenderAccent("bd update bd-1 --status in_progress"))
		fmt.Printf("  %s\n", ui.RenderAccent("bd update bd-1 --priority 0"))
		fmt.Printf("  %s\n\n", ui.RenderAccent("bd update bd-1 --assignee bob"))

		fmt.Printf("%s\n", ui.RenderBold("CLOSING ISSUES"))
		fmt.Printf("  %s\n", ui.RenderAccent("bd close bd-1"))
		fmt.Printf("  %s\n\n", ui.RenderAccent("bd close bd-2 bd-3 --reason \"Fixed in PR #42\""))

		fmt.Printf("%s\n", ui.RenderBold("DATABASE LOCATION"))
		fmt.Printf("  bd automatically discovers your database:\n")
		fmt.Printf("    1. %s flag\n", ui.RenderAccent("--db /path/to/db.db"))
		fmt.Printf("    2. %s environment variable\n", ui.RenderAccent("$BEADS_DB"))
		fmt.Printf("    3. %s in current directory or ancestors\n", ui.RenderAccent(".beads/*.db"))
		fmt.Printf("    4. %s as fallback\n\n", ui.RenderAccent("~/.beads/default.db"))

		fmt.Printf("%s\n", ui.RenderBold("AGENT INTEGRATION"))
		fmt.Printf("  bd is designed for AI-supervised workflows:\n")
		fmt.Printf("    • Agents create issues when discovering new work\n")
		fmt.Printf("    • %s shows unblocked work ready to claim\n", ui.RenderAccent("bd ready"))
		fmt.Printf("    • Use %s flags for programmatic parsing\n", ui.RenderAccent("--json"))
		fmt.Printf("    • Dependencies prevent agents from duplicating effort\n\n")

		fmt.Printf("%s\n", ui.RenderBold("DATABASE EXTENSION"))
		fmt.Printf("  Applications can extend bd's SQLite database:\n")
		fmt.Printf("    • Add your own tables (e.g., %s)\n", ui.RenderAccent("myapp_executions"))
		fmt.Printf("    • Join with %s table for powerful queries\n", ui.RenderAccent("issues"))
		fmt.Printf("    • See database extension docs for integration patterns:\n")
		fmt.Printf("      %s\n\n", ui.RenderAccent("https://github.com/steveyegge/beads/blob/main/EXTENDING.md"))

		fmt.Printf("%s\n", ui.RenderBold("GIT WORKFLOW (AUTO-SYNC)"))
		fmt.Printf("  bd automatically keeps git in sync:\n")
		fmt.Printf("    • %s Export to JSONL after CRUD operations (5s debounce)\n", ui.RenderPass("✓"))
		fmt.Printf("    • %s Import from JSONL when newer than DB (after %s)\n", ui.RenderPass("✓"), ui.RenderAccent("git pull"))
		fmt.Printf("    • %s Works seamlessly across machines and team members\n", ui.RenderPass("✓"))
		fmt.Printf("    • No manual export/import needed!\n")
		fmt.Printf("  Disable with: %s or %s\n\n", ui.RenderAccent("--no-auto-flush"), ui.RenderAccent("--no-auto-import"))

		fmt.Printf("%s\n", ui.RenderPass("Ready to start!"))
		fmt.Printf("Run %s to create your first issue.\n\n", ui.RenderAccent("bd create \"My first issue\""))
	},
}

func init() {
	rootCmd.AddCommand(quickstartCmd)
}
