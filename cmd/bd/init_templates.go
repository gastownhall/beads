package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// createConfigYaml creates the config.yaml template in the specified directory
// In --no-db mode, the prefix is saved here since there's no database to store it.
func createConfigYaml(beadsDir string, noDbMode bool, prefix string) error {
	configYamlPath := filepath.Join(beadsDir, "config.yaml")

	// Skip if already exists
	if _, err := os.Stat(configYamlPath); err == nil {
		return nil
	}

	noDbLine := "# no-db: false"
	if noDbMode {
		noDbLine = "no-db: true  # JSONL-only mode, no SQLite database"
	}

	// In no-db mode, we need to persist the prefix in config.yaml
	prefixLine := "# issue-prefix: \"\""
	if noDbMode && prefix != "" {
		prefixLine = fmt.Sprintf("issue-prefix: %q", prefix)
	}

	configYamlTemplate := fmt.Sprintf(`# Beads Configuration File
# This file configures default behavior for all bd commands in this repository
# All settings can also be set via environment variables (BD_* prefix)
# or overridden with command-line flags

# Issue prefix for this repository (used by bd init)
# If not set, bd init will auto-detect from directory name
# Example: issue-prefix: "myproject" creates issues like "myproject-1", "myproject-2", etc.
%s

# Use no-db mode: load from JSONL, no SQLite, write back after each command
# When true, bd will use .beads/issues.jsonl as the source of truth
# instead of SQLite database
%s

# Disable daemon for RPC communication (forces direct database access)
# no-daemon: false

# Disable auto-flush of database to JSONL after mutations
# no-auto-flush: false

# Disable auto-import from JSONL when it's newer than database
# no-auto-import: false

# Enable JSON output by default
# json: false

# Default actor for audit trails (overridden by BD_ACTOR or --actor)
# actor: ""

# Path to database (overridden by BEADS_DB or --db)
# db: ""

# Auto-start daemon if not running (can also use BEADS_AUTO_START_DAEMON)
# auto-start-daemon: true

# Debounce interval for auto-flush (can also use BEADS_FLUSH_DEBOUNCE)
# flush-debounce: "5s"

# Git branch for beads commits (bd sync will commit to this branch)
# IMPORTANT: Set this for team projects so all clones use the same sync branch.
# This setting persists across clones (unlike database config which is gitignored).
# Can also use BEADS_SYNC_BRANCH env var for local override.
# If not set, bd sync will require you to run 'bd config set sync.branch <branch>'.
# sync-branch: "beads-sync"

# Multi-repo configuration (experimental - bd-307)
# Allows hydrating from multiple repositories and routing writes to the correct JSONL
# repos:
#   primary: "."  # Primary repo (where this database lives)
#   additional:   # Additional repos to hydrate from (read-only)
#     - ~/beads-planning  # Personal planning repo
#     - ~/work-planning   # Work planning repo

# Integration settings (access with 'bd config get/set')
# These are stored in the database, not in this file:
# - jira.url
# - jira.project
# - linear.url
# - linear.api-key
# - github.org
# - github.repo
`, prefixLine, noDbLine)

	if err := os.WriteFile(configYamlPath, []byte(configYamlTemplate), 0600); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

// createReadme creates the README.md file in the .beads directory
func createReadme(beadsDir string) error {
	readmePath := filepath.Join(beadsDir, "README.md")

	// Skip if already exists
	if _, err := os.Stat(readmePath); err == nil {
		return nil
	}

	readmeTemplate := `# Beads - AI-Native Issue Tracking

Welcome to Beads! This repository uses **Beads** for issue tracking - a modern, AI-native tool designed to live directly in your codebase alongside your code.

## What is Beads?

Beads is issue tracking that lives in your repo, making it perfect for AI coding agents and developers who want their issues close to their code. No web UI required - everything works through the CLI and integrates seamlessly with git.

**Learn more:** [github.com/steveyegge/beads](https://github.com/steveyegge/beads)

## Quick Start

### Essential Commands

` + "```bash" + `
# Create new issues
bd create "Add user authentication"

# View all issues
bd list

# View issue details
bd show <issue-id>

# Update issue status
bd update <issue-id> --status in_progress
bd update <issue-id> --status done

# Sync with git remote
bd sync
` + "```" + `

### Working with Issues

Issues in Beads are:
- **Git-native**: Stored in ` + "`.beads/issues.jsonl`" + ` and synced like code
- **AI-friendly**: CLI-first design works perfectly with AI coding agents
- **Branch-aware**: Issues can follow your branch workflow
- **Always in sync**: Auto-syncs with your commits

## Why Beads?

✨ **AI-Native Design**
- Built specifically for AI-assisted development workflows
- CLI-first interface works seamlessly with AI coding agents
- No context switching to web UIs

🚀 **Developer Focused**
- Issues live in your repo, right next to your code
- Works offline, syncs when you push
- Fast, lightweight, and stays out of your way

🔧 **Git Integration**
- Automatic sync with git commits
- Branch-aware issue tracking
- Intelligent JSONL merge resolution

## Get Started with Beads

Try Beads in your own projects:

` + "```bash" + `
# Install Beads
curl -sSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

# Initialize in your repo
bd init

# Create your first issue
bd create "Try out Beads"
` + "```" + `

## Learn More

- **Documentation**: [github.com/steveyegge/beads/docs](https://github.com/steveyegge/beads/tree/main/docs)
- **Quick Start Guide**: Run ` + "`bd quickstart`" + `
- **Examples**: [github.com/steveyegge/beads/examples](https://github.com/steveyegge/beads/tree/main/examples)

---

*Beads: Issue tracking that moves at the speed of thought* ⚡
`

	// Write README.md (0644 is standard for markdown files)
	// #nosec G306 - README needs to be readable
	if err := os.WriteFile(readmePath, []byte(readmeTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	return nil
}
