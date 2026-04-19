# Remote-Latency Performance Fixes — Implementation Plan

- Date: 2026-04-18
- Branch: `alexmsu`
- Spec: `docs/design/remote-latency-perf-design.md`
- Status: Draft, pending review

This plan enumerates the concrete ordered steps to implement the three commits described in the spec. Every step is reversible; no step is merged into `main` automatically. Each commit ends with an explicit verification gate.

## Preconditions

- Working tree on branch `alexmsu` is clean (`git status` shows only `.claude/settings.local.json.bak` as pre-existing untracked).
- `go test ./...` passes on the current tree (baseline).
- `golangci-lint run ./...` warning count captured (baseline).
- Remote timing baseline captured (e.g., `time bd export -o /tmp/probe.jsonl` from hostB = 208 s).

Command to establish baseline:

```bash
cd <beads-root>
git status --short
go test ./... > /tmp/baseline-tests.log 2>&1
golangci-lint run ./... > /tmp/baseline-lint.log 2>&1
```

## Commit 1 — `perf(storage): batch wisp routing probes in bulk helpers`

### 1.1 Add bulk partitioner

**File:** `internal/storage/issueops/wisp_routing.go`

Append after the existing `WispTableRouting` function. Ship **two** helpers: the primitive set-returner and the partition wrapper that call sites use.

```go
// ActiveWispIDsInTx returns the subset of ids that exist in the active
// wisps table, batched at queryBatchSize. This is the primitive — most
// call sites want PartitionByWispInTx below, which preserves input
// ordering. Callers that range the returned map directly must be aware
// Go map iteration order is randomized.
//
// Returns an empty (non-nil) map if ids is empty.
func ActiveWispIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
    result := make(map[string]bool, len(ids))
    if len(ids) == 0 {
        return result, nil
    }

    for start := 0; start < len(ids); start += queryBatchSize {
        end := start + queryBatchSize
        if end > len(ids) {
            end = len(ids)
        }
        batch := ids[start:end]

        placeholders := make([]string, len(batch))
        args := make([]any, len(batch))
        for i, id := range batch {
            placeholders[i] = "?"
            args[i] = id
        }

        //nolint:gosec // G201: placeholders are only "?" literals
        q := fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ","))
        rows, err := tx.QueryContext(ctx, q, args...)
        if err != nil {
            return nil, fmt.Errorf("ActiveWispIDsInTx: %w", err)
        }
        for rows.Next() {
            var id string
            if err := rows.Scan(&id); err != nil {
                _ = rows.Close()
                return nil, fmt.Errorf("ActiveWispIDsInTx scan: %w", err)
            }
            result[id] = true
        }
        if err := rows.Err(); err != nil {
            _ = rows.Close()
            return nil, fmt.Errorf("ActiveWispIDsInTx rows: %w", err)
        }
        _ = rows.Close()
    }

    return result, nil
}

// PartitionByWispInTx splits ids into (wispIDs, permIDs) by running one
// batched membership probe against the wisps table. The returned slices
// preserve input order within each partition, so JSON export ordering
// (bd export, bd list --json) stays deterministic across refactors.
//
// Callers that need a set primitive should use ActiveWispIDsInTx directly.
func PartitionByWispInTx(ctx context.Context, tx *sql.Tx, ids []string) (wispIDs, permIDs []string, err error) {
    if len(ids) == 0 {
        return nil, nil, nil
    }
    wispSet, err := ActiveWispIDsInTx(ctx, tx, ids)
    if err != nil {
        return nil, nil, err
    }
    wispIDs = make([]string, 0, len(wispSet))
    permIDs = make([]string, 0, len(ids)-len(wispSet))
    for _, id := range ids {
        if wispSet[id] {
            wispIDs = append(wispIDs, id)
        } else {
            permIDs = append(permIDs, id)
        }
    }
    return wispIDs, permIDs, nil
}
```

Add the imports if not already present:

```go
import (
    "context"
    "database/sql"
    "fmt"
    "strings"
)
```

Verify `queryBatchSize` is accessible from this package (it is — defined in the same `issueops` package).

### 1.2 Unit tests

**File:** `internal/storage/issueops/wisp_routing_test.go` (new)

Leverage existing test helpers in `internal/storage/dolt/testmain_test.go`; do not mock the DB.

