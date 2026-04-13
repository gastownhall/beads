# Federation Security

This document covers the credential security model for multi-remote federation
deployments. It assumes familiarity with [FEDERATION-SETUP.md](../FEDERATION-SETUP.md)
and [CONFIG.md](CONFIG.md).

## Remote Roles and Trust Model

Each `RemoteConfig` has a **role** that determines how bd treats it during sync:

| Role | Push | Pull | Trust Level |
|------|------|------|-------------|
| `primary` | First, fail-fast | Yes (authoritative) | Highest |
| `backup` | After primary succeeds | Never | Mirror only |
| `archive` | After primary succeeds | Never | Cold storage |

**Pull trust ordering**: bd only pulls from the `primary` remote. Backup and
archive remotes are push-only mirrors. A compromised backup cannot inject data
into your database — the `SyncOrchestrator` enforces this by calling
`PullFrom()` exclusively on the primary remote.

This is a deliberate security boundary: even if an attacker gains write access
to a backup remote, they cannot influence the data your workspace sees.

## Credential Isolation

### Per-Remote Credential Binding

Federation peers store credentials in the `federation_peers` table, encrypted
at rest (AES-256). Each peer has its own username/password pair:

```bash
# Each peer gets independent credentials
bd federation add-peer primary dolthub://org/beads
bd federation add-peer backup  s3://mybucket/beads-backup
bd federation add-peer archive az://account.blob.core.windows.net/archive
```

When bd pushes to a specific remote, `withPeerCredentials()` looks up that
peer's encrypted credentials and passes them to the Dolt subprocess via
`DOLT_REMOTE_USER` and `DOLT_REMOTE_PASSWORD` environment variables. Existing
values for these variables are stripped from the subprocess environment before
the peer-specific values are applied — credentials never leak between remotes.

### Cloud Auth Environment Variables

For cloud-hosted remotes (S3, GCS, Azure Blob), Dolt uses provider-specific
environment variables. The `shouldUseCLIForCloudAuth()` function checks for
these prefixes:

| Prefix | Provider | Common Variables |
|--------|----------|------------------|
| `AWS_` | Amazon S3 | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` |
| `AZURE_STORAGE_` | Azure Blob | `AZURE_STORAGE_ACCOUNT`, `AZURE_STORAGE_KEY`, `AZURE_STORAGE_SAS_TOKEN` |
| `GOOGLE_` | Google Cloud | `GOOGLE_APPLICATION_CREDENTIALS` |
| `GCS_` | GCS (alternate) | `GCS_CREDENTIALS_FILE` |
| `OCI_` | Oracle Cloud | `OCI_CONFIG_FILE` |
| `DOLT_REMOTE_` | Dolt-specific | `DOLT_REMOTE_USER`, `DOLT_REMOTE_PASSWORD` |

**Important limitation**: `shouldUseCLIForCloudAuth()` checks whether *any*
cloud auth variable is set — it does not distinguish per-remote. If you have
both `AWS_ACCESS_KEY_ID` and `AZURE_STORAGE_KEY` set in the same environment,
both are visible to all Dolt subprocesses. To isolate credentials between
cloud providers:

- Run pushes to different providers in separate environments (e.g., separate
  CI jobs or wrapper scripts that set only the relevant variables)
- Use IAM roles or workload identity federation instead of static keys where
  your provider supports it
- Use `DOLT_REMOTE_USER`/`DOLT_REMOTE_PASSWORD` via the peer credential store
  instead of provider-specific env vars when possible

### Credential Reuse Warning

Using the same credential across remotes with different trust levels is a
security risk. If your backup remote uses the same API key as your primary,
compromise of the backup effectively compromises primary access too.

**Best practice**: Use distinct credentials per remote, scoped to the minimum
required permissions:

- Primary: read/write access
- Backup: write-only (append) if your provider supports it
- Archive: write-only, ideally with immutability policies

If your deployment must reuse credentials, document the trust implications and
ensure all remotes sharing a credential have equivalent security controls.

## Credential Lifecycle

### Rotation

1. Generate new credentials with your cloud provider or DoltHub
2. Update the peer's stored credentials:
   ```bash
   bd federation add-peer <name> <url>  # Re-adding overwrites credentials
   ```
3. Verify connectivity: `bd dolt push` to the affected remote
4. Revoke the old credentials at the provider

For cloud auth env vars, update the variables in your CI/CD secrets or shell
profile, then restart any running bd server instances.

### Revocation

If a credential is compromised:

1. **Revoke immediately** at the provider (DoltHub, AWS IAM, Azure portal, GCP console)
2. Rotate to new credentials as above
3. Audit recent sync history: `bd dolt log` shows Dolt commit history including
   push timestamps
4. If the compromised credential was for a backup remote, verify primary data
   integrity — backups cannot inject data, but the attacker may have read data
   from the backup

### Audit Logging

bd does not maintain a separate credential audit log. Use these sources for
audit evidence:

- **Dolt commit history**: Every push creates a Dolt commit with timestamp and
  author metadata (`bd dolt log`)
- **`federation_peers.last_sync`**: Updated on each successful peer operation
- **Cloud provider audit logs**: AWS CloudTrail, Azure Activity Log, GCP Cloud
  Audit Logs — these record all API calls made with your credentials
- **bd server logs**: If running in server mode, connection events are logged
  (credentials are never included — see Log Sanitization below)

## Partial Push Failure

The `SyncOrchestrator` pushes remotes in a defined order with specific failure
semantics:

1. **Primary is pushed first**. If primary fails, all backup/archive pushes are
   **skipped** (`PushStatusSkipped`). No data reaches any remote.

2. **If primary succeeds**, backups are pushed sequentially. Each backup failure
   is independent — one backup failing does not stop other backups.

3. **Degraded sync** (`ErrDegradedSync`): Primary succeeded but one or more
   backups failed. In this state:
   - Primary has the latest data (authoritative, consistent)
   - Some backups may be stale (missing the latest push)
   - No backup has *partial* data — Dolt pushes are atomic per-remote

**Security implications of degraded sync**:

- An attacker observing a backup remote sees either the previous complete state
  or the new complete state, never a partial write
- Stale backups are a durability concern, not a confidentiality one — the data
  on stale backups is a subset of what primary already has
- Retry logic uses exponential backoff (500ms initial, 3 retries by default)
  with transient error detection before marking a push as failed

## Log Sanitization

bd follows a strict policy of never logging sensitive material:

- **Credentials**: `DOLT_REMOTE_USER`, `DOLT_REMOTE_PASSWORD`, cloud API keys,
  SAS tokens, and encrypted peer passwords are never written to logs or error
  messages
- **Connection strings**: Remote URLs may contain embedded credentials (e.g.,
  `https://user:pass@host/path`). Log messages use the remote **name** (e.g.,
  `"pushing to backup"`) rather than the full URL
