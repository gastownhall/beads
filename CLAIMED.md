# CLAIMED — advisory registry of in-flight work

A lightweight, advisory list of who is actively working which slice of the
project, so overlapping efforts surface when work starts instead of when
finished PRs collide.

Motivating example: #4697 (claim_fence + guarded verbs) and #4715
(holder_token + advisory enforcement) are two open PRs occupying the same
work-ownership slot with different designs. Nothing in the repo records which
design is the live candidate for the slot or who is driving it, so the
overlap is discoverable only by reading both PRs end to end. A one-line entry
here makes the next such situation visible at claim time.

## Rules

- **Advisory only.** An entry is a courtesy signal, not a lock and not an
  entitlement. Maintainer decisions and merged code always win over a row
  here.
- Add a row before starting substantial work — a feature slot, a phase of an
  accepted proposal, a large refactor. Small fixes don't need one.
- Remove or update your row when the work merges, is abandoned, or hands
  over.
- Anyone may prune a row with no visible activity for ~30 days, ideally
  after a ping on the linked issue.

## Claims

| Issue(s) / slot | Who | Since | Where |
|---|---|---|---|
| Versioned beads, phases 0–5 ([milestone 1](https://github.com/gastownhall/beads/milestone/1): #6132–#6138; design #5898) | @quad341 | 2026-09-01 | phase PRs from `deploy/*` branches; phase 0: #6147 |
| Versioned beads Phase 2 — migration slot 0068 (`0068_add_attribution_status`: `issue_versions.attribution_status`, design §16.4 / #6135) | @quad341 | 2026-09-06 | branch `builder/be-0uifx` on quad341/beads-sec003-contrib (head `c249019da`); PR #6358 (branch `deploy/be-764ey-gate`), review and release gate passed, stacked on #6147 |
| BDP bead graph — migration slots **0069 and later** (five replicated-table files: scope, types, beads, links, ledger; plus the dolt-ignored `graph_authority_lease` main-series file and its `ignored/` twin); design #6154 | @donnabox | 2026-09-07 | `feat/bead-graph` on donnabox/beads; docs PR #6154 (all P-1 rulings in); P0 (contracts + pinned wire) as a separate PR next; the migrations land in P1 behind a replication/merge ADR |
