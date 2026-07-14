---
title: Federation Setup Guide
description: Configure peer-to-peer sync of beads databases across workspaces with Dolt remotes, sovereignty tiers, and topologies
---

Federation enables peer-to-peer synchronization of beads databases between
multiple workspaces using Dolt remotes. Each workspace maintains its own database
while sharing work items with configured peers.

## Overview

Federation uses Dolt's distributed version control capabilities to sync issue
data between independent teams or locations. Key benefits:

- **Peer-to-peer**: No central server required; each town is autonomous
- **Database-native versioning**: Built on Dolt's version control, not file exports
- **Flexible infrastructure**: Works with DoltHub, S3, GCS, local paths, or SSH
- **Data sovereignty**: Configurable tiers for compliance (GDPR, regional laws)

## Prerequisites

1. **Dolt backend**: Federation requires the Dolt storage backend (the only supported backend)

## Configuration

### Enable Federation-Compatible Sync

Edit `.beads/config.yaml` or `~/.config/bd/config.yaml`:

```yaml
federation:
  remote: dolthub://myorg/beads          # Primary remote (optional)
  sovereignty: T2                        # Data sovereignty tier
```

Or via environment variables:

```bash
export BD_FEDERATION_REMOTE="dolthub://myorg/beads"
export BD_FEDERATION_SOVEREIGNTY="T2"
```

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
- Maximum 64 characters

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

### Credentials

Peers configured with `--user` (and optionally `--password`, otherwise
prompted interactively) store SQL credentials AES-256 encrypted, locally.
Stored credentials are used automatically during sync:

```bash
bd federation add-peer town-gamma 192.168.1.100:3306/beads --user sync-bot
```

### JSON Output

For scripting, use the `--json` flag:

```bash
bd --json federation add-peer staging dolthub://myorg/staging-beads
# {"added":"staging","url":"dolthub://myorg/staging-beads","has_auth":false,"sovereignty":""}
```

### Verify Configuration

List configured peers:

```bash
bd federation list-peers
```

## Syncing with Peers

Use `bd federation sync` to pull from and push to peer towns, and
`bd federation status` to check sync state without transferring data.

```bash
# Sync with all peers
bd federation sync

# Sync with a specific peer
bd federation sync --peer town-beta

# Handle conflicts
bd federation sync --strategy theirs  # or 'ours'

# Check status (ahead/behind, reachability, conflicts)
bd federation status
bd federation status --peer town-beta
```

Without `--strategy`, a sync that hits merge conflicts pauses and reports the
conflicting tables for manual resolution instead of auto-resolving.

### Topologies

| Pattern | Description | Use Case |
|---------|-------------|----------|
| Hub-spoke | Central hub, satellites sync to hub | Team with central coordination |
| Mesh | All peers sync with each other | Decentralized collaboration |
| Hierarchical | Tree of hubs | Multi-team organizations |

## Architecture Notes

### How It Works

1. Each workspace has its own Dolt database
2. `add-peer` registers a Dolt remote (similar to `git remote add`)
3. `bd federation sync` pushes and pulls commits between peers
4. Conflict resolution follows the configured strategy

When run against a Dolt SQL server, federation uses two ports: MySQL (3306)
for multi-writer SQL access, and remotesapi (8080) for peer-to-peer
push/pull:

```
┌─────────────────┐         ┌─────────────────┐
│  Workspace A    │◄───────►│  Workspace B    │
│  dolt sql-server│  sync   │  dolt sql-server│
│  :3306 (sql)    │         │  :3306 (sql)    │
│  :8080 (remote) │         │  :8080 (remote) │
└─────────────────┘         └─────────────────┘
```

### Multi-Repo Support

Issues track their `SourceSystem` to identify which federated system created
them. This enables proper attribution and trust chains across organizations.

### Connectivity

Remote connectivity is validated on first push/pull operation, not when adding
the peer. This allows configuring remotes before infrastructure is ready.

## Planned Features

The following operation has infrastructure support but is not yet exposed as
a command:

- `bd federation push <peer>` / `bd federation pull <peer>` - single-direction
  sync with one peer. `bd federation sync` already covers the bidirectional
  case.

## Troubleshooting

### "requires direct database access"

Federation commands require the Dolt backend with direct database access. Ensure
you have the Dolt backend configured for federation operations.

### "peer already exists"

A peer with that name is already configured. Use a different name or check
existing peers with `bd federation list-peers`.

### Invalid endpoint format

Ensure your endpoint matches one of the supported formats above. The scheme
must be one of: `dolthub://`, `gs://`, `s3://`, `file://`, `https://`, `ssh://`,
or git SSH format (`git@host:path`).

### General health check

```bash
bd doctor --deep
```

## Reference

- Configuration: See [Configuration](/reference/configuration) for all federation settings
- Source: `cmd/bd/federation.go`
- Storage interfaces: `internal/storage/versioned.go`
- Dolt implementation: `internal/storage/dolt/store.go`
