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

### Git Workflow Security

- bd uses standard git operations (no custom protocols)
- Export/import operations read and write local files only
- No network communication except through git itself
- Git hooks (if used) run with your local user permissions

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

All dependencies are regularly updated. Run `go mod verify` to check integrity.

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
