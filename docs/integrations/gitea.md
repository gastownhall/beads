---
title: Gitea & Forgejo Integration Configuration
description: Configuration reference for bd gitea sync, which bidirectionally syncs beads issues with Gitea, Forgejo, Codeberg, and self-hosted instances
---

Last reviewed: 2026-11-26

Freshness source: `cmd/bd/gitea*.go` and `internal/gitea/`.

This guide covers configuration and usage for `bd gitea` (`sync`, `pull`, `push`, `status`, and `repos`), which bidirectionally synchronizes beads with Gitea and Forgejo repositories — including Codeberg (`https://codeberg.org`) and self-hosted instances.

**Proxied-server mode:** `bd gitea status`, `bd gitea repos`, and `bd gitea sync` are not supported when `bd` is connected to a proxied server (error: `gitea <command> is not supported in proxied-server mode`); run them from a workspace with direct database access. Selective `bd gitea pull <refs...>` and `bd gitea push <ids...>` remain permitted in proxied-server mode.

## Quick Start

```bash
# Configure instance URL, token (stored in config.yaml), and target repo
bd config set gitea.url "https://codeberg.org"
bd config set gitea.token "your-personal-access-token"
bd config set gitea.repo "owner/repo"

# Or use environment variables
export GITEA_URL="https://codeberg.org"
export GITEA_TOKEN="your-personal-access-token"
export GITEA_REPO="owner/repo"

# Verify configuration and accessible repositories
bd gitea status
bd gitea repos

# Sync bidirectionally (pull then push)
bd gitea sync

# Limit direction or preview changes
bd gitea sync --pull-only
bd gitea sync --push-only
bd gitea sync --dry-run

# Selective push/pull for specific beads or external refs
bd gitea push bd-a1b2
bd gitea pull 42
```

## Connection Configuration

| Config Key | Env Variable | Required | Description |
|---|---|---|---|
| `gitea.url` | `GITEA_URL` | Yes | Base URL of your Gitea or Forgejo instance (e.g. `https://codeberg.org`) |
| `gitea.token` | `GITEA_TOKEN` | Yes | Personal access token (stored in `config.yaml`, never the Dolt database) |
| `gitea.repo` | `GITEA_REPO` | Yes¹ | Repository in `owner/repo` format |
| `gitea.owner` | `GITEA_OWNER` | Conditional¹ | Repository owner when configuring `gitea.project` separately |
| `gitea.project` | `GITEA_PROJECT` | Conditional¹ | Repository name when `gitea.repo` omits the `owner/` prefix |
| `gitea.tls_ca` | `GITEA_TLS_CA` | No | Optional path to a PEM CA certificate bundle for self-signed TLS (user-global `~/.config/bd/config.yaml` or private shell env only) |

¹ Either `gitea.repo` in `owner/repo` form, or both `gitea.owner` and `gitea.project` (or `gitea.repo` as the repo name), must be set.

**Transport & endpoint security:**

