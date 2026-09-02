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
| Versioned beads, phases 0–3 ([milestone 1](https://github.com/gastownhall/beads/milestone/1): #6132–#6138; design #5898) | @quad341 | 2026-09-01 | phase PRs from `deploy/*` branches; phase 0: #6147 |
| Versioned beads Phase 1 — migration slot 0067 (`issue_versions`, `store_epoch`, `issues.current_revision`); be-hs42e.2 / #6134 | @quad341 | 2026-09-02 | branch `builder/be-hs42e.2`, PR pending |
