---
title: "bd update"
description: "Update one or more issues"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc update`.

Update one or more issues.

If no issue ID is provided, updates the last touched issue (from most recent
create, update, show, or close operation). This fallback only applies in
interactive sessions (stdin is a terminal); in scripts and agent sessions a
missing ID is an error, so a command built from an empty variable cannot
silently mutate an unrelated issue. Set BD_LAST_TOUCHED_FALLBACK=1 to allow
the fallback anywhere, or =0 to disable it entirely.

Updates are applied per issue ID, not atomically across IDs: when some IDs
fail, the remaining issues are still updated, every failed ID is reported on
stderr, and the command exits nonzero.

Exit codes: 1 for general failures; 13 when every failure is a stale
--if-assignee/--if-status guard (the precondition no longer held, nothing was
written — another actor won the race, so retrying the same guard is
pointless).

```
bd update [id...] [flags]
```

**Flags:**

```
      --acceptance string            Acceptance criteria
      --add-label strings            Add labels (repeatable)
      --allow-empty-description      Allow empty description replacement when reading from stdin or file
      --append-notes string          Append to existing notes (with newline separator)
  -a, --assignee string              Assignee
      --await-id string              Set gate await_id (e.g., GitHub run ID for gh:run gates)
      --body-file string             Read description from file (use - for stdin)
      --claim                        Atomically claim the issue (sets assignee to you, status to in_progress; idempotent if already claimed by you; issues assigned to a pool alias listed in the claim.pools config are claimable too)
      --defer string                 Defer until date (empty to clear). Issue hidden from bd ready until then, then auto-wakes to open
  -d, --description string           Issue description
      --design string                Design notes
      --design-file string           Read design from file (use - for stdin)
      --due string                   Due date/time (empty to clear). Formats: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15
      --ephemeral                    Mark issue as ephemeral (wisp) - not exported to JSONL
  -e, --estimate int                 Time estimate in minutes (e.g., 60 for 1 hour)
      --external-ref string          External reference (e.g., 'gh-9', 'jira-ABC', Linear URL)
      --force                        Override two refusals: let -a/--assignee overwrite another actor's live in_progress claim (use only for abandoned claims — crashed agent, expired lease; prefer bd reclaim), and let -s/--status move the issue into closed (or a configured done status) despite open children or a live blocker (same as bd close --force)
      --history                      Clear no-history flag (re-enable Dolt commit history)
      --if-assignee string           Apply the update only if the current assignee equals this value (--if-assignee '' requires unassigned); a mismatch writes nothing and exits 13 (vs 1 for other failures). Requires a field update; cannot combine with --claim
      --if-status string             Apply the update only if the current status equals this value; a mismatch writes nothing and exits 13 (vs 1 for other failures). Requires a field update; cannot combine with --claim
      --metadata string              Set custom metadata (JSON string or @file.json to read from file)
      --no-history                   Mark issue as no-history (skip Dolt commits, not GC-eligible)
      --notes string                 Additional notes (replaces existing notes; use --append-notes to append)
      --parent string                New parent issue ID (reparents the issue, use empty string to remove parent)
      --persistent                   Mark issue as persistent (promote wisp to regular issue)
  -p, --priority string              Priority (0-4 or P0-P4, 0=highest)
      --remove-label strings         Remove labels (repeatable)
      --session string               Claude Code session ID for status=closed (or set CLAUDE_SESSION_ID env var)
      --set-labels strings           Set labels, replacing all existing (repeatable)
      --set-metadata stringArray     Set metadata key=value (repeatable, e.g., --set-metadata team=platform)
      --spec-id string               Link to specification document
  -s, --status string                New status
      --stdin                        Read description from stdin (alias for --body-file -)
      --title string                 New title
  -t, --type string                  New type (bug|feature|task|epic|chore|decision|spike|story|milestone); custom types require types.custom config; aliases: enhancement/feat→feature, dec/adr→decision
      --unset-metadata stringArray   Remove metadata key (repeatable, e.g., --unset-metadata team)
```
