# Oracle A — refactor-safety differential conformance for `bd`

Oracle A answers one question mechanically: **did this change alter any
user-visible `bd` behavior?**

It runs the same curated CLI scenarios against two separately-built `bd`
binaries and diffs every step:

| role | built from | meaning |
|---|---|---|
| **REFERENCE** | the merge-base with `origin/main` (overridable via `REF_REF`) | the "before" |
| **CANDIDATE** | the current working tree (HEAD + uncommitted) | the "after" |

Each scenario is an ordered list of `bd` CLI argv steps run as real processes in
a throwaway workspace (`bd init` + steps). For every step the harness compares
**exit code**, **stderr**, and **JSON-aware stdout** (object key order ignored;
array order compared as a multiset by default, as an ordered sequence for
scenarios flagged `ordered`). Volatile values — timestamps, UUIDs, and the host
actor identity — are normalized to `<TS>`/`<UUID>`/`<ACTOR>`/`<EMAIL>` before
comparison. **Tolerated in-scope divergences: zero.**

## Run it

```sh
tests/oracle-a/run-oracle-a.sh
```

Exit status: `0` = 100% in-scope pass, `1` = at least one in-scope divergence,
`2` = setup/build error. On failure, each divergence is printed with the
reference vs candidate value for the offending step.

Overrides:

