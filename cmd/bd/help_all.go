package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// helpAllFlag is the --all flag for the help command
var helpAllFlag bool

// registerHelpAllFlag adds the --all flag to Cobra's auto-generated help command.
// Must be called after rootCmd.InitDefaultHelpCmd() has run (i.e., after first Execute
// or explicit init). We hook it in init() after all subcommands are registered.
func registerHelpAllFlag() {
	// Cobra lazily creates the help command. We need to find it.
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "help" {
			cmd.Flags().BoolVar(&helpAllFlag, "all", false, "Show help for all commands in a single document")

			// Wrap the existing Run to check --all first
			originalRun := cmd.Run
			cmd.Run = func(cmd *cobra.Command, args []string) {
				if helpAllFlag {
					writeAllHelp(os.Stdout, rootCmd)
					return
				}
				if originalRun != nil {
					originalRun(cmd, args)
				}
			}
			return
		}
	}
}

// writeAllHelp writes a complete markdown reference for all commands,
// generated from the live Cobra command tree.
func writeAllHelp(w io.Writer, root *cobra.Command) {
	fmt.Fprintf(w, "# bd — Complete Command Reference\n\n")
	fmt.Fprintf(w, "Generated from `bd help --all` (bd version %s)\n\n", Version)

	// Collect commands grouped by their GroupID
	type group struct {
		title    string
		commands []*cobra.Command
	}

	// Build ordered group list from root's groups
	groups := root.Groups()
	groupMap := make(map[string]*group, len(groups))
	var orderedGroups []*group
	for _, g := range groups {
		grp := &group{title: g.Title}
		groupMap[g.ID] = grp
		orderedGroups = append(orderedGroups, grp)
	}

	// Ungrouped commands (if any)
	var ungrouped []*group

	for _, cmd := range root.Commands() {
		if !cmd.IsAvailableCommand() && cmd.Name() != "help" {
			continue
		}
		if gid := cmd.GroupID; gid != "" {
			if grp, ok := groupMap[gid]; ok {
				grp.commands = append(grp.commands, cmd)
			}
		} else {
			// Ungrouped
			if len(ungrouped) == 0 {
				ungrouped = append(ungrouped, &group{title: "Other Commands:"})
			}
			ungrouped[0].commands = append(ungrouped[0].commands, cmd)
		}
	}

	// Table of contents
	fmt.Fprintf(w, "## Table of Contents\n\n")
	allGroups := append(orderedGroups, ungrouped...)
	for _, grp := range allGroups {
		if len(grp.commands) == 0 {
			continue
		}
		fmt.Fprintf(w, "### %s\n\n", grp.title)
		for _, cmd := range grp.commands {
			anchor := "bd-" + strings.ReplaceAll(cmd.Name(), "-", "-")
			fmt.Fprintf(w, "- [bd %s](#%s) — %s\n", cmd.Name(), anchor, cmd.Short)
			// Include subcommands in TOC
			for _, sub := range cmd.Commands() {
				if !sub.IsAvailableCommand() {
					continue
				}
				subAnchor := "bd-" + cmd.Name() + "-" + strings.ReplaceAll(sub.Name(), "-", "-")
				fmt.Fprintf(w, "  - [bd %s %s](#%s) — %s\n", cmd.Name(), sub.Name(), subAnchor, sub.Short)
			}
		}
		fmt.Fprintf(w, "\n")
	}

	// Global flags (once)
	fmt.Fprintf(w, "---\n\n## Global Flags\n\n")
	fmt.Fprintf(w, "These flags apply to all commands:\n\n")
	fmt.Fprintf(w, "```\n")
	fmt.Fprintf(w, "%s", root.PersistentFlags().FlagUsages())
	fmt.Fprintf(w, "```\n\n")

	// Command details
	fmt.Fprintf(w, "---\n\n")
	for _, grp := range allGroups {
		if len(grp.commands) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n", grp.title)
		for _, cmd := range grp.commands {
			writeCommandHelp(w, cmd, "bd", 3)
		}
	}
}

// writeCommandHelp writes markdown help for a single command and its subcommands.
func writeCommandHelp(w io.Writer, cmd *cobra.Command, parentPath string, depth int) {
	fullPath := parentPath + " " + cmd.Name()
	heading := strings.Repeat("#", depth)

	fmt.Fprintf(w, "%s %s\n\n", heading, fullPath)

	// Description
	if cmd.Long != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Long)
	} else if cmd.Short != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Short)
	}

	// Usage
	fmt.Fprintf(w, "```\n%s\n```\n\n", strings.TrimRight(cmd.UseLine(), " "))

	// Aliases
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(w, "**Aliases:** %s\n\n", strings.Join(cmd.Aliases, ", "))
	}

	// Examples
	if cmd.Example != "" {
		fmt.Fprintf(w, "**Examples:**\n\n```bash\n%s\n```\n\n", cmd.Example)
	}

	// Local flags (not inherited/global)
	localFlags := cmd.NonInheritedFlags()
	if localFlags.HasFlags() {
		fmt.Fprintf(w, "**Flags:**\n\n```\n%s```\n\n", localFlags.FlagUsages())
	}

	// Subcommands
	subCmds := cmd.Commands()
	hasVisibleSubs := false
	for _, sub := range subCmds {
		if sub.IsAvailableCommand() {
			hasVisibleSubs = true
			break
		}
	}

	if hasVisibleSubs {
		for _, sub := range subCmds {
			if !sub.IsAvailableCommand() {
				continue
			}
			writeCommandHelp(w, sub, fullPath, depth+1)
		}
	}
}
