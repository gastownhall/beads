---
title: CLI Reference
description: Generated reference for every bd command
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --docs-root`.

This reference covers all 112 live top-level `bd` commands. Regenerate it with:

```bash
./scripts/generate-cli-docs.sh
```

## Global Flags

These flags apply to all commands:

```
      --actor string              Actor name for audit trail (default: $BEADS_ACTOR, git user.name, $USER)
      --db string                 Database path (default: auto-discover .beads/*.db)
  -C, --directory string          Change to this directory before running the command (like git -C)
      --dolt-auto-commit string   Dolt auto-commit policy (off|on|batch). 'on': commit after each write. 'batch': defer commits to bd dolt commit; uncommitted changes persist in the working set until then. SIGTERM/SIGHUP flush pending batch commits. Default: off. Override via config key dolt.auto-commit
      --global                    Use the global shared-server database (beads_global)
      --ignore-schema-skew        Proceed despite forward schema drift (some queries may fail)
      --json                      Output in JSON format
      --no-color                  Disable color output (also: NO_COLOR=1 or CLICOLOR=0)
      --profile                   Generate CPU profile for performance analysis
  -q, --quiet                     Suppress non-essential output (errors only)
      --readonly                  Read-only mode: block write operations (for worker sandboxes)
      --sandbox                   Sandbox mode: disables Dolt auto-push
  -v, --verbose                   Enable verbose/debug output
```

## Commands

- [`bd admin`](/cli-reference/admin)
- [`bd ado`](/cli-reference/ado)
- [`bd assign`](/cli-reference/assign)
- [`bd audit`](/cli-reference/audit)
- [`bd backup`](/cli-reference/backup)
- [`bd batch`](/cli-reference/batch)
- [`bd blocked`](/cli-reference/blocked)
- [`bd bootstrap`](/cli-reference/bootstrap)
- [`bd branch`](/cli-reference/branch)
- [`bd children`](/cli-reference/children)
- [`bd close`](/cli-reference/close)
- [`bd comment`](/cli-reference/comment)
- [`bd comments`](/cli-reference/comments)
- [`bd compact`](/cli-reference/compact)
- [`bd completion`](/cli-reference/completion)
- [`bd config`](/cli-reference/config)
- [`bd context`](/cli-reference/context)
- [`bd cook`](/cli-reference/cook)
- [`bd count`](/cli-reference/count)
- [`bd create`](/cli-reference/create)
- [`bd create-form`](/cli-reference/create-form)
- [`bd defer`](/cli-reference/defer)
- [`bd delete`](/cli-reference/delete)
- [`bd dep`](/cli-reference/dep)
- [`bd diff`](/cli-reference/diff)
- [`bd doctor`](/cli-reference/doctor)
- [`bd dolt`](/cli-reference/dolt)
- [`bd duplicate`](/cli-reference/duplicate)
- [`bd duplicates`](/cli-reference/duplicates)
- [`bd edit`](/cli-reference/edit)
- [`bd epic`](/cli-reference/epic)
- [`bd export`](/cli-reference/export)
- [`bd federation`](/cli-reference/federation)
- [`bd find-duplicates`](/cli-reference/find-duplicates)
- [`bd flatten`](/cli-reference/flatten)
- [`bd forget`](/cli-reference/forget)
- [`bd formula`](/cli-reference/formula)
- [`bd gate`](/cli-reference/gate)
- [`bd gc`](/cli-reference/gc)
- [`bd github`](/cli-reference/github)
- [`bd gitlab`](/cli-reference/gitlab)
- [`bd graph`](/cli-reference/graph)
- [`bd heartbeat`](/cli-reference/heartbeat)
- [`bd history`](/cli-reference/history)
- [`bd hooks`](/cli-reference/hooks)
- [`bd human`](/cli-reference/human)
- [`bd import`](/cli-reference/import)
- [`bd info`](/cli-reference/info)
- [`bd init`](/cli-reference/init)
- [`bd init-safety`](/cli-reference/init-safety)
- [`bd jira`](/cli-reference/jira)
- [`bd kv`](/cli-reference/kv)
- [`bd label`](/cli-reference/label)
- [`bd linear`](/cli-reference/linear)
- [`bd link`](/cli-reference/link)
- [`bd lint`](/cli-reference/lint)
- [`bd list`](/cli-reference/list)
- [`bd mail`](/cli-reference/mail)
- [`bd memories`](/cli-reference/memories)
- [`bd merge-slot`](/cli-reference/merge-slot)
- [`bd metrics`](/cli-reference/metrics)
- [`bd migrate`](/cli-reference/migrate)
- [`bd migrate-personal`](/cli-reference/migrate-personal)
- [`bd mol`](/cli-reference/mol)
- [`bd note`](/cli-reference/note)
- [`bd notion`](/cli-reference/notion)
- [`bd onboard`](/cli-reference/onboard)
- [`bd orphans`](/cli-reference/orphans)
- [`bd ping`](/cli-reference/ping)
- [`bd preflight`](/cli-reference/preflight)
- [`bd prime`](/cli-reference/prime)
- [`bd priority`](/cli-reference/priority)
- [`bd promote`](/cli-reference/promote)
- [`bd prune`](/cli-reference/prune)
- [`bd purge`](/cli-reference/purge)
- [`bd q`](/cli-reference/q)
- [`bd query`](/cli-reference/query)
- [`bd quickstart`](/cli-reference/quickstart)
- [`bd ready`](/cli-reference/ready)
- [`bd recall`](/cli-reference/recall)
- [`bd reclaim`](/cli-reference/reclaim)
- [`bd recompute-blocked`](/cli-reference/recompute-blocked)
- [`bd remember`](/cli-reference/remember)
- [`bd rename`](/cli-reference/rename)
- [`bd rename-prefix`](/cli-reference/rename-prefix)
- [`bd reopen`](/cli-reference/reopen)
- [`bd repo`](/cli-reference/repo)
- [`bd restore`](/cli-reference/restore)
- [`bd rules`](/cli-reference/rules)
- [`bd search`](/cli-reference/search)
- [`bd set-state`](/cli-reference/set-state)
- [`bd setup`](/cli-reference/setup)
- [`bd ship`](/cli-reference/ship)
- [`bd show`](/cli-reference/show)
- [`bd sql`](/cli-reference/sql)
- [`bd stale`](/cli-reference/stale)
- [`bd state`](/cli-reference/state)
- [`bd status`](/cli-reference/status)
- [`bd statuses`](/cli-reference/statuses)
- [`bd supersede`](/cli-reference/supersede)
- [`bd swarm`](/cli-reference/swarm)
- [`bd tag`](/cli-reference/tag)
- [`bd todo`](/cli-reference/todo)
- [`bd types`](/cli-reference/types)
- [`bd unclaim`](/cli-reference/unclaim)
- [`bd undefer`](/cli-reference/undefer)
- [`bd update`](/cli-reference/update)
- [`bd upgrade`](/cli-reference/upgrade)
- [`bd vc`](/cli-reference/vc)
- [`bd version`](/cli-reference/version)
- [`bd where`](/cli-reference/where)
- [`bd worktree`](/cli-reference/worktree)
