# PROPOSAL: general conditional (compare-and-set) update for `bd`

**Status:** design, ready for implementation as a beads PR
**Author:** design handoff (wyvern hostile-review epic wy-mdi5h)
**Repo:** `github.com/steveyegge/beads` (`~/src/beads`, branch `main` @ `3a6dcff96`)
**Motivating incidents:** wyvern wheelhouse review findings wy-mdi5h.3, .6, .7, .4 (TOCTOU park/restore/take/bulldog); upstream lineage bd-zccb9, #4727, wy-ejph3, wy-x543k.

---

## 1. Problem

A hostile review of wyvern's wheelhouse fleet scripts found that its single most
common bug class is **check-then-act on `bd` assignee/status**: park, interactive
restore, queue-take, and bulldog-claim each read a bead's state, then issue a
*blind* `bd update -a X` / `-s Y`, and lose to a racing writer in the gap. The
scripts paper over it with re-reads (`wh-take.sh`), but re-reads only narrow the
window; they cannot close it. The root cause is not a wheelhouse bug — it is that
**`bd` exposes no general compare-and-set for the coordination fields.**

`bd` already recognizes this need and has been closing it verb by verb:

| primitive | CAS semantics | added |
|---|---|---|
| `bd update --claim` | `assignee ∈ {'', pool-alias} → me`, `status → in_progress` | (existing) |
| `bd unclaim --if-assignee X` | `assignee == X → ''`, `status → open` | #4727 |
| `verifiedClaimWrite` (WIP) | verify-after-write for the above under a degraded server | bd-zccb9 (uncommitted in tree) |

The pattern is a `UPDATE … WHERE id=? AND status IN(…) AND assignee=?` with
`RowsAffected` as the verdict and a `row_lock` rewrite so a racing reclaim/close
conflicts instead of silently merging (`issueops.UnclaimIssueIfAssigneeInTx`).
This proposal **generalizes that idiom once** instead of minting a third and
fourth special-case verb, and covers the two transitions no existing verb can
express.

## 2. The gap

Two wheelhouse transitions have no primitive:

- **Reassign `X → Y`** (park): "set assignee to `mayor` only if it is still the
  worker I just reclaimed." `--claim` only does `'' → me`; `unclaim --if-assignee`
  only does `X → ''`. Nothing does `X → Y`.
- **Claim-on-behalf with a status guard** (restore): "set assignee to the crew
  owner *and* status to `in_progress`, only if the bead is still `open`/unassigned."
  `--claim` always claims for `$BEADS_ACTOR`, never for a third party, and takes
  no status precondition.

Queue-take (`wh-take.sh`) and bulldog-claim are **not** new-primitive gaps — they
are adoption gaps: `--claim` already handles pool aliases (`claim.pools`, e.g.
`fable-crew`) atomically, so both should simply call `--claim` and honor its
non-zero exit instead of a blind `-a` + swallowed refusal. This proposal fixes
the primitive; those two are follow-up wheelhouse patches (wy-mdi5h.7, .4).

## 3. Proposal: `--if-assignee` / `--if-status` guards on `bd update`

Add two precondition flags to the existing `update` command. When either is
present, the mutation becomes an atomic CAS; when neither is present, `update` is
unchanged.

```
bd update <id> --if-assignee <expected> [--if-status <expected>] [ -a <new> ] [ -s <new> ] [ …other field flags ]
```

Semantics:

- **Precondition** = the conjunction of every `--if-*` guard present. The row must
  currently match *all* of them or the write does not apply.
- **`--if-assignee ''`** means "expected unassigned." Presence is detected with
  `cmd.Flags().Changed("if-assignee")`, exactly as `unclaim` distinguishes an
  explicit empty value (unclaim.go:59–61) — so `--if-assignee ""` is a real
  guard, not "no guard."
- **Atomicity**: one `UPDATE … SET <fields> WHERE id=? AND assignee=? [AND status=?]`
  in a single tx, `RowsAffected==1` is the verdict, `row_lock` rewritten to
  `freshRowLock()` (same invariant as unclaim — a racing reclaim/close on the row
  must conflict, not merge). The guard read and the UPDATE run in the same
  transaction, so there is no internal TOCTOU (mirrors `UnclaimIssueIfAssigneeInTx`).
- **Precondition failure** (`RowsAffected==0`): return a typed
  `storage.ErrAssigneeMismatch` / new `ErrStatusMismatch`, surfaced as a **loud,
  non-zero exit** with actual-vs-expected in the message. It must **never** be
  swallowed or collapse to exit 0 — this is the whole point; the CLI already has
  the "requested-but-lost claim ⇒ SilentExit()/non-zero" plumbing for `--claim`
  (update_proxied_server.go, "beads audit finding #10") and this reuses it.
