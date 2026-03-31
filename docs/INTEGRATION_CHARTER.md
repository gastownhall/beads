# Integration Charter

This document defines the scope boundary for beads tracker integrations. It exists to keep the project focused on its core value — dependency-aware issue tracking for AI agents — and to prevent scope creep into platform territory.

## Core Principle

Tracker integrations are an **adoption bridge**, not a product. They exist to lower the barrier to entry for teams already using GitHub Issues, Jira, Linear, GitLab, or Azure DevOps. The goal is to make it easy to try beads alongside an existing tracker, not to replace that tracker's UI or workflow.

## What Beads Will Maintain

### Bidirectional Sync (Polled)

- **Issue metadata**: title, status, assignee, priority, labels
- **Dependency relationships**: mapped to beads' native dependency graph
- **Conflict resolution**: deterministic strategies (last-write-wins, beads-wins, tracker-wins)
- **Configurable sync intervals**: polled on user-initiated or scheduled cadence

### One-Way Import

- Bulk import from any supported tracker to populate the dependency graph
- Useful for initial adoption or one-time migration

### Reliable Error Handling

- Retry with exponential backoff and jitter for transient failures
- Response size limits to prevent OOM from malformed API responses
- Context cancellation support for graceful shutdown
- Pagination guards to prevent infinite loops
- Terminal sanitization for external content display

## What Beads Will NOT Build

These are explicitly out of scope. If a feature falls into this category, it should be rejected or redirected to an external tool.

### Webhook Gateways / Real-Time Event Systems

Beads does not need sub-second sync. Polled sync on a reasonable interval (minutes to hours) is sufficient for its use case. Webhooks add operational complexity (public endpoints, authentication, retry queues) that is disproportionate to the value they provide.

### Cross-Tracker Orchestration

Routing issues between trackers (e.g., "when a Jira issue is labeled X, create a GitHub issue") is workflow automation, not issue tracking. Tools like Zapier, n8n, or GitHub Actions are better suited for this.

### Attachment / Binary Content Sync

Syncing file attachments across platforms introduces storage management, content-type handling, and size limit mismatches. This is a CDN problem, not an issue tracker problem.

### Full Comment / Thread Mirroring

Comment threads are tightly coupled to each platform's UI and notification systems. Mirroring them creates confusing duplicate notifications and attribution problems. Beads syncs issue metadata, not conversation history.

### Credential Vault / Multi-Platform Token Aggregation

Each tracker integration uses a single API token configured by the user. Beads does not aggregate, rotate, or vault credentials across platforms. Users manage their own tokens through their platform's standard mechanisms.

### UI Parity Features

Beads will not replicate a tracker's web UI features (dashboards, burndown charts, sprint boards). The CLI and JSON output are the interface. If a team needs rich visualization, they should use their tracker's native UI alongside beads.

## Design Guidelines for New Integrations

When adding support for a new tracker or extending an existing one:

1. **Follow the existing pattern** — See `internal/github/`, `internal/jira/`, etc. Each tracker implements the same interface with consistent retry, pagination, and error handling.

2. **Map to beads concepts** — Translate the tracker's data model into beads' core types (Issue, Dependency, Status). Don't import tracker-specific concepts that don't map cleanly.

3. **Fail loudly** — Surface warnings and errors in `SyncStats`. Never silently swallow a failure that could lead to data inconsistency.

4. **Respect rate limits** — Honor `Retry-After` headers, implement backoff with jitter, and document the tracker's rate limit policy.

5. **Test with mocks** — Integration tests should use mock HTTP responses, not live API calls. This keeps CI fast and avoids token management in CI environments.

6. **Document the mapping** — Each integration should document how tracker statuses, priorities, and fields map to beads equivalents.

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-03-24 | Integrations are adoption bridge, not product | Prevent scope creep; focus on dependency graph as differentiator |
| 2026-03-24 | No webhooks, ever | Operational complexity disproportionate to value for polled use case |
| 2026-03-24 | No cross-tracker orchestration | Workflow automation is a different problem domain |
| 2026-03-24 | Fail loudly on sync errors | Silent failures cause data inconsistency and erode trust |

## Related Documents

- [CLI Reference](CLI_REFERENCE.md) — Full command documentation
- [SECURITY.md](../SECURITY.md) — Security considerations for tracker tokens
- [CONTRIBUTING.md](../CONTRIBUTING.md) — How to contribute new integrations