- `gitea.url` must use `https://`. Plain `http://` is permitted only for loopback hosts (`localhost`, `127.0.0.1`, `::1`).
- Custom CA bundles (`gitea.tls_ca` / `GITEA_TLS_CA`) are opt-in and off by default; TLS certificate verification is never disabled. When configured, the PEM file replaces (rather than appends to) the system root pool for that Gitea/Forgejo client. Like endpoint confirmation, `gitea.tls_ca` is a private per-user setting read only from `~/.config/bd/config.yaml` (where `bd config set gitea.tls_ca` writes it) or a private shell environment variable (`GITEA_TLS_CA`) — values in the shared Dolt database, project `.beads/config.yaml`, or `.beads/.env` are ignored.
- `bd` pins the Gitea/Forgejo endpoint per workspace in `trusted-endpoints.json` (see [Configuration](/reference/configuration#precedence)). Because Gitea and Forgejo have no single vendor host, a workspace with a shared `gitea.url` requires private confirmation via `GITEA_URL` in your shell or `gitea.url` in `~/.config/bd/config.yaml` before any token is sent.
- `gitea.token` is routed exclusively to `config.yaml` and redacted from synced database tables so credentials cannot leak via `bd dolt push`.

## Token Scopes

| Operation | Minimum Token Scope |
|---|---|
| `bd gitea status`, `bd gitea repos` | `read:repository` / `read:user` |
| `bd gitea sync --pull-only`, `bd gitea pull` | `read:issue` |
| `bd gitea sync --push-only`, `bd gitea push`, `bd gitea sync` | `write:issue` |

## Filter Configuration

Filters restrict which remote issues are fetched during pull:

| Config Key | Env Variable | CLI Flag | Description |
|---|---|---|---|
| `gitea.filter_labels` | `GITEA_FILTER_LABELS` | `--label` | Comma-separated label names (AND logic) |
| `gitea.filter_milestone` | `GITEA_FILTER_MILESTONE` | `--milestone` | Filter by milestone title |
| `gitea.filter_assignee` | `GITEA_FILTER_ASSIGNEE` | `--assignee` | Filter by assignee username |

CLI flags override configured filters for that invocation.

## Issues vs Pull Requests

Gitea and Forgejo expose pull requests through the repository issues endpoint. `bd gitea` passes `type=issues` and additionally filters out any entry carrying pull-request metadata client-side, so pull requests are never imported as beads during issue sync.

## Dependencies & Relationship Sync

When pushing (`bd gitea sync` or `bd gitea push`), `bd` converges local `blocks` dependencies between synced issues into Gitea/Forgejo issue dependencies (`blocked_by`). When pulling, remote `blocked_by` relationships are imported as `blocks` dependencies.

If issue dependencies are disabled on the Gitea/Forgejo repository or instance (`403` or `404` from the dependencies endpoint), dependency sync logs a non-fatal warning and skips linking without failing the issue sync.

## Field & State Mapping

`bd gitea` maps beads priorities, types, and non-terminal statuses via scoped labels:

| Beads Field | Gitea/Forgejo Representation |
|---|---|
| Priority `0`–`4` | `priority::critical`, `priority::high`, `priority::medium`, `priority::low`, `priority::none` |
| Issue type | `type::bug`, `type::feature`, `type::task`, `type::epic`, `type::chore`, etc. |
| Status (`in_progress`, `blocked`, `deferred`) | `status::in_progress`, `status::blocked`, `status::deferred` |
| Status (`open`, `closed`) | Native issue state (`open` / `closed`) |

Missing labels are automatically created on the repository and resolved to numeric label IDs. When pushing a `closed` bead that has not yet been created remotely, `bd` creates the issue open and closes it in a follow-up update.

By default, remote `closed` issues map to beads status `closed`. To map specific closed labels (such as `wontfix`) to another status (such as `deferred`), configure `gitea.closed_state_map.<label>`:

```bash
bd config set gitea.closed_state_map.wontfix deferred
```

When a closed remote issue maps to a non-closed beads status via `gitea.closed_state_map.<label>`, `closed_at` is cleared on import so the bead remains valid, and subsequent pushes keep the remote issue `closed` while that label and mapped status remain paired.

## Sync Flags

| Flag | Description |
|---|---|
| `--dry-run` | Preview sync without modifying local or remote state |
| `--pull-only` | Only pull issues from Gitea/Forgejo |
| `--push-only` | Only push issues to Gitea/Forgejo |
| `--prefer-newer` | On conflict, keep the most recently updated version (default) |
| `--prefer-local` | On conflict, keep local beads version |
| `--prefer-gitea` | On conflict, keep remote Gitea/Forgejo version |
| `--issues` | Comma-separated bead IDs to sync selectively (mutually exclusive with `--parent`) |
| `--parent` | Limit push to a bead and its descendants (mutually exclusive with `--issues`) |
| `--type` | Only sync listed issue types (comma-separated) |
| `--exclude-type` | Exclude listed issue types (defaults to `molecule,message,event`) |
| `--no-ephemeral` | Exclude ephemeral/wisp beads from push (default `true`) |
| `--json` | Output structured JSON results |

See [Integrations](/integrations/index) and [Configuration](/reference/configuration) for workspace configuration details.
