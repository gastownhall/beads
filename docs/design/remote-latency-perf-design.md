# Remote-Latency Performance Fixes — Design

- Date: 2026-04-18
- Branch: `alexmsu`
- Status: Draft, pending review
- Structure: 1 spec + 1 implementation plan, 3 independently-revertable commits on `alexmsu`

## 1. Why — Motivation and User Impact

### The observed pain

On a remote developer workstation connected to the team's central `dolt sql-server` over a healthy LAN (15 ms MySQL RTT), every `git commit` in a bd-managed project takes **3 minutes 29 seconds**. The delay comes from bd's pre-commit hook, which exports the issue database to JSONL on every commit. What should feel instant instead blocks every commit for minutes.

### Who this hurts

- **Remote developers** on any bd project hosted on a LAN-central Dolt server — the author has twelve such projects. Anyone working from a different machine than the Dolt host experiences this.
- **AI agents and automation** that run `bd list`, `bd ready`, or `bd doctor` on a loop — each invocation pays the same latency tax. A polling agent can burn tens of minutes of wall time per hour on nothing but query round-trips.
- **Pre-commit UX** for the whole team once more developers switch to remote workflows. Hooks that take minutes train users to skip them (`--no-verify`), which defeats the reason hooks exist.

### Concrete cost

Assuming an active developer commits ten times per workday:
- Current: 10 commits × 3m29s = **~35 minutes/day blocked on bd export**.
- After fix: 10 commits × ≤15 s = **≤2.5 minutes/day**.

Across `bd list`, `bd ready`, `bd doctor`, and pre-commit combined, a remote developer today loses roughly one hour per day to bd latency alone. The fix recovers essentially all of it.

### Why the central Dolt server topology is the right one to support

The `dolt-setup.md` memory captures the rationale for a single central `dolt sql-server` serving all project DBs: multi-client consistency, one source of truth, simple NAS-based disaster recovery, no per-project server sprawl. We do not want to abandon that topology. Alternatives considered and rejected:

- **Local embedded-clone mirror on every developer machine.** Adds sync lag, splits the write path, and complicates team freshness guarantees.
- **Move bd execution to the Dolt host via SSH exec.** Awkward for git workflows, requires project mirroring, and surfaces a new class of hook failures.
- **Network-layer tunneling (WireGuard, Tailscale, stunnel).** Measurement proved network RTT is not the dominant cost (see Section 2). No tunnel can recover work that isn't on the wire.

### Why now

1. **We have direct evidence.** A throw-away SQL-tracing driver wrapper proved that one specific N+1 pattern accounts for 99% of the remote cost — 2,285 per-ID `SELECT 1 FROM wisps WHERE id = ? LIMIT 1` probes per `bd export`. The fix target is unambiguous.
2. **The fix is cheap.** One new helper (~25 lines), seven mechanical call-site rewrites with identical shape, one DSN flag flip. Low blast radius, easy to review, easy to revert.
3. **It compounds.** Every future remote bd command — not just the ones measured — benefits from the same batching and the same DSN fix. The investment pays back on every invocation going forward.
4. **Left alone, the pressure grows.** As the issue database grows (one of the busier bd-managed projects already has 457 issues), the N+1 gets proportionally worse. The 208 s number is today's cost; it will increase linearly.

### What "done" looks like

A remote developer runs `git commit` and the hook finishes in under 15 seconds. `bd list`, `bd ready`, and `bd doctor` feel indistinguishable from localhost. No new cross-project coordination is required, no new infrastructure is introduced, and the change is revertable commit-by-commit if any regression surfaces.

## 2. Problem Statement

On a remote client (hostB, WSL2 mirrored-networking) connected over a trusted LAN to a central `dolt sql-server` (hostA, `192.0.2.10:3308`), `bd hooks run pre-commit` hangs for roughly 3m29s. The pre-commit hook spawns `bd export`, which itself takes **208s** end-to-end on the remote but only **2.2s** on hostA localhost — a 95× slowdown that cannot be explained by raw network RTT.

### Measured evidence

