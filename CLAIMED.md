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
- When two rows claim the same slot, whichever push settles it moves the
  losing row in that same push, so the table never records two owners for
  one number. A reader should be able to resolve the table from the rows
  alone, without reading the thread.
- Anyone may prune a row with no visible activity for ~30 days, ideally
  after a ping on the linked issue.

## Claims

| Issue(s) / slot | Who | Since | Where |
|---|---|---|---|
| Versioned beads, phases 0–5 ([milestone 1](https://github.com/gastownhall/beads/milestone/1): #6132–#6138; design #5898) | @quad341 | 2026-09-01 | phase PRs from quad341/beads-sec003-contrib. Merged: #6304 (phase 1, migration 0067); #6650, which carried phase 0's #6147 and phase 2's #6358 (migration 0068); #6675 (migration 0069). The open PRs and their slots are in the next two rows |
| Versioned beads Phase 2 (#6135), the §16.2b participation marker and write fence — migration slot: next contiguous number at merge time (the branch carries `0070_add_participation_generation` and its clone-local twin `ignored/0028_add_wisps_participation_generation`; 0070 is also carried by #6661 and #6008, see their rows) | @quad341 | 2026-09-29 | PR #6943 (branch `deploy/be-o7j67-gate` on quad341/beads-sec003-contrib), based on main |
| Versioned beads Phase 3 (#6136) and the `bd versions` read surface from #6138 — migration slots: next contiguous numbers at merge time (the branches carry `0070_add_removed_restriction`: R7.1 as-of read, and `0071_add_epoch_minted_addresses`: R20 epochs; R16 expected-revision CAS needs no slot, it lands on `issues.current_revision` from 0067; 0070 is also carried by #6943 and #6008, see their rows) | @quad341 | 2026-09-10 | PR #6661 (`bd versions`, the config switch and R7.1; based on main) carries 0070; PR #6664 (R20) is stacked on #6661 and adds 0071 |
| BDP bead graph — migration slots: the next free numbers when P1 lands (five replicated-table files: scope, types, beads, links, ledger; plus the dolt-ignored `graph_authority_lease` main-series file and its `ignored/` twin); design #6154 | @donnabox | 2026-09-07 | `feat/bead-graph` on donnabox/beads; docs PR #6154 (all P-1 rulings in) merged 2026-09-29; P0 (contracts + pinned wire) is PR #6422, which adds no migration; the migrations land in P1 behind a replication/merge ADR. Slot cell changed on 2026-10-03 by @quad341 from "0071 and later" to [@donnabox's own next-free-numbers rule](https://github.com/gastownhall/beads/pull/6149#issuecomment-5585888296), because main now ends at 0069 and open PRs carry 0070–0071; @donnabox, correct this row here if that reads wrong |
| Opt-in label vocabulary registry — migration slot: next contiguous number at merge time (the branch carries `0070_create_label_definitions`, renumbered 0068→0069 behind #6650 and 0069→0070 behind #6675; 0070 is also carried by #6661 and #6943, see their rows); #6010 inherits whatever number #6008 lands on | @marcodelpin | 2026-09-18 | branch `pr/label-definitions-storage` on marcodelpin/beads; PR #6008 (approved), #6010 stacked on it and inheriting the number. Rule, as @marcodelpin proposed it for #6008 / #6358: whichever branch merges second onto a number rebases and renumbers to the next contiguous number before landing, because `TestAllMigrationsSQLAppliesThroughDoltCLIAndRecordsLatestVersion` requires `COUNT(*) == LatestVersion()` (measured on #6008). The order among the 0070 carriers (#6008, #6661, #6943) is the maintainers' call. Transcribed by @quad341 from [@marcodelpin's proposed row](https://github.com/gastownhall/beads/pull/6149#issuecomment-5724342038), updated 2026-10-03 for the renumbers; @marcodelpin, correct it here if it reads wrong |
