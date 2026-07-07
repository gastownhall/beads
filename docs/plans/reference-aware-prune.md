# Plan: Reference-Aware `bd prune` (be-zej8mz)

**PM:** beads/pm  
**Date:** 2026-05-17  
**Architect spec:** be-5sn  
**Designer spec:** be-zej8mz (description field)  
**Status:** Decomposed → builder handoff

---

## Goal

`bd prune` gains a reference-protection layer: closed beads whose ID appears in any open bead's description, notes, or comments are silently skipped. A new `--ignore-references` flag provides the escape hatch.

This closes the incident from 2026-05-01 where architect ADR beads (be-08pl, be-eei) were pruned despite being cited by open verification beads.

---

## Child beads

| ID | Title | Label | Target |
|----|-------|-------|--------|
| be-jewoem | Implement reference-aware bd prune core logic | ready-to-build | builder |
| be-u2mw2x | Update bd prune --help and README for reference-aware skip | ready-to-build | builder |
| be-0wt833 | Bench: bd prune reference scan under 5s on 10K-open-bead fixture | ready-to-build | builder (blocked on be-jewoem) |

---

## Dependency graph

```
be-zej8mz (parent)
├── be-jewoem  core impl          ← unblocked, ready to build
├── be-u2mw2x  docs update        ← unblocked, ready to build
└── be-0wt833  bench              ← blocked until be-jewoem merges
```

---

## Key implementation notes for builder

- **Use `Statuses` filter** (not `ExcludeStatus`) when fetching non-closed beads — PG has a coverage gap on `ExcludeStatus` (be-jdeief). Statuses: open, in_progress, blocked, deferred, pinned.
- **`--ignore-references` on `pruneCmd` only** — `bd purge --ignore-references` must return "unknown flag".
- **`referenced_skipped` always in JSON** (even 0) so orchestrators can branch; `referenced_ids_sample` omitted when 0.
- **Human output**: ≤5 IDs in sample; JSON: ≤100 IDs in sample.
- **No new colors** — reuse `ui.MutedStyle`, `ui.IDStyle`, `ui.CommandStyle` from existing palette.
- **Exact output copy** in be-zej8mz §4 (designer spec) — match it precisely.

---

## Acceptance summary

1. `bd prune` skips referenced closed beads by default.
2. `bd prune --ignore-references` deletes them.
3. `bd purge --ignore-references` → "unknown flag".
4. Integration test: 3-bead fixture (1 referenced, 1 pinned, 1 plain) → only plain deleted.
5. Bench: 10K-bead fixture scan <5s (skippable with `testing.Short()`).
6. All output matches be-zej8mz §4 exact copy.
7. `go test ./...` passes; `golangci-lint` clean.
