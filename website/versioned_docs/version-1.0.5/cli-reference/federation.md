---
id: federation
title: bd federation
slug: /cli-reference/federation
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc federation`

## bd federation

Manage peer-to-peer federation between Dolt-backed beads databases.

Federation enables synchronized issue tracking across multiple workspaces,
each maintaining their own Dolt database while sharing updates via remotes.

Requires the Dolt storage backend.

```
bd federation
```

### bd federation add-peer

Add a new federation peer remote with optional SQL user authentication.

The URL can be:
  - dolthub://org/repo      DoltHub hosted repository
  - host:port/database      Direct dolt sql-server connection
  - file:///path/to/repo    Local file path (for testing)

Credentials are encrypted and stored locally. They are used automatically
when syncing with the peer. If --user is provided without --password,
you will be prompted for the password interactively.

Examples:
  bd federation add-peer town-beta dolthub://acme/town-beta-beads
  bd federation add-peer town-gamma 192.168.1.100:3306/beads --user sync-bot
  bd federation add-peer partner https://partner.example.com/beads --user admin --password secret

```
bd federation add-peer <name> <url> [flags]
```

**Flags:**

```
  -p, --password string      SQL password (prompted if --user set without --password)
      --sovereignty string   Sovereignty tier (T1, T2, T3, T4)
  -u, --user string          SQL username for authentication
```

### bd federation list-peers

List configured federation peers

```
bd federation list-peers
```

### bd federation remove-peer

Remove a federation peer

```
bd federation remove-peer <name>
```

### bd federation status

Show synchronization status with peer towns.

Displays:
  - Configured peers and their URLs
  - Commits ahead/behind each peer
  - Whether there are unresolved conflicts

Examples:
  bd federation status                    # Status for all peers
  bd federation status --peer town-beta   # Status for specific peer

```
bd federation status [--peer name] [flags]
```

**Flags:**

```
      --peer string   Specific peer to check
```

### bd federation sync

Pull from and push to peer towns.

Without --peer, syncs with all configured peers.
With --peer, syncs only with the specified peer.

Handles merge conflicts using the configured strategy:
  --strategy ours    Keep local changes on conflict
  --strategy theirs  Accept remote changes on conflict

If no strategy is specified and conflicts occur, the sync will pause
and report which tables have conflicts for manual resolution.

Examples:
  bd federation sync                      # Sync with all peers
  bd federation sync --peer town-beta     # Sync with specific peer
  bd federation sync --strategy theirs    # Auto-resolve using remote values

```
bd federation sync [--peer name] [flags]
```

**Flags:**

```
      --peer string       Specific peer to sync with
      --strategy string   Conflict resolution strategy (ours|theirs)
```