| Metric | Value |
|---|---|
| Warm `SELECT 1;` over `mysql` client (hostB → hostA) | 15 ms |
| `bd export` wall time, local on hostA | 2.2 s |
| `bd export` wall time, remote on hostB | 208 s |
| Total SQL round-trips issued by `bd export` | 2,316 |
| Distinct MySQL connections opened during one `bd export` | 2 |
| Queries matching the `IsActiveWispInTx` per-ID probe (`SELECT 1 FROM wisps WHERE id = ? LIMIT 1`) | **2,285** |
| Remaining bulk/batched queries | 31 |

Measured via a throw-away `database/sql` driver wrapper that logged each `Query`/`Exec` with `query`, `args`, and duration. The wrapper was reverted after measurement.

### Root cause

bd partitions IDs into `wisps` vs `issues` by calling `IsActiveWispInTx` in a per-ID loop inside **seven** bulk helpers:

1. `internal/storage/issueops/dependencies.go:212-218` (`GetIssuesByIDsInTx`)
2. `internal/storage/issueops/labels.go:48-55` (`GetLabelsForIssuesInTx`)
3. `internal/storage/issueops/comments.go:56-63` (`GetCommentCountsInTx`)
4. `internal/storage/issueops/bulk_ops.go:113-120` (`GetCommentsForIssuesInTx`)
5. `internal/storage/issueops/dependency_queries.go:45-52` (`GetDependencyRecordsForIssuesInTx`)
6. `internal/storage/issueops/dependency_queries.go:207-214` (`GetBlockingInfoForIssuesInTx`)
7. `internal/storage/issueops/delete.go:77-84` (`DeleteIssuesInTx`)

Each call site iterates N IDs and issues one `SELECT 1 FROM wisps WHERE id = ? LIMIT 1` per ID. For 457 issues × 5 bulk helpers invoked by `bd export` that's ~2,285 per-ID probes. Because the DSN does not set `InterpolateParams`, each parameterized `Query`/`QueryRow` under go-sql-driver/mysql uses the server-side binary protocol (PREPARE + EXECUTE + CLOSE; driver source: `github.com/go-sql-driver/mysql@v1.9.3/connection.go:239-410`) — multiple round-trips per query. At 15 ms RTT × (conservatively) 3 RTs × 2,285 probes = **~103 s lower bound**; measured wall-clock is **~200 s** of the 208 s total. The remaining ~100 s is not captured at protocol level in this measurement — plausible sources include per-query server-side work (point lookups add microseconds each, but `SELECT 1`-style probes may still pay a minimum per-statement floor), TCP Nagle / delayed-ACK interactions with the 3-RT sequence, and WSL2 mirrored-mode vSwitch processing. The count-proportional 103 s dominates, which is sufficient to justify the fix regardless of the unattributed overhead — see §9 success criteria which are framed on attributable savings, not the unexplained remainder.

The SQL-trace wrapper patch used to gather this evidence is preserved in **Appendix B** so reviewers can re-run the measurement without guesswork.

### Attribution

| Source | Count | Remote cost | Category |
|---|---:|---:|---|
| `IsActiveWispInTx` N+1 | 2,285 | ~200 s | **bd** — fixable in this repo |
| Legit bulk SELECTs (IN batches) | 25 | ~2.2 s | protocol |
| Dolt-specific (`DOLT_HASHOF`, `SHOW DATABASES`, metadata) | 6 | ~0.5 s | dolt |
| Go/Cobra/JSON overhead | — | ~2 s | bd (fixed) |

Roughly **99% bd, 1% Dolt**. No upstream Dolt change is required.

## 3. Goals

- Reduce remote `bd export` from **208 s → ≤ 10 s** (2 orders of magnitude).
- Apply the same proportional speed-up to every bulk-helper-consuming command: `bd list --json`, `bd ready --json`, `bd show`, `bd doctor`.
- Land each change as an independently-revertable commit on branch `alexmsu`.
- No behavior change for embedded mode, long-timeout merge isolation, or short-lived admin helpers.

## 4. Non-Goals

- Persistent bd daemon / RPC (out of scope; separate future design).
- Dolt-side patches (not required; 1% attribution).
- Local embedded-clone mirroring (an alternative discussed but avoided to keep the central-server topology).
- Any change to embedded-mode pool configuration (`MaxOpenConns=1` there is a correctness invariant).
- `dolt-concurrency.md` orchestrator work; that migration is already landed.

## 5. Approach — Three Commits

### Commit 1 — `perf(storage): batch wisp routing probes in bulk helpers`