1. `TestActiveWispIDsInTx_Empty` — `ids=nil` and `ids=[]` → empty map, no query issued.
2. `TestActiveWispIDsInTx_AllPermanent` — seed only `issues` rows → empty map.
3. `TestActiveWispIDsInTx_AllWisps` — seed only `wisps` rows → result equals input set.
4. `TestActiveWispIDsInTx_Mixed` — seed both → result contains only wisp IDs.
5. `TestActiveWispIDsInTx_Batching` — call with `queryBatchSize*2+1` IDs; assert the result still equals the expected wisp subset.
6. `TestActiveWispIDsInTx_ParityWithLoop` — table-driven: for random ID sets, assert `ActiveWispIDsInTx` result equals bit-identical the per-ID `IsActiveWispInTx` partition.
7. `TestPartitionByWispInTx_Ordering` — given interleaved wisps/perm IDs `[w1, p1, w2, p2, w3]`, assert returned `wispIDs == [w1, w2, w3]` and `permIDs == [p1, p2]` in input order.
8. `TestPartitionByWispInTx_Empty` — nil and empty input → `(nil, nil, nil)`.

### 1.2.1 Semantic-equal export parity test (mandatory)

**File:** `internal/storage/issueops/wisp_routing_export_parity_test.go` (new)

Create a fixture DB with ~20 permanent issues and ~20 explicit-ID wisps (mix of IDs, labels, dependencies, comments). Run two code paths against it:

1. `legacyPartitionAndExport(ctx, store)` — retain a `testOnlyLegacyPartition` helper that uses the old per-ID loop, produce a JSONL byte stream.
2. `bd export` via the new code path, produce a JSONL byte stream.

Assertion: parse both JSONL streams into `[]*types.IssueWithCounts`, sort by `ID`, and `reflect.DeepEqual`. A byte-diff is intentionally avoided because it would fail on cosmetic JSON whitespace/field-order noise without any actual data regression.

This test is the Commit 1 correctness gate. It must be green before Commit 1 ships.

### 1.3 Replace per-ID loops at the 7 call sites

For each site, apply the uniform transformation below — using `PartitionByWispInTx` (not the primitive). One `Edit` per file; keep the diff minimal.

```go
// from:
var wispIDs, permIDs []string
for _, id := range ids {
    if IsActiveWispInTx(ctx, tx, id) {
        wispIDs = append(wispIDs, id)
    } else {
        permIDs = append(permIDs, id)
    }
}

// to:
wispIDs, permIDs, err := PartitionByWispInTx(ctx, tx, ids)
if err != nil {
    return nil, err
}
```

Per-file notes:

**1.3.a** `internal/storage/issueops/dependencies.go` lines 210-218 inside `GetIssuesByIDsInTx`. Returns `([]*types.Issue, error)`.

**1.3.b** `internal/storage/issueops/labels.go` lines 48-55 inside `GetLabelsForIssuesInTx`. Input slice is `issueIDs`; returns `(map[string][]string, error)`.

**1.3.c** `internal/storage/issueops/comments.go` lines 56-63 inside `GetCommentCountsInTx`. Returns `(map[string]int, error)`.

**1.3.d** `internal/storage/issueops/bulk_ops.go` lines 112-120 inside `GetCommentsForIssuesInTx`. Returns `(map[string][]*types.Comment, error)`.

**1.3.e** `internal/storage/issueops/dependency_queries.go` lines 45-52 inside `GetDependencyRecordsForIssuesInTx`. Returns `(map[string][]*types.Dependency, error)`.

**1.3.f** `internal/storage/issueops/dependency_queries.go` lines 205-214 inside `GetBlockingInfoForIssuesInTx`. Verify the exact return signature when editing; apply the matching `return <zero>, err`.

**1.3.g** `internal/storage/issueops/delete.go` lines 76-84 inside `DeleteIssuesInTx`. Returns `(*types.DeleteIssuesResult, error)`. Existing names are `wispIDs` / `regularIDs`; rename returns from `PartitionByWispInTx` accordingly: `wispIDs, regularIDs, err := PartitionByWispInTx(...)`.

### 1.4 Mandatory performance regression benchmark

**File:** `internal/storage/issueops/wisp_routing_test.go` (same file as 1.2)

Add `BenchmarkBulkPartitioning` that wraps the `*sql.Tx` with a counting driver and **fails** (`b.Fatalf`) if the query count exceeds the expected ceiling.

