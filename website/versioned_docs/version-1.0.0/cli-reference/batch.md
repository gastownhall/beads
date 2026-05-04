---
id: batch
title: bd batch
slug: /cli-reference/batch
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc batch`

## bd batch

Run multiple write operations in a single database transaction.

Commands are read from stdin (one per line) or from a file via -f/--file.
All operations execute inside a single dolt transaction: on any error the
whole batch is rolled back, otherwise it is committed with one DOLT_COMMIT.

This is intended for shell scripts that currently invoke 'bd' many times in
a loop, which causes severe write amplification on a dolt sql-server backed
by btrfs+compression. Batching collapses N invocations into one transaction
and one dolt commit.

Grammar (one command per line):
  close &lt;id&gt; [reason...]
  update &lt;id&gt; &lt;key&gt;=&lt;value&gt; [&lt;key&gt;=&lt;value&gt; ...]
  create &lt;type&gt; &lt;priority&gt; &lt;title...&gt;
  dep add &lt;from-id&gt; &lt;to-id&gt; [type]
  dep remove &lt;from-id&gt; &lt;to-id&gt;
  #comment  (blank lines and '# ...' comments are ignored)

Supported 'update' keys: status, priority, title, assignee
Supported dependency types: see 'bd dep add --help' (default: blocks)

Tokens are whitespace-separated. Double-quoted strings ("like this") may
contain spaces; use \" to embed a quote and \\ for a backslash.

Examples:
  # From a pipe
  bd list --status stale -q | awk '&#123;print "close",$1," stale"&#125;' | bd batch

  # From a file
  bd batch -f operations.txt

  # Inline
  printf 'close bd-1 done\nupdate bd-2 status=in_progress\n' | bd batch

On success, exits 0 and prints a summary (or JSON with --json). On any error,
rolls back the entire transaction and exits non-zero with the failing line.

NOTE: This is a narrow subset. Commands like 'show', 'list', 'ready', 'sync',
complex create flows, or any flag not listed above are NOT accepted. Use
normal 'bd' subcommands for interactive/read operations.

```
bd batch [flags]
```

**Flags:**

```
      --dry-run          Parse input and echo commands without executing
  -f, --file string      Read commands from file instead of stdin
  -m, --message string   DOLT_COMMIT message (default: 'bd: batch N ops by <actor>')
```
