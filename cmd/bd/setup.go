package main

import (
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/setup"
)

var (
	setupProject bool
	setupCheck   bool
	setupRemove  bool
	setupStealth bool
)

var setupCmd = &cobra.Command{
	Use:     "setup",
	GroupID: "setup",
	Short:   "Setup integration with AI editors",
	Long:  `Setup integration files for AI editors like Claude Code, Cursor, Aider, and Factory.ai Droid.`,
}

var setupCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Setup Cursor IDE integration",
	Long: `Install Beads workflow rules for Cursor IDE.

Creates .cursor/rules/beads.mdc with bd workflow context.
Uses BEGIN/END markers for safe idempotent updates.`,
	Run: func(cmd *cobra.Command, args []string) {
		if setupCheck {
			setup.CheckCursor()
			return
		}

		if setupRemove {
			setup.RemoveCursor()
			return
		}

		setup.InstallCursor()
	},
}

var setupAiderCmd = &cobra.Command{
	Use:   "aider",
	Short: "Setup Aider integration",
	Long: `Install Beads workflow configuration for Aider.

Creates .aider.conf.yml with bd workflow instructions.
The AI will suggest bd commands for you to run via /run.

Note: Aider requires explicit command execution - the AI cannot
run commands autonomously. It will suggest bd commands which you
must confirm using Aider's /run command.`,
	Run: func(cmd *cobra.Command, args []string) {
		if setupCheck {
			setup.CheckAider()
			return
		}

		if setupRemove {
			setup.RemoveAider()
			return
		}

		setup.InstallAider()
	},
}

var setupFactoryCmd = &cobra.Command{
	Use:   "factory",
	Short: "Setup Factory.ai (Droid) integration",
	Long: `Install Beads workflow configuration for Factory.ai Droid.

Creates or updates AGENTS.md with bd workflow instructions.
Factory Droids automatically read AGENTS.md on session start.

AGENTS.md is the standard format used across AI coding assistants
(Factory, Cursor, Aider, Gemini CLI, Jules, and more).`,
	Run: func(cmd *cobra.Command, args []string) {
		if setupCheck {
			setup.CheckFactory()
			return
		}

		if setupRemove {
			setup.RemoveFactory()
			return
		}

		setup.InstallFactory()
	},
}

var setupClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Setup Claude Code integration",
	Long: `Install Claude Code hooks that auto-inject bd workflow context.

By default, installs hooks globally (~/.claude/settings.json).
Use --project flag to install only for this project.

Hooks call 'bd prime' on SessionStart and PreCompact events to prevent
agents from forgetting bd workflow after context compaction.`,
	Run: func(cmd *cobra.Command, args []string) {
		if setupCheck {
			setup.CheckClaude()
			return
		}

		if setupRemove {
			setup.RemoveClaude(setupProject)
			return
		}

		setup.InstallClaude(setupProject, setupStealth)
	},
}

func init() {
	setupFactoryCmd.Flags().BoolVar(&setupCheck, "check", false, "Check if Factory.ai integration is installed")
	setupFactoryCmd.Flags().BoolVar(&setupRemove, "remove", false, "Remove bd section from AGENTS.md")

	setupClaudeCmd.Flags().BoolVar(&setupProject, "project", false, "Install for this project only (not globally)")
	setupClaudeCmd.Flags().BoolVar(&setupCheck, "check", false, "Check if Claude integration is installed")
	setupClaudeCmd.Flags().BoolVar(&setupRemove, "remove", false, "Remove bd hooks from Claude settings")
	setupClaudeCmd.Flags().BoolVar(&setupStealth, "stealth", false, "Use 'bd prime --stealth' (flush only, no git operations)")

	setupCursorCmd.Flags().BoolVar(&setupCheck, "check", false, "Check if Cursor integration is installed")
	setupCursorCmd.Flags().BoolVar(&setupRemove, "remove", false, "Remove bd rules from Cursor")

	setupAiderCmd.Flags().BoolVar(&setupCheck, "check", false, "Check if Aider integration is installed")
	setupAiderCmd.Flags().BoolVar(&setupRemove, "remove", false, "Remove bd config from Aider")

	setupCmd.AddCommand(setupFactoryCmd)
	setupCmd.AddCommand(setupClaudeCmd)
	setupCmd.AddCommand(setupCursorCmd)
	setupCmd.AddCommand(setupAiderCmd)
	rootCmd.AddCommand(setupCmd)
}