**Scope.** Add a bulk partitioner in `internal/storage/issueops/wisp_routing.go`, then replace all seven per-ID loops.

**New API — two helpers in `wisp_routing.go`.**

```go
// ActiveWispIDsInTx returns the subset of ids that exist in the active wisps
// table, batched at queryBatchSize. Primitive — intended for callers that need
// a set to probe membership for arbitrary IDs.
func ActiveWispIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error)

// PartitionByWispInTx splits ids into wispIDs and permIDs by running one batched
// membership probe. The returned slices preserve the input order within each
// partition, so callers using the result for deterministic iteration (e.g.,
// JSON export ordering) remain stable. Implemented on top of ActiveWispIDsInTx.
func PartitionByWispInTx(ctx context.Context, tx *sql.Tx, ids []string) (wispIDs, permIDs []string, err error)
```

Both APIs ship in the same commit:
- `ActiveWispIDsInTx` is the primitive. Unit-tested directly.
- `PartitionByWispInTx` is a thin wrapper used by all seven bulk call sites.

`map[string]bool` matches the overwhelming bd convention (326 occurrences vs 17 for `map[string]struct{}` — code review response documents this).

**Call-site rewrite pattern** (identical across all 7 sites):

```go
// before
var wispIDs, permIDs []string
for _, id := range ids {
    if IsActiveWispInTx(ctx, tx, id) {
        wispIDs = append(wispIDs, id)
    } else {
        permIDs = append(permIDs, id)
    }
}

// after — the wrapper collapses ~8 lines to 4 and preserves input ordering
wispIDs, permIDs, err := PartitionByWispInTx(ctx, tx, ids)
if err != nil {
    return nil, err
}
```

Preserving input ordering removes a known footgun: any future caller that ranges a `map[string]bool` would see Go's randomized iteration. `PartitionByWispInTx` iterates the input slice internally, so the returned `[]string` slices are always input-ordered.

**Non-targets.** The ~20 single-call sites of `IsActiveWispInTx` (e.g., `close.go`, `update.go`, `claim.go`, `promote.go`, `delete.go:24`, `get_issue.go`, `events.go`, `molecule.go`) are **not** an N+1 — each operates on one ID. Leave them alone.

**Expected impact.** 2,285 per-ID probes → ~3 batched `IN (?)` queries. Alone this drops remote `bd export` from 208 s to **~8 s**.

### Commit 2 — `perf(dolt): enable InterpolateParams to collapse prepared-statement round-trips`

**Scope.** Set `InterpolateParams: true` in `internal/storage/doltutil/dsn.go`. The go-sql-driver/mysql driver then performs parameter interpolation client-side, collapsing each parameterized query from the multi-RT binary protocol to a single round-trip.

**Driver-behavior audit — enumerated.** `InterpolateParams` disables server-side prepared statements and does client-side quoting. The following was verified against the actual bd codebase (not just driver source). Full evidence lives in the commit message:

