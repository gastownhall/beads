# engdocs/ — contributor notes

`engdocs/` holds contributor and internals documentation. It is read on
GitHub, not published: local links must be exact relative file paths, and
`TestEngdocsAndRootMarkdownLinks` in `test/docsync` fails on any that do not
resolve. User-facing documentation lives in `docs/` (the Mintlify site) and
follows the `beads-docs` skill.

## What lives where

| Path | Contents |
|---|---|
| `PROJECT_CHARTER.md` | Product boundary: what belongs in core, an integration, or the orchestration layer. |
| `TESTING.md`, `LINTING.md` | Canonical test and lint policy. |
| `ERROR_HANDLING.md`, `UI_PHILOSOPHY.md` | Conventions for errors and CLI output. |
| `ADDING_AN_ISSUEOPS_ROLE.md` | How to add a storage operation end to end. |
| `EXTENDING.md`, `INTEGRATION_CHARTER.md` | Extension and integration boundaries. |
| `AGENT_SIGNING.md` | Signatures for agent-prepared commits, comments, and reviews. |
| `design/` | Design documents for subsystems (Dolt concurrency, `bd serve`, ownership hand-off, …). |
| `adr/` | Architecture decision records, `<NNNN>-<slug>.md`, each with a `## Status` section. Accepted ADRs are superseded by a new ADR, not rewritten. |
| `decisions/` | Dated decision records for the docs tooling. |
| `staged-for-removal/` | Docs that no longer meet the active-doc bar (see `staged-for-removal/MANIFEST.md`). Do not link to or update them. |

## Code orientation

- `cmd/bd/` — the CLI (see [cmd/bd/AGENTS.md](../cmd/bd/AGENTS.md)).
- `internal/workapi/` — the work-query contract shared by every bd frontend:
  filter construction, defaults, validation, response shaping.
- `internal/httpapi/` — the `bd serve` HTTP surface
  ([SERVE_RUNBOOK.md](SERVE_RUNBOOK.md)).
- `internal/storage/` — shared storage interface and value types, plus
  `HookFiringStore` and its `hook_*.go` decorators, which fire
  on_create/on_update/on_close hooks after successful mutations.
- `internal/storage/issueops/` — transaction-scoped SQL operations shared by
  the server-mode (`internal/storage/dolt/`) and embedded
  (`internal/storage/embeddeddolt/`) stores.
- `internal/storage/schema/` — schema and migrations
  ([migrations/README.md](../internal/storage/schema/migrations/README.md)).
- `internal/doltserver/` — lifecycle of a local `dolt sql-server` process.
- `beads.go`, `backend/`, `issueops/`, `journalops/`, `memoryops/`,
  `format/`, `beadserrors/` — the public Go API for extending bd and for
  out-of-tree storage backends.

Each package's doc comment (`go doc ./internal/<pkg>`) is the authoritative
description; prefer it over prose here when they disagree, and fix this file.
