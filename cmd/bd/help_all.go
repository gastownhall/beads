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

// helpDocFlag is the --doc flag for generating single command docs
var helpDocFlag string

// helpListFlag is the --list flag for listing available commands
var helpListFlag bool

// registerHelpAllFlag adds the --all, --doc, and --list flags to Cobra's auto-generated help command.
// Must be called after rootCmd.InitDefaultHelpCmd() has run (i.e., after first Execute
// or explicit init). We hook it in init() after all subcommands are registered.
func registerHelpAllFlag() {
	// Cobra lazily creates the help command. We need to find it.
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "help" {
			cmd.Flags().BoolVar(&helpAllFlag, "all", false, "Show help for all commands in a single document")
			cmd.Flags().StringVar(&helpDocFlag, "doc", "", "Generate markdown docs for a single command (use - for stdout)")
			cmd.Flags().BoolVar(&helpListFlag, "list", false, "List all available commands")

			// Wrap the existing Run to check --all, --doc, and --list first
			originalRun := cmd.Run
			cmd.Run = func(cmd *cobra.Command, args []string) {
				if helpListFlag {
					// Handle --list flag: list all available commands
					listAllCommands(os.Stdout, rootCmd)
					return
				}
				if helpDocFlag != "" {
					// Handle --doc flag: generate single command docs
					writeSingleCommandDoc(os.Stdout, rootCmd, helpDocFlag)
					return
				}
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

// sidebarPositionMap maps command names to their Docusaurus sidebar position
// This controls the ordering of commands in the website sidebar.
var sidebarPositionMap = map[string]int{
	"create":  10,
	"list":    20,
	"ready":   30,
	"show":    40,
	"update":  50,
	"close":   60,
	"delete":  70,
	"reopen":  80,
	"dep":     100,
	"label":   110,
	"state":   120,
	"sync":    200,
	"import":  210,
	"export":  220,
	"mol":     300,
	"formula": 310,
	"init":    400,
	"setup":   410,
	"config":  420,
	"prime":   500,
	"doctor":  600,
	"admin":   610,
	"migrate": 620,
}

// writeSingleCommandDoc generates markdown documentation for a single command
// with Docusaurus frontmatter for website integration.
func writeSingleCommandDoc(w io.Writer, root *cobra.Command, cmdName string) {
	// Find the command (handle nested commands like "mol pour")
	cmd := findCommand(root, cmdName)
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "Error: command not found: %s\n", cmdName)
		fmt.Fprintf(os.Stderr, "Available commands: ")
		for _, c := range root.Commands() {
			if c.IsAvailableCommand() {
				fmt.Fprintf(os.Stderr, "%s ", c.Name())
			}
		}
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}

	// Get sidebar position (default to 999 if not in map)
	position := 999
	if pos, ok := sidebarPositionMap[cmdName]; ok {
		position = pos
	}

	// Generate Docusaurus frontmatter
	fmt.Fprintf(w, "---\n")
	fmt.Fprintf(w, "id: %s\n", cmdName)
	fmt.Fprintf(w, "title: bd %s\n", cmdName)
	fmt.Fprintf(w, "sidebar_position: %d\n", position)
	fmt.Fprintf(w, "---\n")
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "<!-- AUTO-GENERATED: do not edit manually -->\n")
	fmt.Fprintf(w, "Generated from `bd help --doc %s` (bd version %s)\n\n", cmdName, Version)

	// Generate the command help (using h2 for single command)
	writeCommandHelp(w, cmd, "bd", 2)
}

// findCommand finds a command by name in the command tree.
// Supports nested commands like "mol pour" by splitting on space.
func findCommand(root *cobra.Command, name string) *cobra.Command {
	// Handle nested commands (e.g., "mol pour")
	parts := strings.Split(name, " ")

	var current *cobra.Command
	for i, part := range parts {
		if i == 0 {
			// Start from root's direct commands
			current = findDirectCommand(root, part)
		} else {
			// Look in subcommands of current
			if current != nil {
				current = findDirectCommand(current, part)
			}
		}
		if current == nil {
			return nil
		}
	}
	return current
}

// findDirectCommand finds a direct child command by name.
func findDirectCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, cmd := range parent.Commands() {
		if cmd.Name() == name {
			return cmd
		}
		// Also check aliases
		for _, alias := range cmd.Aliases {
			if alias == name {
				return cmd
			}
		}
	}
	return nil
}

// listAllCommands prints all available commands, one per line.
// Used by the generate-cli-docs.sh script.
func listAllCommands(w io.Writer, root *cobra.Command) {
	for _, cmd := range root.Commands() {
		if cmd.IsAvailableCommand() {
			fmt.Fprintln(w, cmd.Name())
		}
	}
}