- **Subprocess environment**: Credentials are set on the subprocess `cmd.Env`
  only, not on the parent process environment (except when using
  `withEnvCredentials()`, which holds a mutex and restores the original values
  immediately after the operation)

When troubleshooting federation issues, use remote names in bug reports and
support requests — never paste connection strings or environment variable values.

## URL Validation and Injection Prevention

Remote URLs are validated before use to prevent injection attacks:

- **Control characters rejected**: Any byte in the range 0x00–0x1f or 0x7f
  causes validation failure (prevents terminal escape injection and null-byte
  attacks)
- **Leading dash rejected**: URLs starting with `-` are blocked to prevent
  CLI flag injection when URLs are passed as subprocess arguments
- **Scheme allowlist**: Only known schemes are accepted: `dolthub://`, `gs://`,
  `s3://`, `az://`, `file://`, `https://`, `http://`, `ssh://`, `git+ssh://`,
  `git+https://`
- **SCP-style validation**: Git SSH shorthand (`git@host:path`) is validated
  against a strict regex pattern

### Enterprise URL Restrictions

The `federation.allowed-remote-patterns` config key accepts a list of glob
patterns that restrict which remote URLs are permitted:

```yaml
# .beads/config.yaml
federation:
  allowed-remote-patterns:
    - "dolthub://myorg/*"
    - "s3://mycompany-*/**"
```

When set, any remote URL that does not match at least one pattern is rejected.
This prevents users or agents from adding unauthorized remotes. Uses Go's
`path.Match` glob semantics.

## Subprocess Credential Isolation

Credentials are passed to Dolt subprocesses using two mechanisms, both designed
to minimize exposure:

1. **`applyToCmd()`** (preferred): Builds a clean environment for the subprocess,
   stripping any pre-existing `DOLT_REMOTE_USER`/`DOLT_REMOTE_PASSWORD` values
   and injecting only the peer-specific credentials. The parent process
   environment is never modified.

2. **`withEnvCredentials()`** (fallback): Temporarily sets credentials in the
   parent process environment, protected by `federationEnvMutex`. The original
   values are restored immediately after the operation completes. This is used
   when the Dolt API requires process-wide environment variables rather than
   subprocess configuration.

In both cases, credentials exist in memory only for the duration of the
operation and are never written to disk outside the encrypted `federation_peers`
table.

## Summary of Security Properties

| Property | Guarantee |
|----------|-----------|
| Pull source | Primary only — backups cannot inject data |
| Credential storage | AES-256 encrypted in `federation_peers` table |
| Credential passing | Subprocess env only, stripped after use |
| Push atomicity | Per-remote atomic; no partial writes visible |
| URL validation | Control chars, leading dashes, unknown schemes all rejected |
| Log hygiene | Credentials, connection strings, SAS tokens never logged |
| Enterprise lockdown | `federation.allowed-remote-patterns` restricts URLs |
| Peer names | Validated: alphanumeric + hyphens/underscores, max 64 chars |
