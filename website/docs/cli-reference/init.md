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

Auto-export is optional. When enabled, bd exports issues to
.beads/issues.jsonl after write commands (throttled to once per 60s). This is
for viewers (bv), interchange, and issue-level migration; not backup.
Cross-machine sync and backups use Dolt remotes/backups, not JSONL import/export.
To enable: bd config set export.auto true

Non-interactive mode (--non-interactive or BD_NON_INTERACTIVE=1):
  Skips all interactive prompts, using sensible defaults:
  • Role defaults to "maintainer" (override with --role)
  • Fork exclude auto-configured when fork detected
  • Auto-export left at default (disabled)
  • --contributor and --team flags are rejected (wizards require interaction)
  Also auto-detected when stdin is not a terminal or CI=true is set.

```
bd init [flags]
```

**Flags:**

```
      --agents-file string                             Custom filename for agent instructions (default: AGENTS.md)
      --agents-profile string                          AGENTS.md profile: 'minimal' (default, pointer to bd prime) or 'full' (complete command reference)
      --agents-template string                         Path to custom AGENTS.md template (overrides embedded default)
      --backend string                                 Storage backend (default: dolt). --backend=sqlite prints deprecation notice.
      --contributor                                    Run OSS contributor setup wizard
      --database string                                Use existing server database name (overrides prefix-based naming)
      --debug                                          Run the managed Dolt sql-server with --loglevel=debug and CPU profiling (--prof cpu). Persisted to config.yaml as dolt.debug. No effect on externally-managed servers.
      --destroy-token string                           Explicit confirmation token for destructive re-init in non-interactive mode (format: 'DESTROY-<prefix>')
      --discard-remote                                 Authorize discarding the configured remote's Dolt history when re-initializing. Requires --destroy-token in non-interactive mode; see 'bd help init-safety'.
      --external                                       Server is externally managed (skip server startup); use with --shared-server or --server
      --force                                          Deprecated alias for --reinit-local. Bypasses only the LOCAL data-safety guard; does NOT authorize remote divergence (see 'bd help init-safety').
      --from-jsonl                                     Import issues from configured import.path instead of git history
      --init-if-missing                                If the workspace is already initialized, skip init and exit 0 instead of failing (idempotent init for scaffolds)
      --non-interactive                                Skip all interactive prompts (auto-detected in CI or non-TTY environments)
  -p, --prefix string                                  Issue prefix (default: current directory name)
      --proxied-server                                 [EXPERIMENTAL] Use a per-workspace proxied dolt sql-server (proxy + child dolt) rooted at .beads/proxieddb
      --proxied-server-config-path string              [EXPERIMENTAL] Absolute path to an existing dolt sql-server YAML config (proxied-server mode only). When set, bd uses this file instead of auto-generating one. Relative paths are rejected.
      --proxied-server-external-host string            [EXPERIMENTAL] Hostname or IP of an externally-managed dolt sql-server the proxy should front (proxied-server mode only). Mutually exclusive with --proxied-server-external-socket-path.
      --proxied-server-external-keep-alive duration    [EXPERIMENTAL] TCP keepalive period for the proxy→external connection. Zero uses the package default (30s).
      --proxied-server-external-port int               [EXPERIMENTAL] TCP port of the externally-managed dolt sql-server (proxied-server mode only). Required when --proxied-server-external-host is set.
      --proxied-server-external-socket-path string     [EXPERIMENTAL] Absolute unix socket path of the externally-managed dolt sql-server (proxied-server mode only). Mutually exclusive with --proxied-server-external-host. Relative paths are rejected.
      --proxied-server-external-tls                    [EXPERIMENTAL] Require TLS when connecting to the externally-managed dolt sql-server (proxied-server mode only).
      --proxied-server-external-tls-cert-path string   [EXPERIMENTAL] Absolute path to a client TLS certificate (for mTLS to the externally-managed dolt sql-server). Must be paired with --proxied-server-external-tls-key-path. Relative paths are rejected.
      --proxied-server-external-tls-key-path string    [EXPERIMENTAL] Absolute path to the client TLS private key (for mTLS to the externally-managed dolt sql-server). Must be paired with --proxied-server-external-tls-cert-path. Relative paths are rejected.
      --proxied-server-external-user string            [EXPERIMENTAL] MySQL user for the externally-managed dolt sql-server (proxied-server mode only). Defaults to "root" when empty. Password is read at runtime from $BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD and is never persisted to disk.
      --proxied-server-log-path string                 [EXPERIMENTAL] Absolute path to the proxied dolt sql-server log file (proxied-server mode only). Default: <beadsDir>/proxieddb/server.log. Relative paths are rejected.
      --proxied-server-root-path string                [EXPERIMENTAL] Absolute directory holding the proxied dolt sql-server's lockfiles, pidfiles, and child .dolt repository (proxied-server mode only). Default: <beadsDir>/proxieddb. May not exist yet — bd will create it. Relative paths are rejected.
  -q, --quiet                                          Suppress output (quiet mode)
      --reinit-local                                   Re-initialize local .beads/ over existing local data. Does NOT authorize remote divergence; see --discard-remote.
      --remote string                                  Dolt remote URL to clone from and persist as sync.remote
      --role string                                    Set beads role without prompting: "maintainer" or "contributor"
      --server                                         Use external dolt sql-server instead of embedded engine
      --server-host string                             Dolt server host (default: 127.0.0.1)
      --server-port int                                Dolt server port (default: 3307)
      --server-socket string                           Unix domain socket path (overrides host/port)
      --server-user string                             Dolt server MySQL user (default: root)
      --setup-exclude                                  Configure .git/info/exclude to keep beads files local (for forks)
      --shared-server                                  Enable shared Dolt server mode (all projects share one server at ~/.beads/shared-server/)
      --skip-agents                                    Skip AGENTS.md and Claude/Codex setup generation
      --skip-hooks                                     Skip git hooks installation
      --stealth                                        Enable stealth mode: global gitattributes and gitignore, no local repo tracking
      --team                                           Run team workflow setup wizard
```