```go
// Expected query count for N IDs at queryBatchSize B:
//   ceil(N / B) batched IN queries + at most 1 legacy fallback probe = tight ceiling.
func BenchmarkBulkPartitioning(b *testing.B) {
    const N = 500
    // ... seed fixture ...
    expectedMax := (N+queryBatchSize-1)/queryBatchSize + 1

    var queryCount atomic.Int64
    // wrap tx so every QueryContext increments queryCount
    // ...

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        queryCount.Store(0)
        _, _, err := PartitionByWispInTx(ctx, tx, ids)
        if err != nil {
            b.Fatalf("partition: %v", err)
        }
        if got := queryCount.Load(); got > int64(expectedMax) {
            b.Fatalf("N+1 regression: %d queries (max %d)", got, expectedMax)
        }
    }
}
```

Running the full `go test ./...` executes benchmarks in their own phase; the ceiling assertion fails CI if a future refactor reintroduces an N+1.

### 1.5 Verify Commit 1

```bash
cd <beads-root>
go build ./...
go test ./internal/storage/issueops/... -run 'TestActiveWispIDsInTx|TestPartitionByWispInTx|TestWispRoutingExportParity' -v
go test -bench=BenchmarkBulkPartitioning -benchtime=3x ./internal/storage/issueops/...
go test ./...
golangci-lint run ./...

# Manual remote timing:
cd <bd-project>
make -C <beads-root> install   # put fresh bd at ~/.local/bin/bd
time bd export -o /tmp/commit1-probe.jsonl
```

**Gate.** `bd export` remote ≤ 10 s. Parity test green. Benchmark does not exceed its query-count ceiling. All existing tests green. Lint no new warnings. If not met: revert, return to Phase 1 of systematic debugging.

### 1.6 Commit 1

```bash
cd <beads-root>
bd export -o .beads/issues.jsonl   # refresh bd's own JSONL snapshot
git add internal/storage/issueops/wisp_routing.go \
        internal/storage/issueops/wisp_routing_test.go \
        internal/storage/issueops/wisp_routing_export_parity_test.go \
        internal/storage/issueops/dependencies.go \
        internal/storage/issueops/labels.go \
        internal/storage/issueops/comments.go \
        internal/storage/issueops/bulk_ops.go \
        internal/storage/issueops/dependency_queries.go \
        internal/storage/issueops/delete.go \
        .beads/issues.jsonl
git commit -m "$(cat <<'EOF'
perf(storage): batch wisp routing probes in bulk helpers

Replace per-ID IsActiveWispInTx loops with one batched
SELECT id FROM wisps WHERE id IN (...) via new ActiveWispIDsInTx +
PartitionByWispInTx helpers.

Eliminates a 2,285-query N+1 that dominated remote bd export wall time
(208s -> ~8s with a 15ms remote RTT). Touches 7 bulk helpers in
internal/storage/issueops/; single-ID call sites are unchanged.

PartitionByWispInTx preserves input ordering so JSON export ordering
remains deterministic. Mandatory benchmark asserts a query-count
ceiling to catch future N+1 regressions.

See docs/design/remote-latency-perf-design.md
EOF
)"
```

## Commit 2 — `perf(dolt): enable InterpolateParams to collapse prepared-statement round-trips`

### 2.1 DSN flag flip

**File:** `internal/storage/doltutil/dsn.go` (in `ServerDSN.String()`):

```go
cfg := mysql.Config{
    User:                 d.User,
    Passwd:               d.Password,
    Net:                  "tcp",
    Addr:                 fmt.Sprintf("%s:%d", d.Host, d.Port),
    DBName:               d.Database,
    ParseTime:            true,
    MultiStatements:      true,
    InterpolateParams:    true,   // added
    Timeout:              timeout,
    AllowNativePasswords: true,
}
```

### 2.2 Driver-behavior audit (enumerated, for the commit message)

Run each grep below, paste the raw results into a scratch file, and include the final distilled table in the commit message. This replaces the previously-vague "spot-check" language.

```bash
# a) Every Exec/Query arg type on server-mode write paths
grep -rn --include='*.go' -E "\.(ExecContext|QueryContext|QueryRowContext)\(" internal/storage/ cmd/bd/ | grep -v _test.go

# b) []byte / BLOB argument usage on write paths
grep -rn --include='*.go' -E "\.(Exec|Query)(Context)?\([^)]*\[\]byte" internal/ cmd/ | grep -v _test.go

# c) Prepare / Stmt reuse (expected: zero hits in non-test code)
grep -rn --include='*.go' -E "\.(Prepare|PrepareContext)\(|\bsql\.Stmt\b|\*sql\.Stmt" internal/ cmd/ | grep -v _test.go

# d) driver.Valuer implementations
grep -rn --include='*.go' -E "func .* Value\(\) \(driver\.Value" internal/types/

# e) json.RawMessage passed as Exec arg
grep -rn --include='*.go' "json.RawMessage" internal/storage/ cmd/bd/
```

