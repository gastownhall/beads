---
title: Related Projects
description: Adjacent, independent projects that solve neighboring problems and compose well with beads
---

Adjacent or complementary tools that solve different problems in the
same neighborhood as Beads. These are not Beads integrations (see
[Community Tools](/community-tools) for those) — they are
independent projects whose users may also find Beads useful, or vice
versa.

## Recall / knowledge graph

- **[scry](https://github.com/prmichaelsen/scry)** —
  ([scryspec.com](https://scryspec.com)) — marker-indexed knowledge
  and recall graph for AI coding agents. Files declare identity via
  inline `@scry.entry` markers; the index makes designs, lessons, and
  decisions reachable by meaning, tag, and seeded question rather than
  by path. Different job from Beads: where Beads is a task graph for
  *what to do next*, scry is a recall layer for *what was decided and
  why*. They compose — independently arrived at the same hash-based-ID
  convention (`bd-a1b2`, `~hash`) for the same reason: preventing
  collisions across multi-agent and multi-branch work.

## Task queues for agent fleets

- **[bead-rs](https://github.com/jedarden/bead-rs)** — clean-room Rust
  task-coordination store for agent fleets: a dependency graph of "beads" in
  SQLite, exactly one unblocked bead handed out per request through a
  single-transaction atomic claim, and a git-tracked checkpoint for recovery.
  Same neighbourhood as Beads (it began as a response to read-then-write
  claiming racing at twenty workers) but a separate lineage and schema; not a
  Beads integration.
