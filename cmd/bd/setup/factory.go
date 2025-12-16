package setup

import (
	"fmt"
	"os"
	"strings"
)

// Factory/Droid integration markers for AGENTS.md
const (
	factoryBeginMarker = "<!-- BEGIN BEADS INTEGRATION -->"
	factoryEndMarker   = "<!-- END BEADS INTEGRATION -->"
)

const factoryBeadsSection = `<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

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
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
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
   - ` + "`bd create \"Found bug\" --description=\"Details about what was found\" -p 1 --deps discovered-from:<parent-id>`" + `
5. **Complete**: ` + "`bd close <id> --reason \"Done\"`" + `

### Auto-Sync

bd automatically syncs with git:

- Exports to ` + "`.beads/issues.jsonl`" + ` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after ` + "`git pull`" + `)
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use ` + "`--json`" + ` flag for programmatic use
- ✅ Link discovered work with ` + "`discovered-from`" + ` dependencies
- ✅ Check ` + "`bd ready`" + ` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

<!-- END BEADS INTEGRATION -->
`

// InstallFactory installs Factory.ai/Droid integration
func InstallFactory() {
	agentsPath := "AGENTS.md"

	fmt.Println("Installing Factory.ai (Droid) integration...")

	// Check if AGENTS.md exists
	var currentContent string
	data, err := os.ReadFile(agentsPath)
	if err == nil {
		currentContent = string(data)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: failed to read AGENTS.md: %v\n", err)
		os.Exit(1)
	}

	// If file exists, check if we already have beads section
	if currentContent != "" {
		if strings.Contains(currentContent, factoryBeginMarker) {
			// Update existing section
			newContent := updateBeadsSection(currentContent)
			if err := atomicWriteFile(agentsPath, []byte(newContent)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: write AGENTS.md: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ Updated existing beads section in AGENTS.md")
		} else {
			// Append to existing file
			newContent := currentContent + "\n\n" + factoryBeadsSection
			if err := atomicWriteFile(agentsPath, []byte(newContent)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: write AGENTS.md: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ Added beads section to existing AGENTS.md")
		}
	} else {
		// Create new AGENTS.md with template
		newContent := createNewAgentsFile()
		if err := atomicWriteFile(agentsPath, []byte(newContent)); err != nil {
			fmt.Fprintf(os.Stderr, "Error: write AGENTS.md: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Created new AGENTS.md with beads integration")
	}

	fmt.Printf("\n✓ Factory.ai (Droid) integration installed\n")
	fmt.Printf("  File: %s\n", agentsPath)
	fmt.Println("\nFactory Droid will automatically read AGENTS.md on session start.")
	fmt.Println("No additional configuration needed!")
}

// CheckFactory checks if Factory.ai integration is installed
func CheckFactory() {
	agentsPath := "AGENTS.md"

	// Check if AGENTS.md exists
	data, err := os.ReadFile(agentsPath)
	if os.IsNotExist(err) {
		fmt.Println("✗ AGENTS.md not found")
		fmt.Println("  Run: bd setup factory")
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read AGENTS.md: %v\n", err)
		os.Exit(1)
	}

	// Check if it contains beads section
	content := string(data)
	if strings.Contains(content, factoryBeginMarker) {
		fmt.Println("✓ Factory.ai integration installed:", agentsPath)
		fmt.Println("  Beads section found in AGENTS.md")
	} else {
		fmt.Println("⚠ AGENTS.md exists but no beads section found")
		fmt.Println("  Run: bd setup factory (to add beads section)")
		os.Exit(1)
	}
}

// RemoveFactory removes Factory.ai integration
func RemoveFactory() {
	agentsPath := "AGENTS.md"

	fmt.Println("Removing Factory.ai (Droid) integration...")

	// Read current content
	data, err := os.ReadFile(agentsPath)
	if os.IsNotExist(err) {
		fmt.Println("No AGENTS.md file found")
		return
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read AGENTS.md: %v\n", err)
		os.Exit(1)
	}

	content := string(data)

	// Check if beads section exists
	if !strings.Contains(content, factoryBeginMarker) {
		fmt.Println("No beads section found in AGENTS.md")
		return
	}

	// Remove beads section
	newContent := removeBeadsSection(content)

	// If file would be empty after removal, delete it
	trimmed := strings.TrimSpace(newContent)
	if trimmed == "" {
		if err := os.Remove(agentsPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to remove AGENTS.md: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Removed AGENTS.md (file was empty after removing beads section)")
	} else {
		// Write back modified content
		if err := atomicWriteFile(agentsPath, []byte(newContent)); err != nil {
			fmt.Fprintf(os.Stderr, "Error: write AGENTS.md: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Removed beads section from AGENTS.md")
	}
}

// updateBeadsSection replaces the beads section in existing content
func updateBeadsSection(content string) string {
	start := strings.Index(content, factoryBeginMarker)
	end := strings.Index(content, factoryEndMarker)

	if start == -1 || end == -1 || start > end {
		// Markers not found or invalid, append instead
		return content + "\n\n" + factoryBeadsSection
	}

	// Replace section between markers (including end marker line)
	endOfEndMarker := end + len(factoryEndMarker)
	// Find the next newline after end marker
	nextNewline := strings.Index(content[endOfEndMarker:], "\n")
	if nextNewline != -1 {
		endOfEndMarker += nextNewline + 1
	}

	return content[:start] + factoryBeadsSection + content[endOfEndMarker:]
}

// removeBeadsSection removes the beads section from content
func removeBeadsSection(content string) string {
	start := strings.Index(content, factoryBeginMarker)
	end := strings.Index(content, factoryEndMarker)

	if start == -1 || end == -1 || start > end {
		return content
	}

	// Find the next newline after end marker
	endOfEndMarker := end + len(factoryEndMarker)
	nextNewline := strings.Index(content[endOfEndMarker:], "\n")
	if nextNewline != -1 {
		endOfEndMarker += nextNewline + 1
	}

	// Also remove leading blank lines before the section
	trimStart := start
	for trimStart > 0 && (content[trimStart-1] == '\n' || content[trimStart-1] == '\r') {
		trimStart--
	}

	return content[:trimStart] + content[endOfEndMarker:]
}

// createNewAgentsFile creates a new AGENTS.md with a basic template
func createNewAgentsFile() string {
	return `# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

` + factoryBeadsSection + `

## Build & Test

_Add your build and test commands here_

` + "```bash" + `
# Example:
# npm install
# npm test
` + "```" + `

## Architecture Overview

_Add a brief overview of your project architecture_

## Conventions & Patterns

_Add your project-specific conventions here_
`
}