Confirmed findings (from the spec §5 audit table — reproduce here for commit-message traceability):

- `string`, `int`, `int64`, `bool`, `time.Time`, `*time.Time`, `nil` — all interpolate identically to server-side PREPARE.
- `[]byte` appears only in `internal/storage/issueops/federation.go:32-46` and `internal/storage/dolt/credentials.go:166,258-265` (encrypted password); driver hex-literal escape applies.
- Zero `Prepare` / `Stmt` / `driver.Valuer` / `json.RawMessage`-as-Exec-arg hits.
- `MultiStatements=true` and `InterpolateParams=true` are orthogonal in `github.com/go-sql-driver/mysql@v1.9.3/connection.go:340-410`.

Paste the raw grep output + distilled table into the commit message body.

Record findings inline as comments in `dsn.go` at the `InterpolateParams: true` line.

### 2.3 Unit test — DSN flag presence

**File:** `internal/storage/doltutil/dsn_test.go` (new)

```go
package doltutil

import (
    "strings"
    "testing"
)

func TestServerDSN_InterpolateParams(t *testing.T) {
    dsn := ServerDSN{Host: "127.0.0.1", Port: 3307, User: "root"}.String()
    if !strings.Contains(dsn, "interpolateParams=true") {
        t.Fatalf("expected interpolateParams=true in DSN, got: %s", dsn)
    }
    // existing flags must still be present
    for _, want := range []string{"parseTime=true", "multiStatements=true", "allowNativePasswords=true"} {
        if !strings.Contains(dsn, want) {
            t.Fatalf("expected %s in DSN, got: %s", want, dsn)
        }
    }
}
```

### 2.4 Dolt-specific round-trip integration test (mandatory)

**File:** `internal/storage/dolt/interpolate_params_test.go` (new)

Uses the existing Dolt test harness (`testmain_test.go`). Opens two database handles against the same DB — one with `InterpolateParams=false`, one with `=true`. Inserts and retrieves edge-case values into the real `issues` table.

```go
func TestInterpolateParams_RoundTripParity(t *testing.T) {
    // ... spin up in-process Dolt via existing test helper ...
    cases := []struct {
        name      string
        issueID   string
        metadata  string                 // JSON column
        createdAt time.Time              // DATETIME column (schema is second-precision)
        title     string                 // String with metacharacters
    }{
        {"json_null_nested", "t-json",
            `{"a":null,"emoji":"\u00e9","nested":{"k":[1,2,3]}}`,
            time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
            "plain"},
        {"datetime_fractional", "t-dt",
            `{}`,
            time.Date(2026, 4, 18, 12, 34, 56, 123456789, time.UTC),
            "plain"},
        {"string_metachars", "t-str",
            `{}`,
            time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
            `O'Brien\n"quote" \\`},
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            out1 := roundTripThroughDB(t, /*interpolate=*/ false, tc)
            out2 := roundTripThroughDB(t, /*interpolate=*/ true, tc)
            if !reflect.DeepEqual(out1, out2) {
                t.Fatalf("interpolate=true produced different result:\n  off: %+v\n  on:  %+v", out1, out2)
            }
        })
    }
}
```

Intentionally **no** BLOB case — bd has no BLOB in parameterized write paths beyond `federation_peers.password_encrypted`, which is already covered by existing federation tests. The reviewer's BLOCKER 3 is closed by this test.

### 2.5 Verify Commit 2

```bash
cd <beads-root>
go test ./internal/storage/doltutil/... -v
go test ./internal/storage/dolt/... -run TestInterpolateParams -v
go test ./...
golangci-lint run ./...

make install
cd <bd-project>
time bd export -o /tmp/commit2-probe.jsonl
```

**Gate.** `bd export` remote ≤ 10 s (the §9 success target). Round-trip parity test green. Full suite green. Lint clean. If any test fails with new `time.Time` / JSON / string-escape mismatches, revert and open a bd issue for the specific incompatibility.

### 2.6 Commit 2

```bash
cd <beads-root>
bd export -o .beads/issues.jsonl
git add internal/storage/doltutil/dsn.go \
        internal/storage/doltutil/dsn_test.go \
        internal/storage/dolt/interpolate_params_test.go \
        .beads/issues.jsonl
