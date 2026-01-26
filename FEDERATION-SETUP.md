# Federation Setup Guide

Federation enables peer-to-peer synchronization of beads databases between
multiple Gas Towns using Dolt remotes. Each town maintains its own database
while sharing work items with configured peers.

## Overview

Federation uses Dolt's distributed version control capabilities to sync issue
data between independent teams or locations. Key benefits:

- **Peer-to-peer**: No central server required; each town is autonomous
- **Database-native versioning**: Built on Dolt's version control, not file exports
- **Flexible infrastructure**: Works with DoltHub, S3, GCS, local paths, or SSH
- **Data sovereignty**: Configurable tiers for compliance (GDPR, regional laws)

## Prerequisites

1. **Dolt backend**: Federation requires the Dolt storage backend (not SQLite)
2. **Sync mode**: Must use `dolt-native` or `belt-and-suspenders` sync mode

## Configuration

### Enable Federation-Compatible Sync

Edit `.beads/config.yaml` or `~/.config/bd/config.yaml`:

```yaml
sync:
  mode: dolt-native                      # Required for federation

federation:
  remote: dolthub://myorg/beads          # Primary remote (optional)
  sovereignty: T2                        # Data sovereignty tier
```

Or via environment variables:

```bash
export BD_SYNC_MODE="dolt-native"
export BD_FEDERATION_REMOTE="dolthub://myorg/beads"
export BD_FEDERATION_SOVEREIGNTY="T2"
```

### Sync Modes

| Mode | Description | Federation Support |
|------|-------------|-------------------|
| `git-portable` | JSONL export to git (default) | No |
| `realtime` | JSONL on every change | No |
| `dolt-native` | Dolt remotes only | Yes |
| `belt-and-suspenders` | Dolt + JSONL backup | Yes |

### Data Sovereignty Tiers

| Tier | Description | Use Case |
|------|-------------|----------|
| T1 | No restrictions | Public data |
| T2 | Organization-level | Regional/company compliance |
| T3 | Pseudonymous | Identifiers removed |
| T4 | Anonymous | Maximum privacy |

## Adding Federation Peers

Use `bd federation add-peer` to register remote peers:

```bash
bd federation add-peer <name> <endpoint>
```

### Peer Name Rules

- Must start with a letter
- Alphanumeric, dash, and underscore only
- Reserved names not allowed: `origin`, `main`, `master`, `HEAD`

### Supported Endpoint Formats

| Format | Example | Description |
|--------|---------|-------------|
| DoltHub | `dolthub://org/repo` | DoltHub hosted repository |
| Google Cloud | `gs://bucket/path` | Google Cloud Storage |
| Amazon S3 | `s3://bucket/path` | Amazon S3 |
| Local | `file:///path/to/backup` | Local filesystem |
| HTTPS | `https://host/path` | HTTPS remote |
| SSH | `ssh://host/path` | SSH remote |
| Git SSH | `git@host:path` | Git SSH shorthand |

### Examples

```bash
# Add a staging environment on DoltHub
bd federation add-peer staging dolthub://myorg/staging-beads

# Add a cloud backup
bd federation add-peer backup gs://mybucket/beads-backup
bd federation add-peer backup-s3 s3://mybucket/beads-backup

# Add a local backup
bd federation add-peer local file:///home/user/beads-backup

# Add a partner organization
bd federation add-peer partner-town dolthub://partner-org/beads
```

### JSON Output

For scripting, use the `--json` flag:

```bash
bd --json federation add-peer staging dolthub://myorg/staging-beads
# {"name":"staging","endpoint":"dolthub://myorg/staging-beads","status":"added"}
```

### Verify Configuration

Check stored peers:

```bash
bd config list | grep federation.peers
```

## Architecture Notes

### How It Works

1. Each Gas Town has its own Dolt database
2. `add-peer` registers a Dolt remote (similar to `git remote add`)
3. Push/pull operations sync commits between peers
4. Conflict resolution follows configured strategy

### Multi-Repo Support

Issues track their `SourceSystem` to identify which federated system created
them. This enables proper attribution and trust chains across organizations.

### Connectivity

Remote connectivity is validated on first push/pull operation, not when adding
the peer. This allows configuring remotes before infrastructure is ready.

## Planned Features

The following operations have infrastructure support but are not yet exposed
as commands:

- `bd federation list-peers` - List configured peers
- `bd federation push <peer>` - Push to specific peer
- `bd federation pull <peer>` - Pull from specific peer
- `bd federation sync <peer>` - Bidirectional sync

## Troubleshooting

### "requires direct database access"

Federation commands require the Dolt backend. Ensure you're not running in
daemon mode for federation operations.

### "peer already exists"

A peer with that name is already configured. Use a different name or check
existing peers with `bd config list | grep federation.peers`.

### Invalid endpoint format

Ensure your endpoint matches one of the supported formats above. The scheme
must be one of: `dolthub://`, `gs://`, `s3://`, `file://`, `https://`, `ssh://`,
or git SSH format (`git@host:path`).

## Reference

- Configuration: See `docs/CONFIG.md` for all federation settings
- Source: `cmd/bd/federation.go`
- Storage interfaces: `internal/storage/versioned.go`
- Dolt implementation: `internal/storage/dolt/store.go`