- `REF_REF=<gitref>` — compare against something other than the merge-base
  (e.g. a release tag, or `origin/main`'s tip).
- `ORACLE_CATALOG=1` — **deep tier**: in addition to the curated scenarios, run
  the full enumerated catalog (`harness/scenarios/enumerated.json`, ~500
  deterministic scenarios covering the wider `bd` CLI surface). Much broader
  coverage, ~10–15 min. Off by default so the everyday gate stays fast.
- `KEEP_ARTIFACTS=1` — keep the scratch build dir (binaries, goldens, scoreboard
  output) for inspection instead of deleting it on exit.

## Two tiers

- **Curated** (default) — the hand-maintained scenarios in `harness/src/scenarios.rs`:
  a small, always-green set targeting the highest-value contract behaviors. Fast
  enough to run on every change.
- **Enumerated catalog** (`ORACLE_CATALOG=1`) — ~500 deterministic scenarios in
  `harness/scenarios/enumerated.json`, generated to sweep the wider CLI surface as
  data. Non-deterministic entries (ID minting, non-reproducible init output) are
  excluded automatically; entries already covered by the curated set are de-duped.
  The in-scope predicate (below) applies identically to both tiers.

## Prerequisites

- **Rust / `cargo`** — builds the conformance harness (`harness/`).
- **A CGO toolchain (`gcc`/`cc`)** and **`go`** — `bd` embeds Dolt, which is cgo;
  both binaries build with `CGO_ENABLED=1 -tags gms_pure_go` (the `gms_pure_go`
  tag is mandatory per `engdocs/ICU-POLICY.md`).
- **`git`** with `origin/main` fetched (the script resolves `REF_REF` locally;
  run `git fetch` first if it is stale).
- **`jq`** — used for the golden floor assertion (the reference's `create` steps
  must exit 0 with an id, so a broken environment can't score a false green).

## Runtime

Dominated by the **cold reference build** (a full `bd` compile, ~1 min on a warm
module cache, several minutes cold). The candidate build reuses the local build
cache and is fast. Harness build is ~10 s. Golden capture + scoring run the
curated scenarios as real `bd` processes (~30–60 s). End-to-end: **~2–7 minutes**
depending on Go build-cache warmth (the **deep tier**, `ORACLE_CATALOG=1`, adds the
~500-scenario catalog for **~10–15 minutes** total). There is no Dolt *server* in
the loop — every scenario uses embedded Dolt in its own tempdir.

## What green PROVES

- For the **curated contract scenarios** (see `harness/src/scenarios.rs`), the
  candidate `bd` produces byte-identical (post-normalization) exit codes, stderr,
  and JSON-structural stdout to the reference `bd`, step for step.
- This covers the semantics most likely to break under a storage or serialization
  refactor: the close→ready `is_blocked` propagation, transitive/parent-child
  blocking, cycle rejection, claim lifecycle + idempotent self-reclaim, storage
  tiers (ephemeral/no-history) set algebra, **`delete`** and a **real (non-dry-run)
  `purge` with re-seed** (both must recompute surviving neighbours' `is_blocked`),
  **`comment` add/list**, **`config set`/`get` success and reject paths**,
  metadata storage, **label-based query filtering** (`list --label`), and the
  error contracts (`bd sql` embedded-unsupported, not-found, config-key rejection).

## What green does NOT prove

- **Only the in-scope surface.** The predicate is the contract commands + flags in
  `harness/src/bin/scoreboard.rs` (`IN_SCOPE_CMDS` / `IN_SCOPE_FLAGS`, with `--json`
  required for the JSON-output commands). Behavior outside that set — most of the
  wider `bd` command surface, human/plain output modes, and any flag not listed —
  is reported as out-of-scope and is **not** a gate. A change that touches only
  out-of-scope behavior passes Oracle A.
- **Ordering tiebreaks are UNGATED.** `ready`/`list` order among equal-priority
  issues is empirically **nondeterministic** here: every issue is created seconds
  apart, bd orders equal priorities by `created_at DESC` (with an `id ASC` final
  tiebreak), and `created_at` is both sub-second-precision-dependent AND normalized
  to `<TS>` — so the same equal-priority creates land in a different order run to
  run. Array output is therefore compared as a **multiset** by default; only
  scenarios with all-DISTINCT priorities are marked `ordered` and compared as a
  sequence. **What this leaves unpinned:** the `id ASC` / `created_at DESC`
  tiebreak chain, and the >48h hybrid-priority arm (unreachable without an
  injectable clock). A change that breaks tiebreaks or the hybrid arm passes Oracle A.
- **Minted-ID paths are UNGATED.** Every create scenario passes an explicit `--id`.
  A `--id`-less create mints a hash-derived ID that hashes the wall-clock timestamp,
  so the reference and candidate mint *different* IDs for "the same" create and the
  ID is NOT normalized (it is not a UUID) — an `--id`-less scenario would flake. ID
  minting is a property-test concern, not a byte-diff one.
- **Anything invisible to stdout/stderr/exit diffing.** Post-commit hook firing,
  rollback-suppressed side effects, and N-commits-instead-of-one are deliberately
  NOT asserted here. Timestamp *values* are erased by `<TS>` normalization, so a
  local-vs-UTC divergence in a timestamp value is also out of reach.
- **The scenario environment is scrubbed, and `BEADS_TEST_MODE=1` is ungated.**
  Scenarios run with an explicit env whitelist (`PATH`/`HOME`/`TMPDIR` +
  `BEADS_TEST_MODE=1`) rather than the inherited host env, so a green certifies the
  *same* configuration every run. But `BEADS_TEST_MODE=1` itself changes store
  construction (auto-server decisions off, port rewiring), so this rig does NOT
  cover the mode/topology construction paths — do not cite Oracle A green for those.
- **This is a same-backend gate.** Oracle A is same-backend (embedded Dolt)
  before-vs-after. It is a refactor-safety net, not a cross-backend conformance run.

## The harness

The Rust crate under `harness/` is a self-contained conformance rig
(MIT OR Apache-2.0). It builds two binaries — `capture_golden` (records the
reference `bd`'s per-step traces) and `scoreboard` (replays every scenario against
the candidate `bd` and diffs it against the goldens). Goldens are re-captured from
the reference build into `harness/testdata/golden/` (git-ignored) on **every** run,
so the gate always reflects the current "before" — nothing is pinned to a stale
snapshot. See `harness/PROVENANCE.md` for how goldens, the environment contract,
and the standalone Cargo manifest are handled.