git commit -m "$(cat <<'EOF'
perf(dolt): enable InterpolateParams to collapse prepared-statement RTTs

Set InterpolateParams: true in ServerDSN. go-sql-driver/mysql quotes
arguments client-side, so each parameterized query becomes a single
round-trip instead of the multi-RT binary protocol.

Combined with commit 1, this keeps remote bd export within the
10s success target (§9).

Driver-behavior audit (grepped against non-test code, see design §5):
- string/int/int64/bool/time.Time/nil interpolate identically
- []byte appears only in federation.go / credentials.go password paths,
  already hex-escaped by the driver
- zero Prepare/Stmt reuse in non-test code (no regression risk)
- no custom driver.Valuer in internal/types
- no json.RawMessage passed as Exec arg
- MultiStatements and InterpolateParams are orthogonal in
  go-sql-driver/mysql@v1.9.3/connection.go:340-410

Side benefit: Dolt query log now shows final interpolated SQL, which
is strictly better for EXPLAIN / post-mortem debugging.

Dolt-specific round-trip parity test added:
internal/storage/dolt/interpolate_params_test.go covers JSON, DATETIME
fractional seconds, and strings with SQL metacharacters.

ParseTime and MultiStatements remain set.

See docs/design/remote-latency-perf-design.md
EOF
)"
```

## Commit 3 — `perf(dolt): tune connection-pool settings in auxiliary paths`

### 3.1 Extract pool constants

**File:** `cmd/bd/doctor/pool_config.go` (new)

```go
// Package doctor — connection-pool constants for diagnostic / cleanup paths.
// Widened from the historical 2/1/30s to 5/2/60s:
//   - Historical values were chosen arbitrarily (no git-blame rationale).
//   - 5 open conns lets doctor probes run modestly concurrent without
//     saturating the shared Dolt server.
//   - 60s lifetime is defense-in-depth against any hypothetical session-
//     state drift (DOLT_CHECKOUT is not reachable from these call sites,
//     verified in design §5 Commit 3 branch-switching audit).
//   - The main long-lived daemon pool in internal/storage/dolt/store.go
//     uses 10/5/1h — these diagnostic pools intentionally stay narrower.
package doctor

import "time"

const DiagnosticPoolMaxOpenConns = 5
const DiagnosticPoolMaxIdleConns = 2
const DiagnosticPoolMaxLifetime  = 60 * time.Second
```

### 3.2 Update the three call sites to use the constants

**3.2.a** `cmd/bd/dolt.go` lines 1518-1520:

```go
import "github.com/steveyegge/beads/cmd/bd/doctor"   // add if missing

db.SetMaxOpenConns(doctor.DiagnosticPoolMaxOpenConns)
db.SetMaxIdleConns(doctor.DiagnosticPoolMaxIdleConns)
db.SetConnMaxLifetime(doctor.DiagnosticPoolMaxLifetime)
```

(If importing `cmd/bd/doctor` from `cmd/bd` creates a cycle, duplicate the three constants in `cmd/bd/dolt_pool.go` with a doc-comment pointing to `cmd/bd/doctor/pool_config.go` as the canonical source. Verify with `go build ./...` first.)

**3.2.b** `cmd/bd/doctor/server.go` lines 320-322 — same constants.

**3.2.c** `cmd/bd/doctor/dolt.go` lines 59-61 — same constants.

Do **not** touch any other `SetMaxOpenConns(1)` in the repository. See the explicit non-touch list in the spec.

### 3.3 Constants-used-by-call-sites test

**File:** `cmd/bd/doctor/pool_config_test.go` (new)

A simple sanity test that the three constants have the values the spec claims, and that they are neither the old historical defaults nor accidentally-zero.

```go
package doctor

import (
    "testing"
    "time"
)