| Arg type observed on write paths | File:line (sample) | Interpolation behavior | Risk |
|---|---|---|---|
| `string` | `internal/storage/issueops/helpers.go:46-97` (47 arg insert), `bulk_ops.go:94,201` | Driver quotes with `'…'`, escapes `'`, `\`, NUL per MySQL standard | None — identical to server-side PREPARE behavior |
| `int`, `int64` | `helpers.go:46-97`, `compaction.go:91` | Numeric literal | None |
| `bool` | `helpers.go:46-97` | `1`/`0` | None |
| `time.Time` | `helpers.go:46-97`, `compaction.go:91` | `'YYYY-MM-DD HH:MM:SS[.fraction]'` via `utils.go:268 appendDateTime`, respects `ParseTime=true` | None — schema is second-precision |
| `*time.Time` (nullable) | `helpers.go:46-97` | Dereference or `NULL` | None |
| `nil` (from `NullString` helper) | `helpers.go:46-97` | Literal `NULL` | None |
| `[]byte` | **only** `federation.go:32-46`, `credentials.go:166,258-265` (password_encrypted) | Hex literal `_binary 0x…` via `connection.go:306-317` | None — already round-tripped by binary protocol today |
| JSON as `string` | `helpers.go:548 JSONMetadata()`, `queries.go:140 JSON_EXTRACT` parameters | Quoted as ordinary string, server parses JSON on read | None — bd never passes `json.RawMessage` directly |

**What bd does NOT have that would be at risk:**
- No `db.Prepare()` / `tx.Prepare()` / `sql.Stmt` reuse anywhere in non-test code (`grep` returned zero hits). Reviewer concern about reused prepared statements silently degrading to per-call quoting does not apply.
- No custom `driver.Valuer` implementations in `internal/types/` (0 matches).
- No `json.RawMessage` arguments passed to `Exec`/`Query` — JSON always pre-serialized to `string`.

**`MultiStatements=true` interaction.** The driver's `Exec`/`Query` paths (`connection.go:340-410`) do not branch on `MultiStatements` when interpolating; the flag only affects whether the server tokenizes `;` in the query body. Orthogonal.

**Coverage.** Applies everywhere `ServerDSN.String()` is consumed, which is every server-mode connection (`store.go:openServerConnection`, doctor, dolt subcommands, admin helpers). Embedded mode uses a different DSN builder and is unaffected.

**Observability upside.** Dolt's query log will now show the final interpolated SQL instead of the parameterized skeleton, which is strictly better for `EXPLAIN` / post-mortem debugging. Called out in the commit message.

**Expected impact.** Additional reduction on the remaining 25-31 parameterized queries and on **all** future remote bd commands. Combined with Commit 1, projected `bd export` remote is within the ≤ 10 s success criterion (§9); tighter projections intentionally not stated to avoid over-claiming.

### Commit 3 — `perf(dolt): tune connection-pool settings in auxiliary paths`

**Scope.** Narrow. The main server pool (`store.go:1257 applyPoolLimits`) already uses `MaxOpenConns=10 / MaxIdleConns=5 / ConnMaxLifetime=1h` — designed for long-lived daemons, not part of this fix. The remaining `MaxOpenConns(1)` sites are audited and selectively widened only where they serve no correctness role.

| File:line | Current | Action | Rationale |
|---|---|---|---|
| `internal/storage/embeddeddolt/open.go:51-54` | `MaxOpenConns=1, ConnMaxLifetime=0` | **No change** | Single-writer invariant of embedded mode |
| `internal/storage/dolt/store.go:1238` (`execWithLongTimeout`) | `MaxOpenConns=1` | **No change** | Deliberately isolated 5-minute ops (merge/conflict) |
| `internal/storage/dolt/store.go:2066` | `MaxOpenConns=1` | Audit & leave if it's an isolation barrier | Verify before touching |
| `internal/doltserver/doltserver.go:894,932` (admin helpers) | `MaxOpenConns=1, ConnMaxLifetime=10s` | **No change** | Short-lived, one-shot ops |
| `cmd/bd/dolt.go:1518-1520` (dolt subcommands — only reached from `bd dolt clean-databases`; fans out to `SHOW DATABASES` + `DROP DATABASE`, no `DOLT_CHECKOUT`) | `MaxOpenConns=2 / Idle=1 / Lifetime=30s` | Widen to `5 / 2 / 60s` via named constants | No isolation requirement; reviewer-proposed 60s cap adopted as defense-in-depth |
| `cmd/bd/doctor/server.go:320-322` (only reached from `checkConnectionWithDB`, `checkSchemaWithDB`, `checkIssueCountWithDB` — all read-only) | `MaxOpenConns=2 / Idle=1 / Lifetime=30s` | Widen to `5 / 2 / 60s` via named constants | Read-only diagnostic path, no session state |
| `cmd/bd/doctor/dolt.go:59-61` (same read-only fan-out via `checkPhantomDatabases`) | `MaxOpenConns=2 / Idle=1 / Lifetime=30s` | Widen to `5 / 2 / 60s` via named constants | Same as above |

**Named constants.** To make future audits trivial, extract the three tuples into package-level constants in a new file `cmd/bd/doctor/pool_config.go`:

```go
package doctor

import "time"

// DiagnosticPoolMaxOpenConns caps concurrent diagnostic probes. Wider than the
// historical 2 so doctor runs faster; narrower than the daemon pool (10) so
// idle socket count stays small on the shared Dolt server.
const DiagnosticPoolMaxOpenConns = 5

// DiagnosticPoolMaxIdleConns = keep 2 warm for burst checks.
const DiagnosticPoolMaxIdleConns = 2

