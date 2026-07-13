# Documentation Disposition Inventory

Reviewed: 2026-07-07 (paths updated 2026-07-10 for the Mintlify port: user docs now live in the docs/ site tree; this ledger and other internal docs live in engdocs/)

CI cleanup entry added: 2026-05-28

Scope: every Markdown file under `docs/`, including `adr/`, `design/`,
`design/otel/`, and the staged-for-removal bin.

Evidence bar: retained factual claims must be backed by current CLI behaviour,
current tests, or code that is used by the product. Reference-shaped docs either
need generated/checkable maintenance, or an explicit `Last reviewed:` marker and
freshness source.

## Canonical Seams

| Seam | Canonical docs | Rule |
|---|---|---|
| Architecture | `ARCHITECTURE.md`, `INTERNALS.md`, `DOLT.md`, `adr/`, `design/` | Durable structure, package boundaries, storage model, and invariants live here. |
| Behaviour/reference | `CLI_REFERENCE.md`, `CONFIG.md`, `SETUP.md`, `JSON_SCHEMA.md`, `RECOVERY.md`, `ERROR_HANDLING.md`, `TROUBLESHOOTING.md` | CLI/config/runtime contracts live here and need generation or freshness review. |
| User-facing workflow | `INSTALLING.md`, `QUICKSTART.md`, `FAQ.md`, `SYNC_SETUP.md`, integration guides, `WORKTREES.md`, `UNINSTALLING.md` | Task-oriented user docs live here; avoid duplicating implementation tables unless linked to reference docs. |
| Maintainer/operator | root `RELEASING.md`, `RELEASE-STABILITY-GATE.md`, `LINTING.md`, `SECURITY-DEPENDENCY-EXCEPTIONS.md`, `PERFORMANCE_TESTING.md`, `CI_TEST_SURFACE_AUDIT.md`, `CI_CLEANUP_PLAN.md` | Maintainer process docs stay active only when tied to current scripts/checks. |
| Historical/staged | `staged-for-removal/` | Resolved audits, stale duplicates, and unsupported snapshots are preserved here until deleted or rescued. |

## Reference Freshness

| Doc | Freshness path |
|---|---|
| `CLI_REFERENCE.md` | Generated from `bd help --all`. |
| `plugins/beads/skills/beads/resources/CLI_REFERENCE.md` | Pointer-only doc: must link to live CLI help and canonical generated `docs/CLI_REFERENCE.md`; do not duplicate generated command tables. |
| `reference/configuration.md` | `Last reviewed:` marker tied to `cmd/bd/main.go`, `cmd/bd/config.go`, and `internal/configfile/`. (Mintlify port: absorbed `CONFIG.md`.) |
| `getting-started/ide-setup.md` | `Last reviewed:` marker tied to `cmd/bd/setup*.go` and `internal/recipes/`. (Mintlify port: absorbed `SETUP.md`.) |
| `integrations/azure-devops.md` | `Last reviewed:` marker tied to `cmd/bd/ado*.go` and `internal/ado/`. (Mintlify port: was `ADO_CONFIG.md`.) |
| `reference/json-schema.md` | `Last reviewed:` marker tied to `cmd/bd/output.go`, `cmd/bd/errors.go`, and protocol tests. (Mintlify port: was `JSON_SCHEMA.md`.) |
| `recovery/init-safety.md` | `Last reviewed:` marker tied to `cmd/bd/init*.go` safety code and tests. (Mintlify port: was `RECOVERY.md`.) |
| `ERROR_HANDLING.md` | `Last reviewed:` marker tied to current command error exits and JSON error helpers. |
| `LINTING.md` | `Last reviewed:` marker tied to `.golangci.yml` and current lint output. |
| `CI_CLEANUP_PLAN.md` | `Last reviewed:` marker tied to CI audit, workflow files, package manifests, and maintainer decision review. |
| `design/otel/otel-data-model.md` | `Last reviewed:` marker tied to telemetry, Dolt storage, hooks, and AI call sites. |

Follow-up automation should replace marker-only checks with generated or
`--check` blocks where a clean code source exists.

## Active Docs

