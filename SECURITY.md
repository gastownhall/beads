# Security Policy

## Reporting Security Issues

If you discover a security vulnerability in bd, please report it responsibly:

**Email**: security@steveyegge.com (or open a private security advisory on GitHub)

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

We will respond within 48 hours and work with you to address the issue.

## Security Considerations

### Database Security

bd stores issue data locally in a Dolt database (`.beads/dolt/`), which is gitignored.

**Important**:
- Do not store sensitive information (passwords, API keys, secrets) in issue descriptions or metadata
- Issue data is committed to git and will be visible to anyone with repository access
- bd does not encrypt data at rest (it's a local development tool)
- The `.beads/` directory contains server state files (PID, port) and should have restrictive permissions (0700) to prevent other local users from tampering with process lifecycle

### Git Workflow Security

- bd uses standard git operations (no custom protocols)
- Export/import operations read and write local files only
- No network communication except through git and the Dolt dependency (see Network & Privacy below)
- Git hooks (if used) run with your local user permissions

### Network & Privacy

Beads is local-first — the beads codebase itself contains no telemetry,
analytics, or outbound network calls.

However, the **Dolt** database engine (a beads dependency) collects usage
metrics by default, contacting `doltremoteapi.dolthub.com` even when no
remotes are configured.

To disable Dolt metrics collection, use either method:

```sh
# Method 1: Dolt config (persistent)
dolt config --global --add metrics.disabled true

# Method 2: Environment variable (per-session or export in shell profile)
export DOLT_DISABLE_EVENT_FLUSH=1
```

To verify, block `doltremoteapi.dolthub.com` in your firewall or DNS — beads
continues working normally with no degradation.

### Tracker Integration Trust Model

When syncing with external trackers (GitHub Issues, Jira, Linear, GitLab, Azure DevOps), all data crossing the integration boundary is treated as **untrusted input**.

**Trust boundaries:**
- Issue titles and descriptions from external trackers may contain arbitrary content, including ANSI escape sequences, control characters, or prompt injection payloads targeting AI agents
- External content is sanitized before terminal display (ANSI stripping, control character removal)
- API responses are size-limited to prevent out-of-memory conditions from malformed responses
- External issue identifiers are validated before use in SQL queries

**Credential handling:**
- Tracker API tokens stored in beads config (`bd config set`) are **plaintext** in the Dolt database
- Prefer platform-native authentication when available (`gh auth`, `glab auth`, Azure CLI) — these use the platform's secure credential store
- Never store tokens in environment variables in shared environments
- Tokens are scoped to the permissions you grant — use minimal required scopes

**Sync security model:**
- Sync is always **user-initiated** — no background daemons, no inbound webhooks, no listening ports
- No data is sent to external trackers unless the user explicitly runs `bd push` or `bd sync`
- Conflict resolution strategies are deterministic and auditable via Dolt history

**Content safety for AI agents:**
- Issue descriptions imported from external trackers may contain prompt injection payloads
- Consuming agents should treat all issue content as untrusted input
- The `--json` output flag provides structured data that separates metadata from free-text content
- beads does not execute or interpret issue content — it is stored and displayed only

### Command Injection Protection

bd uses parameterized SQL queries to prevent SQL injection. However:
- Do not pass untrusted input directly to `bd` commands
- Issue IDs are validated against the pattern `^[a-z0-9-]+$`
- File paths are validated before reading/writing

### Dependency Security

bd has minimal dependencies:
- Go standard library
- Dolt (version-controlled SQL database)
- Cobra CLI framework

All dependencies are pinned via `go.sum` and verified with `go mod verify`. Renovate (or Dependabot) monitors for known vulnerabilities. Run `go mod verify` locally to check integrity.

## Supported Versions

We provide security updates for:

| Version | Supported          |
| ------- | ------------------ |
| main    | :white_check_mark: |
| < 1.0   | :x:                |

Once version 1.0 is released, we will support the latest major version and one previous major version.

## Best Practices

1. **Don't commit secrets** - Never put API keys, passwords, or credentials in issue descriptions
2. **Review before sharing** - Check issue content before sharing project details
3. **Use private repos** - If your issues contain proprietary information, use private git repositories
4. **Validate git hooks** - If using automated export/import hooks, review them for safety
5. **Regular updates** - Keep bd updated to the latest version: `go install github.com/steveyegge/beads/cmd/bd@latest`

## Known Limitations

- bd is designed for **development/internal use**, not production secret management
- Issue data is stored in plain text in the Dolt database
- No built-in encryption or access control (relies on filesystem permissions)
- No audit logging beyond git history

For sensitive workflows, consider using bd only for non-sensitive task tracking.

## Security Updates

Security updates will be announced via:
- GitHub Security Advisories
- Release notes on GitHub
- Git commit messages (tagged with `[security]`)

Subscribe to the repository for notifications.
