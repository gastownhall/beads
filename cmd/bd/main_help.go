package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/ui"
)

// colorizedHelpFunc wraps Cobra's default help with semantic coloring
// Applies subtle accent color to group headers for visual hierarchy
func colorizedHelpFunc(cmd *cobra.Command, args []string) {
	// Build full help output: Long description + Usage
	var output strings.Builder

	// Include Long description first (like Cobra's default help)
	if cmd.Long != "" {
		output.WriteString(cmd.Long)
		output.WriteString("\n\n")
	} else if cmd.Short != "" {
		output.WriteString(cmd.Short)
		output.WriteString("\n\n")
	}

	// Add the usage string which contains commands, flags, etc.
	output.WriteString(cmd.UsageString())

	// Apply semantic coloring
	result := colorizeHelpOutput(output.String())
	fmt.Print(result)
}

// colorizeHelpOutput applies semantic colors to help text
// - Group headers get accent color for visual hierarchy
// - Section headers (Examples:, Flags:) get accent color
// - Command names get subtle styling for scanability
// - Flag names get bold styling, types get muted
// - Default values get muted styling
func colorizeHelpOutput(help string) string {
	// Match group header lines (e.g., "Working With Issues:")
	// These are standalone lines ending with ":" and followed by commands
	groupHeaderRE := regexp.MustCompile(`(?m)^([A-Z][A-Za-z &]+:)\s*$`)

	result := groupHeaderRE.ReplaceAllStringFunc(help, func(match string) string {
		// Trim whitespace, colorize, then restore
		trimmed := strings.TrimSpace(match)
		return ui.RenderAccent(trimmed)
	})

	// Match section headers in subcommand help (Examples:, Flags:, etc.)
	sectionHeaderRE := regexp.MustCompile(`(?m)^(Examples|Flags|Usage|Global Flags|Aliases|Available Commands):`)
	result = sectionHeaderRE.ReplaceAllStringFunc(result, func(match string) string {
		return ui.RenderAccent(match)
	})

	// Match command lines: "  command   Description text"
	// Commands are indented with 2 spaces, followed by spaces, then description
	// Pattern matches: indent + command-name (with hyphens) + spacing + description
	cmdLineRE := regexp.MustCompile(`(?m)^(  )([a-z][a-z0-9]*(?:-[a-z0-9]+)*)(\s{2,})(.*)$`)

	result = cmdLineRE.ReplaceAllStringFunc(result, func(match string) string {
		parts := cmdLineRE.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		indent := parts[1]
		cmdName := parts[2]
		spacing := parts[3]
		description := parts[4]

		// Colorize command references in description (e.g., 'comments add')
		description = colorizeCommandRefs(description)

		// Highlight entry point hints (e.g., "(start here)")
		description = highlightEntryPoints(description)

		// Subtle styling on command name for scanability
		return indent + ui.RenderCommand(cmdName) + spacing + description
	})

	// Match flag lines: "  -f, --file string   Description"
	// Pattern: indent + flags + spacing + optional type + description
	flagLineRE := regexp.MustCompile(`(?m)^(\s+)(-\w,\s+--[\w-]+|--[\w-]+)(\s+)(string|int|duration|bool)?(\s*.*)$`)
	result = flagLineRE.ReplaceAllStringFunc(result, func(match string) string {
		parts := flagLineRE.FindStringSubmatch(match)
		if len(parts) < 6 {
			return match
		}
		indent := parts[1]
		flags := parts[2]
		spacing := parts[3]
		typeStr := parts[4]
		desc := parts[5]

		// Mute default values in description
		desc = muteDefaults(desc)

		if typeStr != "" {
			return indent + ui.RenderCommand(flags) + spacing + ui.RenderMuted(typeStr) + desc
		}
		return indent + ui.RenderCommand(flags) + spacing + desc
	})

	return result
}

// muteDefaults applies muted styling to default value annotations
func muteDefaults(text string) string {
	defaultRE := regexp.MustCompile(`(\(default[^)]*\))`)
	return defaultRE.ReplaceAllStringFunc(text, func(match string) string {
		return ui.RenderMuted(match)
	})
}

// highlightEntryPoints applies accent styling to entry point hints like "(start here)"
func highlightEntryPoints(text string) string {
	entryRE := regexp.MustCompile(`(\(start here\))`)
	return entryRE.ReplaceAllStringFunc(text, func(match string) string {
		return ui.RenderAccent(match)
	})
}

// colorizeCommandRefs applies command styling to references in text
// Matches patterns like 'command name' or 'bd command'
func colorizeCommandRefs(text string) string {
	// Match 'command words' in single quotes (e.g., 'comments add')
	cmdRefRE := regexp.MustCompile(`'([a-z][a-z0-9 -]+)'`)

	return cmdRefRE.ReplaceAllStringFunc(text, func(match string) string {
		// Extract the command name without quotes
		inner := match[1 : len(match)-1]
		return "'" + ui.RenderCommand(inner) + "'"
	})
}