| File | Disposition | Rationale and evidence |
|---|---|---|
| `ADAPTIVE_IDS.md` | Keep | Behaviour doc for hash ID scaling; verify against ID generation code before changing numeric claims. |
| `ADO_CONFIG.md` | Keep with freshness | ADO reference; marker points at ADO command/client code. |
| `AGENT_SIGNING.md` | Keep | Maintainer/operator convention for agent comment and commit signatures. |
| `adr/0001-multi-remote-approach.md` | Keep | ADR; historical decision record, not a live reference table. |
| `adr/0002-init-safety-invariants.md` | Keep | ADR backing `RECOVERY.md` and init safety code. |
| `ADVANCED.md` | Keep/revise as needed | User-facing advanced workflows; mixed command examples should defer to generated CLI reference when expanded. |
| `AIDER_INTEGRATION.md` | Keep | User-facing integration guide; evidence is setup/integration behaviour. |
| `ANTIVIRUS.md` | Keep | User-facing operational note; review vendor/version claims when touched. |
| `ARCHITECTURE.md` | Keep | Primary architecture overview; evidence is current package layout and Dolt-only storage path. |
| `ATTRIBUTION.md` | Keep | Attribution record for removed merge engine. |
| `CLAUDE_INTEGRATION.md` | Keep | Design/user guide for Claude setup; paired with `SETUP.md`. |
| `CLAUDE.md` | Revise | Kept as architecture orientation only; command/workflow duplication was reduced in favour of root `AGENTS.md` and `AGENT_INSTRUCTIONS.md`. |
| `CLI_REFERENCE.md` | Keep/generated | Generated command reference from live help output. |
| `CI_CLEANUP_PLAN.md` | Keep with freshness | Accepted CI tier decisions, package gate policy, measurement plan, and implementation order. |
| `CI_REQUIRED_CHECK_TOPOLOGY.md` | Keep | Design note for the required-check aggregate gate topology; explicitly flags pre-rollout status and gates branch-protection changes on the new checks passing first. |
| `CI_TEST_SURFACE_AUDIT.md` | Keep with freshness | Snapshot of local validation commands, GitHub Actions coverage, gaps, and CI cleanup roadmap. |
| `plugins/beads/skills/beads/resources/CLI_REFERENCE.md` | Keep pointer | Plugin skill resource intentionally points at live CLI sources to avoid duplicate generated command snapshots. |
| `CODEX_INTEGRATION.md` | Keep | User-facing Codex integration guide. |
| `COLLISION_MATH.md` | Keep | Mathematical background; low product drift. |
| `COMMUNITY_TOOLS.md` | Keep | Curated external tools list; external links need periodic review. |
| `CONFIG.md` | Keep with freshness | Reference doc; reviewed against config and env-var code. |
| `CONTRIBUTOR_NAMESPACE_ISOLATION.md` | Keep | Design/user guide for contributor routing and `BEADS_DIR` behaviour. |
| `COPILOT_CLI_INTEGRATION.md` | Keep | Design doc for GitHub Copilot CLI integration; paired with `COPILOT_INTEGRATION.md` (VS Code + MCP). |
| `COPILOT_INTEGRATION.md` | Keep | User-facing integration guide. |
| `DEPENDENCIES.md` | Keep | Behaviour doc for graph semantics. |
| `design/dolt-concurrency.md` | Keep | Design note for Dolt concurrency. |
| `design/kv-store.md` | Keep | Draft design note; retain as design seam, not user reference. |
| `design/otel/otel-architecture.md` | Keep | Architecture/design doc for telemetry; reference tables should defer to data model. |
| `design/otel/otel-data-model.md` | Keep with freshness | Reference schema; reviewed against telemetry and emission code. |
| `DOC_INVENTORY.md` | Keep | This disposition and seam inventory. |
| `DOLT-BACKEND.md` | Consolidated pointer | Old duplicate staged; stable path points to canonical `DOLT.md`. |
| `DOLT.md` | Keep/canonical | Canonical Dolt backend guide. |
| `ERROR_HANDLING.md` | Keep with freshness | Pattern guide with code-linked examples; marker added. |
| `EXCLUSIVE_LOCK.md` | Keep | Behaviour/design doc for lock protocol. |
| `FAQ.md` | Revise | Opening wording now describes beads as Dolt-powered; stale pre-1.0 status removed. |
| `FEDERATION.md` | Keep/canonical | Canonical federation (peer-to-peer sync) guide; moved from root `FEDERATION-SETUP.md`, which is now a stable pointer. Absorbed the duplicate federation section previously in `DOLT.md`. |
| `GIT_INTEGRATION.md` | Keep | User-facing git/worktree/hook behaviour. |
| `graph-links.md` | Keep | Behaviour/design doc for graph links. |
| `ICU-POLICY.md` | Revise | Link updated to canonical Dolt doc. |
| `INSTALLING.md` | Keep | User-facing installation guide; install matrix needs periodic link/version review. |
| `INTEGRATION_CHARTER.md` | Keep | Scope-boundary policy. |
| `INTERNALS.md` | Keep | Internal architecture/runtime deep dive. |
| `JSON_SCHEMA.md` | Keep with freshness | JSON contract; marker tied to schema constant and tests. |
| `LABELS.md` | Keep | User-facing label philosophy plus examples; generated CLI handles command reference. |
| `LINTING.md` | Revise with freshness | Stale fixed-count wording removed; marker tied to current lint output. |
| `messaging.md` | Keep | Design doc for messaging issue types. |
| `METADATA.md` | Keep | Behaviour doc for metadata field semantics. |
| `MOLECULES.md` | Keep | User-facing workflow concept doc. |
| `MULTI_REPO_AGENTS.md` | Keep | Agent workflow guide. |
| `MULTI_REPO_MIGRATION.md` | Keep | Human migration guide. |
| `OBSERVABILITY.md` | Keep | User-facing OTel guide; data-model reference owns schema tables. |
| `PERFORMANCE_TESTING.md` | Keep | Maintainer testing guide. |
| `PLUGIN.md` | Keep | User-facing plugin guide. |
| `PROJECT_CHARTER.md` | Keep | Canonical product scope and boundary policy. |
| `PROTECTED_BRANCHES.md` | Keep | User-facing protected-branch workflow. |
| `QUICKSTART.md` | Keep pointer | Short pointer to website quickstart; low drift. |
| `README_TESTING.md` | Consolidated pointer | Old duplicate staged; active path points to `TESTING.md` and `TESTING_PHILOSOPHY.md`. |
| `RECOVERY.md` | Keep with freshness | Runtime recovery playbooks; marker tied to init safety constants/tests. |
| `RELATED_PROJECTS.md` | Keep | Curated list of adjacent, non-integration projects; companion to `COMMUNITY_TOOLS.md`. |
| `RELEASE-STABILITY-GATE.md` | Keep | Maintainer release gate policy. |
| `RELEASING.md` | Consolidated pointer | Old duplicate staged; canonical process is root `RELEASING.md`. |
| `REPO_CONTEXT.md` | Keep | Architecture/behaviour doc for repo context. |
| `ROUTING.md` | Keep | Multi-repo auto-routing design. |
| `RULES_AUDIT_DESIGN.md` | Keep | Design doc for rules audit. |
| `SECURITY-DEPENDENCY-EXCEPTIONS.md` | Keep with freshness | Existing freshness-marker exemplar. |
| `SETUP.md` | Keep with freshness | Setup reference; marker tied to setup commands and recipes. |
| `staged-for-removal/MANIFEST.md` | Keep | Staged removal process and per-file rationale. |
| `superpowers/plans/2026-05-03-unclaim-command.md` | Removed | PR-passenger planning artifact from a contributor's personal multi-agent rig, same class as the artifacts removed in commit 187ba85f3 (#4632); no readers in this repo, content stays in git history. |
| `superpowers/specs/2026-05-03-unclaim-command-design.md` | Removed | PR-passenger design-spec artifact paired with the plans file above; same removal rationale, content stays in git history. |
| `SYNC_CONCEPTS.md` | Keep/canonical | Sanctioned website-mirror source for the sync conceptual model (Dolt source of truth, wire format, JSONL export, anti-patterns); listed in `deploy-docs.yml` and linked from `AGENTS.md`, `README.md`, `ARCHITECTURE.md`, `DOLT.md`, `GIT_INTEGRATION.md`, and `FAQ.md`. |
| `SYNC_SETUP.md` | Revise | Links now point at canonical `DOLT.md`. |
| `TESTING.md` | Revise/canonical | Canonical test-running guide; stale line/test-count and stale skip entry removed. |
| `TESTING_PHILOSOPHY.md` | Keep | Canonical test-design guidance; duplicate/historical links removed. |
| `TODO.md` | Keep | Behaviour/user guide for `bd todo`. |
| `TROUBLESHOOTING.md` | Keep | User-facing recovery guide; debug/env tables need freshness review when edited. |
| `UI_PHILOSOPHY.md` | Keep | Design philosophy. |
| `UNINSTALLING.md` | Keep | User-facing uninstall guide. |
| `WORKTREES.md` | Keep | User-facing worktree guide. |

## Staged Docs

Every staged file is recorded in
[`staged-for-removal/MANIFEST.md`](staged-for-removal/MANIFEST.md) with original
path, reason, missing evidence, and rescue criteria.
