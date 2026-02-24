package recipes

// Template is the universal beads workflow template.
// This content is written to all file-based recipes.
const Template = `# Beads Issue Tracking

This project uses [Beads (bd)](https://github.com/steveyegge/beads) for issue tracking.

## Core Rules

- Track ALL work in bd (never use markdown TODOs or comment-based task lists)
- Use ` + "`bd ready`" + ` to find available work
- Use ` + "`bd create`" + ` to track new issues/tasks/bugs
- Use ` + "`bd sync`" + ` at end of session to sync with git remote
- Git hooks auto-sync on commit/merge

## Quick Reference

` + "```bash" + `
bd prime                              # Load complete workflow context
bd ready                              # Show issues ready to work (no blockers)
bd list --status=open                 # List all open issues
bd create --title="..." --type=task   # Create new issue
bd update <id> --claim                # Claim work atomically
bd close <id>                         # Mark complete
bd dep add <issue> <depends-on>       # Add dependency
bd sync                               # Sync with git remote
` + "```" + `

## Workflow

1. Check for ready work: ` + "`bd ready`" + `
2. Claim an issue atomically: ` + "`bd update <id> --claim`" + `
3. Do the work
4. Mark complete: ` + "`bd close <id>`" + `
5. Sync: ` + "`bd sync`" + ` (or let git hooks handle it)

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

For detailed docs: see AGENTS.md, QUICKSTART.md, or run ` + "`bd --help`" + `
`
