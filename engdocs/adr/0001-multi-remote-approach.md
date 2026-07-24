# ADR-0001: Multi-Remote Approach

## Status

Accepted

## Date

2026-04-07

## Decision Drivers

- **Backup redundancy**: Push to primary (DoltHub) + backup (Azure Blob Storage)
  simultaneously for disaster recovery.
- **Data sovereignty**: Route data to region-specific remotes for compliance.
- **Hybrid sync**: Push to DoltHub for collaboration + Azure for enterprise backup.
- **Incremental risk**: Avoid big-bang changes to a working federation system.

## Context

Beads federation currently supports a single remote for push/pull sync:

- `federation.remote` in `config.yaml` is a single URL string.
- `FederationConfig` has one `Remote string` field.
- `DoltStore.remote` is a single string, used by `Push()` and `Pull()`.
- `bd dolt push` / `bd dolt pull` have no `--remote` argument.

Dolt natively supports multiple remotes (like git): `dolt remote add backup az://...`
and `CALL DOLT_PUSH('backup', 'main')` work out of the box. The gap is in the beads
layer, not in Dolt itself.

A spike (bd-qky) investigated four approaches. A council review produced 49 findings
with a "Request Changes" verdict, leading to this phased decision.

## Decision

**Phased implementation**: start with Approach C (Dolt-native `--remote` flag) as a
tracer bullet, then evolve to Approach A (config-managed additional remotes).

### Phase 1 — Tracer Bullet (Approach C)

Expose Dolt's native multi-remote capability through the beads CLI:

- Add `--remote <name>` flag to `bd dolt push`.
- Users manage additional remotes manually via `bd dolt remote add <name> <url>`.
- No config changes. No new orchestration layer.
- Pull remains single-remote only (primary / `origin`). Pulling from non-primary
  remotes is not supported — mirrors are push-only.
- Credential routing relies on ambient environment variables (the user sets
  `AZURE_STORAGE_ACCOUNT`, AWS creds, etc. before invoking).

This validates the multi-remote workflow with minimal code and risk.

### Phase 2 — Target Architecture (Approach A)

Add config-managed additional remotes:

- Keep `federation.remote` as the primary (backwards compatible).
- Add `federation.additional-remotes` as an ordered list of named remotes:
  ```yaml
  federation:
    remote: "dolthub://org/beads"     # primary, Dolt remote name: "origin"
    additional-remotes:
      - name: backup
        url: "az://account.blob.core.windows.net/container/path"
      - name: archive
        url: "gs://bucket/path"
  ```
- List ordering defines push order (first entry pushed first after primary).
- Remote entries are extensible objects (not bare URL strings) to allow future
  per-remote configuration (credentials, roles, filters).
- The primary remote always uses the Dolt remote name `origin` (matching
  existing drift/apply behavior). Additional remotes use their `name` field
  as the Dolt remote name.
- Introduce a **SyncOrchestrator** component (SRP) to coordinate multi-remote
  push operations.
- Integrate with existing drift/apply infrastructure for remote consistency.

## Considered Alternatives

### Approach A: Config-driven multi-remote — *selected as target (Phase 2)*

Keep `federation.remote` as primary, add `federation.additional-remotes` map.

**Pros**: Backwards compatible. Clear primary vs. backup distinction. Config-drift
manageable via existing drift/apply infrastructure. Extensible object values support
future per-remote credential configuration. Ordered list guarantees deterministic
push sequence.

**Cons**: Two config patterns to maintain (`remote` + `additional-remotes`). Requires
SyncOrchestrator for coordination. More code than Phase 1.

### Approach B: Remote list with roles — *rejected*

Replace `federation.remote` with a list of remotes, each with name/url/role
(primary/backup/archive).

**Rejected because**:
- **Breaking config change** — every existing `config.yaml` would need migration.
  Migration risk is high for a tool used in CI pipelines and team workflows.
- **Security harder to reason about** — a flat list makes it less obvious which
  remote is authoritative. Role annotations help but add complexity.
- **No incremental path** — requires full implementation before any value is
  delivered.

### Approach C: Dolt-native `--remote` flag — *selected as first step (Phase 1)*

Expose Dolt's native multi-remote via a CLI flag on `bd dolt push`/`bd dolt pull`.

**Pros**: Minimal code changes. Leverages Dolt's existing multi-remote support.
Fast feedback loop — validates workflow assumptions before investing in config
management.

**Cons**: Manual remote management (no config-driven setup). Drift/apply cannot
manage additional remotes. No orchestrated multi-push.

### Approach D: Push hooks / middleware — *rejected*

Post-push hook triggers additional pushes to backup remotes.

