package recipes

import "fmt"

// Template is the universal beads workflow template.
// This content is written to all file-based recipes.
const Template = `# Beads Issue Tracking

This project uses [Beads (bd)](https://github.com/gastownhall/beads) for issue tracking.

## Core Rules

- Track ALL work in bd (never use markdown TODOs or comment-based task lists)
- Use ` + "`bd ready`" + ` to find available work
- Use ` + "`bd create`" + ` to track new issues/tasks/bugs
- Treat commit, push, and Dolt remote sync as policy-controlled handoff actions
- Run ` + "`bd prime`" + ` for complete workflow context (SSOT for operational commands)
- Default to conservative git authority: report status and proposed commands unless the user, orchestrator, or repository profile explicitly authorizes commit/sync/push

## Quick Reference

` + "```bash" + `
bd prime                              # Load complete workflow context (SSOT)
bd ready                              # Show issues ready to work (no blockers)
bd list --status=open                 # List all open issues
bd create "title" -t task -p 2        # Create new issue
bd update <id> --claim                # Claim work atomically
bd unclaim <id>                       # Release stuck issue (agent crashed)
bd close <id>                         # Mark complete
bd dep add <issue> <depends-on>       # Add dependency
bd dolt push                          # Sync with remote when authorized
` + "```" + `

## Workflow

1. Check for ready work: ` + "`bd ready`" + `
2. Claim an issue atomically: ` + "`bd update <id> --claim`" + `
3. Do the work
4. Mark complete: ` + "`bd close <id>`" + `
5. Handoff: report changed files, validation, issue status, and any proposed commit/sync/push commands

## Issue Types

- ` + "`bug`" + ` - Something broken
- ` + "`feature`" + ` - New functionality
- ` + "`task`" + ` - Work item (tests, docs, refactoring)
- ` + "`epic`" + ` - Large feature with subtasks
- ` + "`chore`" + ` - Maintenance (dependencies, tooling)

## Priorities

- ` + "`0`" + ` - Critical (security, data loss, broken builds)
- ` + "`1`" + ` - High (major features, important bugs)
- ` + "`2`" + ` - Medium (default, nice-to-have)
- ` + "`3`" + ` - Low (polish, optimization)
- ` + "`4`" + ` - Backlog (future ideas)

## Context Loading

Run ` + "`bd prime`" + ` to get complete workflow documentation in AI-optimized format.
` + "`bd prime`" + ` is the single source of truth for operational commands and session workflow.

For detailed docs: see AGENTS.md, QUICKSTART.md, or run ` + "`bd --help`" + `
`

// CopilotInstructionsTemplate is the repository instructions file used by the
// lightweight Copilot CLI plugin recipe.
const CopilotInstructionsTemplate = `# GitHub Copilot Instructions

This repository uses **Beads (bd)** for issue tracking.

## Core Workflow

- Use ` + "`bd ready`" + ` to find unblocked work
- Use ` + "`bd create`" + ` to track new work
- Use ` + "`bd update <id> --claim`" + ` before starting
- Use ` + "`bd close <id>`" + ` when work is complete
- Treat commit, push, and Dolt remote sync as policy-controlled handoff actions
- Do not commit, push, or run Dolt remote sync unless explicitly authorized

## Context Loading

Run ` + "`bd prime`" + ` for the full workflow context.

If the Beads Copilot plugin is installed, Copilot CLI will automatically run
` + "`bd prime`" + ` on session start and before compaction.
`

// ContentForPath returns the file content that should be written for a recipe path.
func ContentForPath(recipe Recipe, path string) (string, error) {
	switch recipe.Type {
	case TypeFile:
		if recipe.Content != "" {
			return recipe.Content, nil
		}
		return Template, nil
	case TypeMultiFile:
		if len(recipe.Contents) == 0 {
			return "", fmt.Errorf("recipe %q has no file contents", recipe.Name)
		}
		content, ok := recipe.Contents[path]
		if !ok {
			return "", fmt.Errorf("recipe %q has no content for %s", recipe.Name, path)
		}
		return content, nil
	default:
		return "", fmt.Errorf("recipe %q has unsupported type %q", recipe.Name, recipe.Type)
	}
}
