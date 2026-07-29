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

	body := renderInitConfigYAML(prefix, noDbMode)
	if err := os.WriteFile(configYamlPath, body, 0600); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

func renderInitConfigYAML(prefix string, noDbMode bool) []byte {
	noDbLine := "# no-db: false"
	if noDbMode {
		noDbLine = "no-db: true  # JSONL-only mode, no database"
	}

	prefixLine := "# issue-prefix: \"\""
	if noDbMode && prefix != "" {
		prefixLine = fmt.Sprintf("issue-prefix: %q", prefix)
	}

	body := fmt.Sprintf(`# Beads Configuration File
# This file configures default behavior for all bd commands in this repository
# All settings can also be set via environment variables (BD_* prefix)
# or overridden with command-line flags

# Issue prefix for this repository (used by bd init)
# If not set, bd init will auto-detect from directory name
# Example: issue-prefix: "myproject" creates issues like "myproject-1", "myproject-2", etc.
%s

# Use no-db mode: JSONL-only, no Dolt database
# When true, .beads/issues.jsonl is the only local store
%s

# Enable JSON output by default
# json: false

# Feedback title formatting for mutating commands (create/update/close/dep/edit)
# 0 = hide titles, N > 0 = truncate to N characters
# output:
#   title-length: 255

# Default actor for audit trails (overridden by BEADS_ACTOR or --actor)
# actor: ""

# Optional JSONL sidecar for explicit agent/tool interaction audit records.
# Issue history is always recorded in the database and is visible with
# bd history <id> --events; this only controls .beads/interactions.jsonl.
# audit:
#   enabled: false

# Export events (audit trail) to .beads/events.jsonl on each flush/sync
# When enabled, new events are appended incrementally using a high-water mark.
# Use 'bd export --events' to trigger manually regardless of this setting.
# events-export: false

# Multi-repo configuration (experimental - bd-307)
# Allows hydrating from multiple repositories and routing writes to the correct database
# repos:
#   primary: "."  # Primary repo (where this database lives)
#   additional:   # Additional repos to hydrate from (read-only)
#     - ~/beads-planning  # Personal planning repo
#     - ~/work-planning   # Work planning repo

# Dolt-native backup (periodic backup for off-machine recovery)
# This is full database backup only. Cross-machine sync uses Dolt remotes.
# backup:
#   enabled: false     # Disable auto-backup entirely
#   interval: 15m      # Minimum time between auto-backups
#   git-push: false    # Disable git push (backup locally only)
#   git-repo: ""       # Separate git repo for backups (default: project repo)

# Optional JSONL auto-export for viewers, interchange, and issue-level migration.
# Disabled by default; enable only when an integration needs fresh .beads/issues.jsonl.
# Use relative paths under .beads/ for JSONL import/export filenames.
# export:
#   auto: false
#   path: issues.jsonl
#   interval: 60s
#   git-add: false
# import:
#   path: issues.jsonl

# Integration settings (access with 'bd config get/set')
# Non-secret keys (stored in the database):
# - jira.url, jira.project
# - linear.team_id
# - github.org, github.repo
#
# Secret keys (stored in this file but prefer env vars to avoid git exposure):
# - linear.api_key  → use LINEAR_API_KEY env var instead
# - github.token    → use GITHUB_TOKEN env var instead
`, prefixLine, noDbLine)
	return []byte(body)
}

const BeadsReadmeTemplate = `# Beads - AI-Native Issue Tracking

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
bd update <issue-id> --claim
bd update <issue-id> --status done

# Sync with Dolt remote
bd dolt push
` + "```" + `

### Working with Issues

Issues in Beads are:
- **Git-native**: Stored in Dolt database with version control and branching
- **AI-friendly**: CLI-first design works perfectly with AI coding agents
- **Branch-aware**: Issues can follow your branch workflow
- **Sync-ready**: Uses Dolt remotes for backup and team sharing

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
- Dolt-native sync via bd dolt push / bd dolt pull
- Branch-aware issue tracking
- Dolt-native three-way merge resolution

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

func createReadme(beadsDir string) error {
	readmePath := filepath.Join(beadsDir, "README.md")

	// Skip if already exists
	if _, err := os.Stat(readmePath); err == nil {
		return nil
	}

	// Write README.md (0644 is standard for markdown files)
	// #nosec G306 - README needs to be readable
	if err := os.WriteFile(readmePath, []byte(BeadsReadmeTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	return nil
}