**Rejected because**:
- **Complex error handling** — hook failures are hard to surface and retry.
- **Non-blocking semantics unsuitable for sync** — the caller needs to know
  whether the backup push succeeded. Fire-and-forget is wrong for data
  replication.
- **Layering violation** — sync responsibility should live in the storage layer,
  not in a hook system.

## Design Principles

### Pull authority

The primary remote (`federation.remote`) is always authoritative for pulls.
Additional remotes are **push-only mirrors**.

**Rationale**:
- Backup remotes may be stale due to partial push failures.
- Mirrors must not diverge independently; pulling from mirrors creates
  split-brain ambiguity.
- A single source of truth simplifies conflict resolution.

**Disaster recovery**: If the primary remote is permanently lost, an operator
manually promotes a mirror by updating `federation.remote` in `config.yaml`
to point to the mirror URL. This is an explicit, auditable action — not
automatic failover.

### Push semantics

- **Sequential push**: Primary (`origin`) first, then additional remotes in
  list order. This gives clear error semantics — primary success is the
  minimum bar.
- **Partial failure**: If primary succeeds but a backup fails, the command
  reports success with warnings. The operator is responsible for retrying
  the failed backup push (e.g., `bd dolt push --remote backup`). The
  exit code reflects primary success (0), with diagnostic output for
  backup failures. A future `--strict` mode could fail on any mirror
  push failure for CI pipelines that require confirmed redundancy.
- **Phase 2 default behavior**: Plain `bd dolt push` (no `--remote` flag)
  pushes to primary and all configured additional remotes. The `--remote`
  flag targets a single remote.

### Credential routing

Phase 1 relies on ambient environment variables — the user sets the appropriate
credentials before invoking `bd dolt push --remote <name>`. This matches how
Dolt itself handles credentials.

Phase 2 may introduce per-remote credential configuration within the
`additional-remotes` object, but this is not yet decided. The extensible object
format (`url` + future fields) keeps this door open.

The existing `shouldUseCLIForCloudAuth()` check — which tests for ANY cloud
environment variable — will need per-remote refinement in Phase 2 to route
credentials correctly per transport protocol.

### SyncOrchestrator (Phase 2)

A dedicated `SyncOrchestrator` component owns multi-remote coordination:
- Iterates configured remotes in order.
- Handles per-remote push with appropriate credential routing.
- Aggregates results (success/warning/failure).
- Single Responsibility Principle — keeps `DoltStore` focused on single-remote
  operations.

## Out of Scope

### Selective filtering

Routing subsets of issues to specific remotes based on metadata (e.g., push
only priority-0 issues to a compliance remote) requires a fundamentally
different architecture — application-level filtering at the Dolt row/branch
level rather than remote-level push. This is **descoped to a separate future
spike**.

### Automatic failover

No automatic primary-to-mirror promotion. Failover is an explicit operator
action (see "Pull authority" above).

### Parallel push

Phase 2 uses sequential push for simplicity and clear error ordering. Parallel
push is a potential future optimization but adds complexity to error aggregation
and credential isolation.

## Consequences

### Positive

- **Incremental delivery**: Phase 1 provides working multi-remote push with
  minimal code and risk. Teams can start using Azure backup immediately.
- **Backwards compatible**: Existing `federation.remote` configs are unchanged.
  No migration required.
- **Validated learning**: Phase 1 validates multi-remote workflow assumptions
  before investing in config management and orchestration.
- **Extensible config**: Object-valued remotes in Phase 2 allow adding
  per-remote credentials, roles, and filters without another config migration.

### Negative

- **Phase 1 is manual**: Users must run `bd dolt remote add` and manage
  credentials via environment variables. No config-driven setup.
- **Temporary inconsistency**: Between Phase 1 and Phase 2, remotes are managed
  two ways (manual Dolt remotes vs. config-managed). Migration path from
  Phase 1 manual remotes to Phase 2 config-managed remotes needs to be defined
  during Phase 2 implementation.
- **Selective filtering deferred**: Cannot route subsets of issues to specific
  remotes until a separate spike explores row/branch-level filtering.
- **Credential routing deferred**: Per-remote credential configuration is
  punted to Phase 2, limiting Phase 1 to environments where ambient
  credentials cover all remotes.

## References

- Spike investigation: bd-qky
- Council review: session council-20260407-101917 (49 findings, "Request Changes")
- Dolt multi-remote docs: `dolt remote add`, `CALL DOLT_PUSH('remote', 'branch')`
- Related config: `internal/config/config.go` (`FederationConfig`)
- Related storage: `internal/storage/dolt/store.go` (`DoltStore.Push`, `DoltStore.Pull`)
