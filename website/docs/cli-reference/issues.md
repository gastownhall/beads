---
id: issues
title: Issue Commands
sidebar_position: 3
---

# Issue Commands

Commands for managing issues.

## bd create

Create a new issue.

```bash
bd create <title> [flags]
```

**All flags:**
```bash
--type, -t        Issue type (bug|feature|task|epic|chore)
--priority, -p    Priority 0-4
--description, -d Detailed description
--design          Design notes
--acceptance      Acceptance criteria
--notes           Additional notes
--labels, -l      Comma-separated labels
--parent          Parent issue ID
--deps            Dependencies (type:id format)
--assignee        Assigned user
--json            JSON output
```

**Examples:**
```bash
# Bug with high priority
bd create "Login fails with special chars" -t bug -p 1

# Feature with description
bd create "Add export to PDF" -t feature -p 2 \
  --description="Users want to export reports as PDF files"

# Feature with design, acceptance, and notes
bd create "Implement user authentication" -t feature -p 1 \
  --description="Add JWT-based authentication" \
  --design="Use bcrypt for password hashing, JWT for sessions" \
  --acceptance="All tests pass, security audit complete" \
  --notes="Consider rate limiting for login attempts"

# Task with labels
bd create "Update CI config" -t task -l "ci,infrastructure"

# Epic with children
bd create "Auth System" -t epic -p 1
bd create "Design login UI" --parent bd-42
bd create "Implement backend" --parent bd-42

# Discovered issue
bd create "Found SQL injection" -t bug -p 0 \
  --deps discovered-from:bd-42 --json
```

## bd show

Display issue details.

```bash
bd show <id>... [flags]
```

**Flags:**
```bash
--full        Show all fields including comments
--json        JSON output
```

**Examples:**
```bash
bd show bd-42
bd show bd-42 --full
bd show bd-42 bd-43 bd-44 --json
```

## bd update

Update issue fields.

```bash
bd update <id> [flags]
```

**All flags:**
```bash
--status          New status (open|in_progress|closed)
--priority        New priority (0-4)
--title           New title
--description     New description
--type            New type
--add-label       Add label(s)
--remove-label    Remove label(s)
--assignee        New assignee
--json            JSON output
```

**Examples:**
```bash
# Start work
bd update bd-42 --status in_progress

# Escalate priority
bd update bd-42 --priority 0 --add-label urgent

# Change title and description
bd update bd-42 --title "New title" --description="Updated description"

# Multiple changes
bd update bd-42 --status in_progress --priority 1 --add-label "in-review" --json
```

## bd close

Close an issue.

```bash
bd close <id> [flags]
```

**Flags:**
```bash
--reason    Closure reason (stored in comment)
--json      JSON output
```

**Examples:**
```bash
bd close bd-42
bd close bd-42 --reason "Fixed in commit abc123"
bd close bd-42 --reason "Duplicate of bd-43" --json
```

## bd reopen

Reopen a closed issue.

```bash
bd reopen <id> [flags]
```

**Examples:**
```bash
bd reopen bd-42
bd reopen bd-42 --json
```

## bd delete

Delete an issue.

```bash
bd delete <id> [flags]
```

**Flags:**
```bash
--force, -f    Skip confirmation
--json         JSON output
```

**Examples:**
```bash
bd delete bd-42
bd delete bd-42 -f --json
```

**Note:** Deletions are tracked in the Dolt database for sync.

## bd search

Search issues by text.

```bash
bd search <query> [flags]
```

**Flags:**
```bash
--status    Filter by status
--type      Filter by type
--json      JSON output
```

**Examples:**
```bash
bd search "authentication"
bd search "login bug" --status open
bd search "API" --type feature --json
```

## bd duplicates

Find and manage duplicate issues.

```bash
bd duplicates [flags]
```

**Flags:**
```bash
--auto-merge    Automatically merge all duplicates
--dry-run       Preview without changes
--json          JSON output
```

**Examples:**
```bash
bd duplicates
bd duplicates --auto-merge
bd duplicates --dry-run --json
```

## bd merge

Merge duplicate issues.

```bash
bd merge <source>... --into <target> [flags]
```

**Flags:**
```bash
--into      Target issue to merge into
--dry-run   Preview without changes
--json      JSON output
```

**Examples:**
```bash
bd merge bd-42 bd-43 --into bd-41
bd merge bd-42 bd-43 --into bd-41 --dry-run --json
```
