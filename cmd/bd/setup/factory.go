package setup

import (
	"errors"
	"fmt"
	"io"
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

var (
	errAgentsFileMissing   = errors.New("agents file not found")
	errBeadsSectionMissing = errors.New("beads section missing")
)

type factoryEnv struct {
	agentsPath string
	stdout     io.Writer
	stderr     io.Writer
}

var factoryEnvProvider = defaultFactoryEnv

func defaultFactoryEnv() factoryEnv {
	return factoryEnv{
		agentsPath: "AGENTS.md",
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}
}

// InstallFactory installs Factory.ai/Droid integration
func InstallFactory() {
	env := factoryEnvProvider()
	if err := installFactory(env); err != nil {
		setupExit(1)
	}
}

func installFactory(env factoryEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Installing Factory.ai (Droid) integration...")

	var currentContent string
	data, err := os.ReadFile(env.agentsPath)
	if err == nil {
		currentContent = string(data)
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	if currentContent != "" {
		if strings.Contains(currentContent, factoryBeginMarker) {
			newContent := updateBeadsSection(currentContent)
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintln(env.stdout, "✓ Updated existing beads section in AGENTS.md")
		} else {
			newContent := currentContent + "\n\n" + factoryBeadsSection
			if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
				return err
			}
			_, _ = fmt.Fprintln(env.stdout, "✓ Added beads section to existing AGENTS.md")
		}
	} else {
		newContent := createNewAgentsFile()
		if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
			return err
		}
		_, _ = fmt.Fprintln(env.stdout, "✓ Created new AGENTS.md with beads integration")
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ Factory.ai (Droid) integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  File: %s\n", env.agentsPath)
	_, _ = fmt.Fprintln(env.stdout, "\nFactory Droid will automatically read AGENTS.md on session start.")
	_, _ = fmt.Fprintln(env.stdout, "No additional configuration needed!")
	return nil
}

// CheckFactory checks if Factory.ai integration is installed
func CheckFactory() {
	env := factoryEnvProvider()
	if err := checkFactory(env); err != nil {
		setupExit(1)
	}
}

func checkFactory(env factoryEnv) error {
	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "✗ AGENTS.md not found")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup factory")
		return errAgentsFileMissing
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if strings.Contains(content, factoryBeginMarker) {
		_, _ = fmt.Fprintf(env.stdout, "✓ Factory.ai integration installed: %s\n", env.agentsPath)
		_, _ = fmt.Fprintln(env.stdout, "  Beads section found in AGENTS.md")
		return nil
	}

	_, _ = fmt.Fprintln(env.stdout, "⚠ AGENTS.md exists but no beads section found")
	_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup factory (to add beads section)")
	return errBeadsSectionMissing
}

// RemoveFactory removes Factory.ai integration
func RemoveFactory() {
	env := factoryEnvProvider()
	if err := removeFactory(env); err != nil {
		setupExit(1)
	}
}

func removeFactory(env factoryEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Removing Factory.ai (Droid) integration...")
	data, err := os.ReadFile(env.agentsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "No AGENTS.md file found")
		return nil
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.agentsPath, err)
		return err
	}

	content := string(data)
	if !strings.Contains(content, factoryBeginMarker) {
		_, _ = fmt.Fprintln(env.stdout, "No beads section found in AGENTS.md")
		return nil
	}

	newContent := removeBeadsSection(content)
	trimmed := strings.TrimSpace(newContent)
	if trimmed == "" {
		if err := os.Remove(env.agentsPath); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to remove %s: %v\n", env.agentsPath, err)
			return err
		}
		_, _ = fmt.Fprintf(env.stdout, "✓ Removed %s (file was empty after removing beads section)\n", env.agentsPath)
		return nil
	}

	if err := atomicWriteFile(env.agentsPath, []byte(newContent)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.agentsPath, err)
		return err
	}
	_, _ = fmt.Fprintln(env.stdout, "✓ Removed beads section from AGENTS.md")
	return nil
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