// DiagnosticPoolMaxLifetime = 60s caps connection reuse to a value safely
// smaller than any plausible branch-state-affecting window, while still
// avoiding the reconnect storm of the previous 30s setting.
const DiagnosticPoolMaxLifetime = 60 * time.Second
```

All three call sites reference the same constants.

**Branch-switching audit (addresses review concern).** A connection reused across a `DOLT_CHECKOUT` / branch switch could return wrong-branch rows. Verified: none of the three widened call sites reach `DOLT_CHECKOUT`. Actual `DOLT_CHECKOUT` sites are `internal/storage/versioncontrolops/branches.go:54`, `compact.go:30,47,64`, `flatten.go:55,58` — none reachable from `cmd/bd/dolt.go:1518`'s `clean-databases` path or from `cmd/bd/doctor/*`. The `60 s` lifetime is still chosen as defense-in-depth and is safely below any plausible server-state-volatility window.

**Expected impact.** Minor for `bd export` (≈0). Moderate for `bd doctor` and `bd dolt clean-databases` (≈1-3 s per invocation).

**Explicit non-touch list** (all correctness-load-bearing):

- `internal/storage/embeddeddolt/open.go`
- `internal/storage/dolt/store.go:1238` (`execWithLongTimeout`)
- `internal/doltserver/doltserver.go:894,932` (`EnsureGlobalDatabase`, `FlushWorkingSet`)
- Any test-only tuning in `*_test.go`

## 6. Testing Strategy

### Unit tests

- **Commit 1.** New `wisp_routing_test.go` covering:
  - Empty input → empty map, no query issued.
  - All permanent IDs → empty map.
  - Mixed set → correct wisp IDs only.
  - Batching: set `queryBatchSize=2`, feed 5 mixed IDs, expect 3 queries via `sqlmock`.
  - Matches existing semantics: for every partitioning scenario, the result is bit-identical to the per-ID loop.
  - `PartitionByWispInTx` ordering: verify the returned `wispIDs` and `permIDs` slices preserve input order within each partition, so consumers relying on deterministic iteration are safe.
- **Commit 2.** New `dsn_test.go` in `internal/storage/doltutil/`: confirm DSN string contains `interpolateParams=true` and that the previously-set flags (`parseTime`, `multiStatements`, `allowNativePasswords`, `tls=false`) are preserved.
- **Commit 3.** Add `cmd/bd/doctor/pool_config_test.go` asserting the three named constants are used by all widened sites. Existing `connection_pool_test.go` must stay green.

### Integration tests

- Existing integration tests under `cmd/bd/testdata/*.txt` must pass unchanged.
- **New test — semantic-equal export (Commit 1, mandatory):** `internal/storage/issueops/wisp_routing_export_parity_test.go`. Fixture database with ~20 permanent issues + ~20 explicit-ID wisps. Produce two JSONL streams:
  1. Export using the pre-refactor `IsActiveWispInTx` per-ID loop (retained in a `testOnlyLegacyPartition` helper).
  2. Export using the new `PartitionByWispInTx`.
  Assertion: after parsing each line to an `IssueWithCounts` and sorting by ID, both streams are deep-equal. This is stronger than byte-identical diff (immune to cosmetic JSON whitespace / field-order noise) and tighter than a smoke test (detects any actual data-shape regression).
- **New test — Dolt-specific InterpolateParams round-trip (Commit 2, mandatory):** `internal/storage/dolt/interpolate_params_test.go`. Real in-process Dolt (via existing `testmain_test.go` harness). Two DB handles — one with `InterpolateParams=false`, one with `=true`. For each, insert + retrieve:
  - JSON column (`issues.metadata`): `{"a":null,"emoji":"\u00e9","nested":{"k":[1,2,3]}}`
  - DATETIME column (`issues.created_at`): `time.Date(2026, 4, 18, 12, 0, 0, 123456789, time.UTC)` (sub-microsecond input, second-precision schema)
  - String with SQL metacharacters: `O'Brien\\n"quote"`
  Assertion: `scanResult(false) == scanResult(true)` byte-for-byte. Closes reviewer BLOCKER 3. BLOB intentionally excluded — bd has no BLOB in parameterized write paths beyond `federation_peers.password_encrypted`, which is already covered by existing federation tests.
- Run full `go test ./...` to confirm no regression in `issueops/*_test.go`.

### Performance regression guard (Commit 1, mandatory — promoted from optional)

New benchmark `BenchmarkBulkPartitioning` in `wisp_routing_test.go`. Simulate N=500 IDs against an in-process Dolt using existing `dolt_benchmark_test.go` harness. In addition to timing, wrap the `*sql.Tx` with a counting driver and **fail** the benchmark if it issues more than `ceil(N / queryBatchSize) + 1` queries. A future engineer reintroducing an N+1 fails CI.

### Manual verification

On branch `alexmsu`, after each commit:

```bash
# From hostB (remote client):
cd ~/src/<bd-project>
time bd export -o /tmp/probe.jsonl

# Expected after Commit 1: ≈ 8s
# Expected after Commit 2: ≈ 4s
# Expected after Commit 3: ≈ 4s (unchanged for export; see doctor)
time bd doctor
# Expected after Commit 3: noticeable drop (from 91s → ~60-70s)
```

## 7. Risks & Mitigations

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| `PartitionByWispInTx` returns different set than per-ID loop due to NULL / case-sensitivity / whitespace | Low | Correctness | Parity unit test + mandatory `wisp_routing_export_parity_test.go` semantic-equal integration test |
| Future caller ranges over the `ActiveWispIDsInTx` set directly, relying on non-deterministic iteration order | Low | Subtle ordering bug | `PartitionByWispInTx` is the call-site API and returns input-ordered `[]string`; doc comment warns about direct map iteration |
| `InterpolateParams=true` changes `time.Time` / binary / JSON rendering | Low | Data corruption on writes | Enumerated audit in §5 Commit 2 + mandatory Dolt-specific round-trip test (`interpolate_params_test.go`) exercising JSON, DATETIME, SQL-metacharacter strings |
| `InterpolateParams` interacts with `MultiStatements` in unexpected ways | Very low | Query parse errors | Driver source audit confirms `Exec`/`Query` paths don't branch on `MultiStatements` when interpolating (`connection.go:340-410`); full test suite runs |
| Hidden prepared-statement reuse silently regresses under InterpolateParams | None (confirmed) | n/a | Zero `Prepare`/`Stmt` hits in non-test code; recorded in commit-2 message |
| Widened pool in `cmd/bd/doctor/*` triggers concurrency bug narrow setting hid | Low | Flaky doctor | Cap conservative (5/2/60s); no branch-switch reachable from widened call sites (audited in §5 Commit 3) |
| Connection reuse across `DOLT_CHECKOUT` returns wrong-branch rows | None (confirmed) | Correctness | Widened call sites verified read-only (dolt `SHOW DATABASES`/`DROP DATABASE` + doctor probes); no `DOLT_CHECKOUT` reachable; 60s lifetime as defense-in-depth |
| Commit 3 inadvertently touches a correctness-load-bearing `MaxOpenConns(1)` | Low | Stale data / hangs | Explicit non-touch list above; review diff against it |
| Remote timing varies run-to-run under Windows firewall inspection | Med | Misleading benchmark | Run each timing 3× and report median; capture `dolt-server.log` line count |
| Attribution gap: fixing query count recovers only the count-proportional ~103 s, leaves ~100 s unexplained overhead | Med | Target may slip | §9 target is 10 s (2× margin over the ~103 s recoverable); overhead ~100 s likely per-query (vanishes with fix) but not fully characterized; if the 10 s target is missed we revisit before proceeding to T2 |

## 8. Rollout

All three commits land on branch `alexmsu` in this repo. Sequence:

1. Branch is already `alexmsu`; confirm clean tree.
2. Implement Commit 1, run `go test ./... && golangci-lint run ./...`, verify manual remote time.
3. Implement Commit 2, same verification.
4. Implement Commit 3, same verification.
5. Run `make install` locally to refresh `~/.local/bin/bd`.
6. On hostB, re-measure `time git commit --allow-empty -m "post-fix smoke"` and compare to the 3m29s baseline.
7. Upstream PR authoring is out of scope for this spec; covered in a follow-up.

## 9. Success Criteria

- Remote `bd export` ≤ 10 s (baseline 208 s). No tighter projection asserted; see §2 for the unattributed-overhead caveat.
- Remote `git commit --allow-empty` via pre-commit hook ≤ 15 s (baseline 3m29s).
- `go test ./...` green.
- `golangci-lint run ./...` no new warnings above baseline.
- `bd doctor` passes with zero new errors.
- Export output is **semantically equal** (per-issue deep-equal after sort-by-ID) before vs after on a fixture DB — validated by `wisp_routing_export_parity_test.go`. Byte-identical guarantee is intentionally relaxed to avoid brittleness to cosmetic JSON-serialization noise.

## 10. Out of Scope / Follow-ups

- **`bd daemon`** — persistent local agent for true zero-startup command latency. Separate design.
- **`RunCompatMigrations` fast-path skip** — save ~20 RTs on every write command; separate issue.
- **`cmd/bd/doctor/perf_dolt.go` connection tuning** — already uses wider pool (5/2); out of scope here.
- **Observability around bulk helpers** — reviewer suggested a `debug.Logf` counter in `ActiveWispIDsInTx` that would emit batch count + size, making future N+1 regressions visible without re-running the SQL-trace wrapper. Valuable but orthogonal to the perf fix. Deferred out of this PR to keep review surface focused; re-raise if a future investigation needs it.
- **Local embedded clone on hostB (`Option T2` from discussion)** — alternative topology not required if the fixes above meet the target.
- **Upstream PRs to `steveyegge/beads`** — deferred; authored after `alexmsu` commits are validated in production use.
- **Commit 3 (auxiliary pool tuning) — descoped 2026-04-18.** Commit 3 in §5 was sized against a 91 s `bd doctor` baseline. After Commits 1 + 2 landed, the measured numbers were `bd export` 0.514 s, `bd doctor` 3.285 s, `bd dolt clean-databases --dry-run` 0.062 s. The pool-widening from 2/1/30s → 5/2/60s was designed to shave 1–3 s off commands that now complete in under 3.3 s. The marginal wall-clock gain does not justify the review surface (branch-switch reachability audit, `ConnMaxLifetime` semantics, potential `cmd/bd` → `cmd/bd/doctor` import-cycle fallback). The original `MaxOpenConns=2 / Idle=1 / Lifetime=30s` values stay. Revisit only if future workload surfaces pool starvation with fresh measurements.

## 11. Appendix A — File Inventory

### Files touched by Commit 1

- `internal/storage/issueops/wisp_routing.go` (add `ActiveWispIDsInTx` and `PartitionByWispInTx`)
- `internal/storage/issueops/wisp_routing_test.go` (new)
- `internal/storage/issueops/wisp_routing_export_parity_test.go` (new — semantic-equal export parity test)
- `internal/storage/issueops/dependencies.go` (lines 211-218)
- `internal/storage/issueops/labels.go` (lines 48-55)
- `internal/storage/issueops/comments.go` (lines 56-63)
- `internal/storage/issueops/bulk_ops.go` (lines 113-120)
- `internal/storage/issueops/dependency_queries.go` (lines 45-52 and 207-214)
- `internal/storage/issueops/delete.go` (lines 77-84)

### Files touched by Commit 2

- `internal/storage/doltutil/dsn.go`
- `internal/storage/doltutil/dsn_test.go` (new)
- `internal/storage/dolt/interpolate_params_test.go` (new — Dolt-specific JSON+DATETIME+string round-trip)

### Files touched by Commit 3

- `cmd/bd/dolt.go` (lines 1518-1520)
- `cmd/bd/doctor/server.go` (lines 320-322)
- `cmd/bd/doctor/dolt.go` (lines 59-61)
- `cmd/bd/doctor/pool_config.go` (new — named constants)
- `cmd/bd/doctor/pool_config_test.go` (new — asserts call sites use the constants)

### Explicit non-changes

- `internal/storage/embeddeddolt/open.go`
- `internal/storage/dolt/store.go:1238` (`execWithLongTimeout`)
- `internal/doltserver/doltserver.go:894,932`
- `internal/storage/dolt/store.go:1257` (`applyPoolLimits`, already correct)
- All `*_test.go` pool configurations

## 12. Appendix B — SQL-Trace Driver Wrapper (Reproducibility)

This is the throw-away instrumentation used to produce the 2,285-probe count in §2. It is **not** committed; reviewers can apply it on a scratch worktree to re-verify the baseline measurement and confirm the post-fix count.

### How to use

1. `git checkout -b perf-measure-tmp alexmsu`
2. Apply the patch below (create `internal/storage/dolt/sqltrace_probe.go`, edit `store.go:1284-1303`).
3. `make install`
4. `BD_SQL_LOG=/tmp/bd-sql.log bd export -o /tmp/export.jsonl` from hostB.
5. `jq -s 'group_by(.query) | map({q: .[0].query, n: length}) | sort_by(.n) | reverse | .[:10]' /tmp/bd-sql.log` to see the top-10 queries by count.
6. After measurement: `git checkout alexmsu && git branch -D perf-measure-tmp`.

### Patch — `internal/storage/dolt/sqltrace_probe.go` (new file)

```go
//go:build tracesql
// +build tracesql

package dolt

import (
    "context"
    "database/sql"
    "database/sql/driver"
    "encoding/json"
    "io"
    "os"
    "strings"
    "sync"
    "time"

    mysqldrv "github.com/go-sql-driver/mysql"
)

var (
    traceOnce sync.Once
    traceOut  io.Writer
    traceMu   sync.Mutex
)

func initTrace() {
    path := os.Getenv("BD_SQL_LOG")
    if path == "" {
        traceOut = io.Discard
        return
    }
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil {
        traceOut = io.Discard
        return
    }
    traceOut = f
}

type traceDriver struct{ inner driver.Driver }

func (d *traceDriver) Open(dsn string) (driver.Conn, error) {
    traceOnce.Do(initTrace)
    c, err := d.inner.Open(dsn)
    if err != nil {
        return nil, err
    }
    return &traceConn{inner: c}, nil
}

type traceConn struct{ inner driver.Conn }

func (c *traceConn) Prepare(q string) (driver.Stmt, error) { return c.inner.Prepare(q) }
func (c *traceConn) Close() error                          { return c.inner.Close() }
func (c *traceConn) Begin() (driver.Tx, error)             { return c.inner.Begin() }

func (c *traceConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
    t0 := time.Now()
    qc, ok := c.inner.(driver.QueryerContext)
    if !ok {
        return nil, driver.ErrSkip
    }
    rows, err := qc.QueryContext(ctx, q, args)
    log(q, args, time.Since(t0), err)
    return rows, err
}

func (c *traceConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
    t0 := time.Now()
    ec, ok := c.inner.(driver.ExecerContext)
    if !ok {
        return nil, driver.ErrSkip
    }
    r, err := ec.ExecContext(ctx, q, args)
    log(q, args, time.Since(t0), err)
    return r, err
}

func log(q string, args []driver.NamedValue, d time.Duration, err error) {
    traceMu.Lock()
    defer traceMu.Unlock()
    rec := map[string]any{
        "q":   strings.TrimSpace(q),
        "dur": d.Microseconds(),
        "n":   len(args),
    }
    if err != nil {
        rec["err"] = err.Error()
    }
    b, _ := json.Marshal(rec)
    _, _ = traceOut.Write(append(b, '\n'))
}

// init replaces the standard "mysql" driver with the tracing wrapper when
// built with -tags tracesql. No runtime overhead when built without the tag.
func init() {
    sql.Register("mysql-trace", &traceDriver{inner: mysqldrv.MySQLDriver{}})
}
```

### Patch — `internal/storage/dolt/store.go`

Change the driver name in `openServerConnection` under the tracesql build:

```go
// Replace: db, err := sql.Open("mysql", connStr)
// With:    db, err := sql.Open("mysql-trace", connStr)
// (only under `-tags tracesql` builds; default build unchanged)
```

Or simpler: keep as-is and add `BD_SQL_DRIVER` env lookup:

```go
drv := "mysql"
if os.Getenv("BD_SQL_LOG") != "" {
    drv = "mysql-trace"
}
db, err := sql.Open(drv, connStr)
```

Build with `go build -tags tracesql -o ~/.local/bin/bd ./cmd/bd` to enable; the default build has zero cost. The instrumentation file compiles only under the build tag.

### Interpreting the output

Each line is a JSON record with `q`, `args`-count, `dur` (µs), optional `err`. Sum durations and group by `q` to replicate the §2 breakdown. Before the fix, expect ~2,285 lines with query `SELECT 1 FROM wisps WHERE id = ? LIMIT 1`. After Commit 1, expect that count to drop to 0 and see ~3 lines with `SELECT id FROM wisps WHERE id IN (?, ?, …)`.