- **Verify-after-write**: route through the WIP `verifiedClaimWrite` (a guarded
  reassign is coordination-critical and idempotent for the winner — safe to
  replay when a re-read proves the commit rolled back). Add a `reassigned(actor)`
  / `matched(post)` `claimPostcondition` alongside `claimedBy`/`unclaimed`.
- **Wisps exempt**, closed issues rejected — same as the sibling verbs.
- **Batch/JSON**: same mixed-batch exit-code rule as `--claim` (one lost
  precondition in a batch ⇒ non-zero overall), same JSON shape.

### Why guards on `update` rather than a new `bd reassign` verb

- The transitions we need are *arbitrary* field CAS (assignee and/or status), not
  a single named operation; a verb per transition is how we got here.
- `update` already owns multi-field mutation, batching, JSON, proxied-server
  routing, and the finding-#10 exit-code contract — guards compose with all of it
  for free.
- `--claim` and `unclaim --if-assignee` remain the ergonomic shorthands for their
  two hot paths; the general guards are the escape hatch the wrappers currently
  fake with `bd sql`.

Alternative considered — **dedicated `bd reassign --from --to`**: more
discoverable for the park case, but doesn't express restore's status-guarded
claim-on-behalf without yet more flags, and duplicates update's plumbing. Rejected
in favor of the general guards. (Flag it for maintainer preference — it is a CLI
surface call, not a correctness call.)

## 4. How each wheelhouse finding collapses to it

```sh
# park (wy-mdi5h.3) — reassign only if the worker still holds it:
bd update "$id" --if-assignee "$reclaimed_worker" -a "$PARK_ASSIGNEE"

# interactive restore (wy-mdi5h.6) — claim-on-behalf, only while still open:
bd update "$id" --if-assignee '' --if-status open -a "$owner" -s in_progress

# queue-take (wy-mdi5h.7) — ADOPTION, no new primitive: use pool-aware claim
bd update "$id" --claim          # fable-crew is a claim.pools alias

# bulldog (wy-mdi5h.4) — ADOPTION: claim + honor the non-zero exit, don't swallow
bd update "$BEAD" --claim || { echo "foreign/lost; not mutating"; exit 1; }
```

The first two are the primitive this proposal adds; the last two are wheelhouse
patches that consume the *existing* `--claim`.

## 5. Implementation sketch (files)

- `internal/storage/issueops/update_cas.go` (new): `UpdateIssueIfMatchInTx(ctx,
  tx, id, guards, updates)` — build the `WHERE` from present guards, reuse the
  unclaim.go CAS body (row_lock rewrite, `status IN ('open','in_progress')`
  liveness clause, RowsAffected verdict, typed mismatch error incl. actual state).
- `internal/storage/storage.go`: add `ErrStatusMismatch`; add
  `UpdateIssueIfMatch(ctx, id, actor, guards, updates)` to the `Storage` interface.
- `internal/storage/dolt/issues.go`: `DoltStore.UpdateIssueIfMatch` wrapping
  `verifiedClaimWrite` with a `matched(post)` postcondition + DOLT_ADD/COMMIT.
- `cmd/bd/update.go`: register `--if-assignee` (String) and `--if-status`
  (String); when either `Changed()`, route to the CAS path; wire the
  precondition-failed → non-zero exit through the existing `claimFailed` machinery
  (both `update.go` and `update_proxied_server.go`). Reject `--if-*` combined with
  `--claim` (mutually exclusive; `--claim` is its own CAS).
- `cmd/bd/prime.go`: document the new guards next to `--claim`.

## 6. Tests

Beads side (their conventions):
- `internal/storage/issueops` unit tests: match applies, assignee-mismatch no-op,
  status-mismatch no-op, `--if-assignee ''` matches only unassigned, closed-issue
  reject, row_lock rewritten on success.
- A concurrency test in the spirit of the existing claim CAS tests: two goroutines
  reassign the same row, exactly one gets `RowsAffected==1`.
- `verifiedClaimWrite` path: reported-success-but-rolled-back ⇒ replay; applied ⇒
  success; genuine mismatch ⇒ loud non-zero.
- CLI: non-zero exit on precondition failure, mixed-batch exit code, JSON shape.

Wyvern side (house rule — each consuming fix gets a `test-*.sh` pin):
- `test-park-cas.sh`, `test-restore-cas.sh` asserting a racing claim in the window
  is refused (non-zero) rather than clobbered.

## 7. Scope / non-goals

- **In:** assignee + status guards on `update`; storage + CLI + verify integration;
  the two wheelhouse consumers that are genuine primitive gaps.
- **Out:** label/other-field guards (`--if-label`) — no current consumer; add later
  if one appears. The wheelhouse *adoption* patches (queue-take, bulldog switching
  to `--claim`) are separate wy-mdi5h children, not part of this beads PR.
- **Coordinate with the uncommitted `claim_verify.go` WIP** already in the tree —
  this proposal depends on `verifiedClaimWrite` landing (or lands alongside it).
