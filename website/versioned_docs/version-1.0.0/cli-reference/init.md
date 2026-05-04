---
id: init
title: bd init
slug: /cli-reference/init
sidebar_position: 400
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc init`

## bd init

Initialize bd in the current directory by creating a .beads/ directory
and Dolt database. Optionally specify a custom issue prefix.

Dolt is the default (and only supported) storage backend. The legacy SQLite
backend has been removed. Use --backend=sqlite to see migration instructions.

Use --database to specify an existing server database name, overriding the
default prefix-based naming. This is useful when an external tool (e.g. an orchestrator)
has already created the database.

With --stealth: configures per-repository git settings for invisible beads usage:
  • .git/info/exclude to prevent beads files from being committed
  Perfect for personal use without affecting repo collaborators.
  To set up a specific AI tool, run: bd setup &lt;claude|cursor|aider|...&gt; --stealth

By default, beads uses an embedded Dolt engine (no external server needed).
Pass --server to use an external dolt sql-server instead. In server mode,
set connection details with --server-host, --server-port, and --server-user.
Password should be set via BEADS_DOLT_PASSWORD environment variable.

Auto-export is enabled by default. After every write command, bd exports
issues to .beads/issues.jsonl (throttled to once per 60s). This keeps
viewers (bv) and git-based workflows up to date without extra steps.
To disable: bd config set export.auto false

Non-interactive mode (--non-interactive or BD_NON_INTERACTIVE=1):
  Skips all interactive prompts, using sensible defaults:
  • Role defaults to "maintainer" (override with --role)
  • Fork exclude auto-configured when fork detected
  • Auto-export left at default (enabled)
  • --contributor and --team flags are rejected (wizards require interaction)
  Also auto-detected when stdin is not a terminal or CI=true is set.

```
bd init [flags]
```

**Flags:**

```
      --agents-file string       Custom filename for agent instructions (default: AGENTS.md)
      --agents-profile string    AGENTS.md profile: 'minimal' (default, pointer to bd prime) or 'full' (complete command reference)
      --agents-template string   Path to custom AGENTS.md template (overrides embedded default)
      --backend string           Storage backend (default: dolt). --backend=sqlite prints deprecation notice.
      --contributor              Run OSS contributor setup wizard
      --database string          Use existing server database name (overrides prefix-based naming)
      --destroy-token string     Explicit confirmation token for destructive re-init in non-interactive mode (format: 'DESTROY-<prefix>')
      --discard-remote           Authorize discarding the configured remote's Dolt history when re-initializing. Requires --destroy-token in non-interactive mode; see 'bd help init-safety'.
      --external                 Server is externally managed (skip server startup); use with --shared-server or --server
      --force                    Deprecated alias for --reinit-local. Bypasses only the LOCAL data-safety guard; does NOT authorize remote divergence (see 'bd help init-safety').
      --from-jsonl               Import issues from .beads/issues.jsonl instead of git history
      --non-interactive          Skip all interactive prompts (auto-detected in CI or non-TTY environments)
  -p, --prefix string            Issue prefix (default: current directory name)
  -q, --quiet                    Suppress output (quiet mode)
      --reinit-local             Re-initialize local .beads/ over existing local data. Does NOT authorize remote divergence; see --discard-remote.
      --remote string            Dolt remote URL to clone from and persist as sync.remote
      --role string              Set beads role without prompting: "maintainer" or "contributor"
      --server                   Use external dolt sql-server instead of embedded engine
      --server-host string       Dolt server host (default: 127.0.0.1)
      --server-port int          Dolt server port (default: 3307)
      --server-socket string     Unix domain socket path (overrides host/port)
      --server-user string       Dolt server MySQL user (default: root)
      --setup-exclude            Configure .git/info/exclude to keep beads files local (for forks)
      --shared-server            Enable shared Dolt server mode (all projects share one server at ~/.beads/shared-server/)
      --skip-agents              Skip AGENTS.md and Claude settings generation
      --skip-hooks               Skip git hooks installation
      --stealth                  Enable stealth mode: global gitattributes and gitignore, no local repo tracking
      --team                     Run team workflow setup wizard
```