func TestDiagnosticPoolConstants(t *testing.T) {
    if DiagnosticPoolMaxOpenConns != 5 {
        t.Errorf("MaxOpenConns = %d, want 5", DiagnosticPoolMaxOpenConns)
    }
    if DiagnosticPoolMaxIdleConns != 2 {
        t.Errorf("MaxIdleConns = %d, want 2", DiagnosticPoolMaxIdleConns)
    }
    if DiagnosticPoolMaxLifetime != 60*time.Second {
        t.Errorf("MaxLifetime = %v, want 60s", DiagnosticPoolMaxLifetime)
    }
}
```

### 3.4 Verify no correctness regression

Running the full test suite covers the concurrency/merge semantics of the widened paths:

```bash
cd <beads-root>
go test ./cmd/bd/... ./internal/... -race -count=1
```

If the `-race` run is too slow on the machine, restrict to the affected packages:

```bash
go test ./cmd/bd/doctor/... ./internal/storage/dolt/... -race -count=1
```

### 3.5 Verify Commit 3

```bash
make install
cd <bd-project>
time bd doctor
time bd dolt status   # if applicable; exercises cmd/bd/dolt.go path
```

**Gate.** No test regressions. `bd doctor` faster than pre-Commit-1 baseline (91 s) by a meaningful margin (expect ≥ 20 s improvement from Commit 1 alone; Commit 3 adds marginal gains).

### 3.6 Commit 3

```bash
cd <beads-root>
bd export -o .beads/issues.jsonl
git add cmd/bd/doctor/pool_config.go \
        cmd/bd/doctor/pool_config_test.go \
        cmd/bd/dolt.go \
        cmd/bd/doctor/server.go \
        cmd/bd/doctor/dolt.go \
        .beads/issues.jsonl
git commit -m "$(cat <<'EOF'
perf(dolt): tune connection-pool settings in auxiliary paths

Widen three auxiliary SQL pool configurations from 2/1/30s to 5/2/60s:
- cmd/bd/dolt.go (dolt subcommands; reachable only from clean-databases)
- cmd/bd/doctor/server.go (server-mode doctor probe, read-only fan-out)
- cmd/bd/doctor/dolt.go (dolt-specific doctor probe, read-only)

Values extracted into named constants in cmd/bd/doctor/pool_config.go
(DiagnosticPoolMaxOpenConns=5, MaxIdleConns=2, MaxLifetime=60s) with
a test asserting the intended values are used, so future audits are
trivial.

Branch-switching audit: none of the three call sites reach DOLT_CHECKOUT
(actual CHECKOUT sites are in internal/storage/versioncontrolops/).
The 60s lifetime is defense-in-depth — the reviewer-proposed cap
adopted even though no session-state risk was identified.

Embedded mode, execWithLongTimeout, and admin helpers
(EnsureGlobalDatabase, FlushWorkingSet) are intentionally left
unchanged — their MaxOpenConns=1 is a correctness invariant or
an isolation barrier, not an oversight.

See docs/design/remote-latency-perf-design.md
EOF
)"
```

## Final verification

After all three commits:

```bash
cd <beads-root>
git log --oneline alexmsu ^main | head
go test ./... 2>&1 | tail -20
golangci-lint run ./... 2>&1 | tail -20
make install

# Remote, hostB:
cd <bd-project>
time bd export -o /tmp/final-probe.jsonl
time git commit --allow-empty -m "post-perf-fixes smoke"
time bd doctor
time bd list --json > /dev/null
time bd ready --json > /dev/null
```

**Acceptance matrix.**

| Metric | Baseline | Target | Measured |
|---|---:|---:|---|
| `bd export` remote | 208 s | ≤ 10 s | _to record_ |
| `git commit --allow-empty` | 3m29s | ≤ 15 s | _to record_ |
| `bd doctor` | 91 s | noticeable drop | _to record_ |
| `go test ./...` | green | green | _to record_ |
| `golangci-lint` new warnings | 0 | 0 | _to record_ |

## Rollback

Each commit is reverted independently with `git revert <sha>`. The bulk partitioner from Commit 1 can stay even if the call sites are reverted (it's a pure addition). The DSN flag in Commit 2 is a one-line revert. Commit 3 is three isolated edits.

If a later bd upstream release introduces a conflicting change (for example, upstream shipping its own `ActiveWispIDsInTx` helper), resolve by taking upstream and re-porting the call-site edits.

## Hand-off notes

- After all commits merge into `main`, delete the measurement baselines at `/tmp/baseline-*.log` and `/tmp/commit*-probe.jsonl`.
- Follow-up items listed in spec §10 ("Out of Scope / Follow-ups" — bd daemon, RunCompatMigrations fast-path, local embedded-clone topology, bulk-helper observability) stay documented there; no separate tracker issues for this work.
- Update the project memory under `<claude-memory-dir>` with a short pointer: "remote-latency cause was N+1 wisp routing + 3-RT prepared-statement protocol, fixed on branch alexmsu".
