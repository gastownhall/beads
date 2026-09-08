# OpenTelemetry Data Model

Last reviewed: 2026-05-08

Freshness source: `internal/telemetry/`, `internal/storage/dolt/store.go`,
`internal/compact/haiku.go`, `cmd/bd/find_duplicates.go`, and hook execution
code under `internal/hooks/`.

Complete schema of all telemetry events emitted by Beads. Each event consists of:

1. **Span** (→ any OTLP v1.x+ backend, stdout when `BD_OTEL_STDOUT=true`) with full structured attributes
2. **Metric counter/histogram** (→ any OTLP v1.x+ backend, defaults to VictoriaMetrics) for aggregation

All command spans automatically carry `bd.command`, `bd.version`, `bd.args` from startup context; `bd.actor` is added after actor resolution.

---

## Metric Naming Convention

OTel SDK names use **dot notation** internally. Prometheus-compatible backends (VictoriaMetrics, Prometheus) export these as **underscore-separated** names, appending type suffixes:

| Code name (SDK) | Exported name (Prometheus/VM) |
|-----------------|-------------------------------|
| `bd.storage.operations` | `bd_storage_operations_total` |
| `bd.storage.operation.duration` | `bd_storage_operation_duration_ms` |
| `bd.storage.errors` | `bd_storage_errors_total` |
| `bd.issue.count` | `bd_issue_count` |
| `bd.db.retry_count` | `bd_db_retry_count_total` |
| `bd.db.lock_wait_ms` | `bd_db_lock_wait_ms` |
| `bd.db.circuit_trips` | `bd_db_circuit_trips_total` |
| `bd.db.circuit_rejected` | `bd_db_circuit_rejected_total` |
| `bd.ai.input_tokens` | `bd_ai_input_tokens_total` |
| `bd.ai.output_tokens` | `bd_ai_output_tokens_total` |
| `bd.ai.request.duration` | `bd_ai_request_duration_ms` |

---

## Event Index

| Event | Category | Status |
|-------|----------|--------|
| `bd.command` | CLI | ✅ Implemented |
| `storage.*` | Storage | ✅ Implemented |
| `dolt.*` | Dolt Backend | ✅ Implemented |
| `doltserver.*` | Server Lifecycle | 🔲 Roadmap (Tier 1) |
| `hook.exec` | Hooks | ✅ Implemented (span only) |
| `anthropic.messages.new` | AI | ✅ Implemented |

---

## 1. Identity Hierarchy

### 1.1 Instance

The outermost grouping. Derived at command startup time from the machine hostname and the working directory.

| Attribute | Type | Description |
|---|---|---|
| `host` | string | System hostname |
| `os` | string | System OS information |

### 1.2 Command

Each `bd` command execution generates a span with full context.

| Attribute | Type | Source |
|---|---|---|
| `bd.command` | string | Subcommand name (`create`, `list`, `show`, etc.) |
| `bd.version` | string | Current bd version string |
| `bd.args` | string | Full argument list |
| `bd.actor` | string | Actor identity — set after actor resolution (may lag span start) |

---

## 2. CLI Command Events

### `bd.command.<name>`

Emitted once per `bd` subcommand execution. Anchors all subsequent events for that command. The span name is `bd.command.` + command name (e.g. `bd.command.create`).

| Attribute | Type | Description |
|---|---|---|
| `bd.command` | string | Subcommand name |
| `bd.version` | string | bd version |
| `bd.args` | string | Full arguments passed to command |
| `bd.actor` | string | Actor identity (set after actor resolution) |

---

## 3. Storage Events

### `storage.CreateIssue`

Emitted when an issue is created.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"CreateIssue"` |
| `bd.issue.type` | string | Issue type (`task`, `epic`, `merge-request`, etc.) |
| `bd.actor` | string | Actor creating the issue |

### `storage.UpdateIssue`

Emitted when an issue is updated.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"UpdateIssue"` |
| `bd.issue.id` | string | Issue ID being updated |
| `bd.update.count` | int | Number of fields being updated |
| `bd.actor` | string | Actor updating the issue |

### `storage.GetIssue`

Emitted when an issue is retrieved.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"GetIssue"` |
| `bd.issue.id` | string | Issue ID being retrieved |

### `storage.SearchIssues`

Emitted when searching for issues.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"SearchIssues"` |
| `bd.query` | string | Search query string |
| `bd.result.count` | int | Number of results returned |

### `storage.GetReadyWork`

Emitted when querying for ready work.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"GetReadyWork"` |
| `bd.result.count` | int | Number of ready issues returned |

### `storage.GetBlockedIssues`

Emitted when querying for blocked issues.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"GetBlockedIssues"` |
| `bd.result.count` | int | Number of blocked issues returned |

### `storage.RunInTransaction`

Emitted when executing a transaction.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"RunInTransaction"` |
| `db.commit_msg` | string | Commit message |

---

## 4. Dolt Backend Events

### `dolt.query`

Emitted for each SQL read query via `queryContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"query"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |
| `db.system` | string | `"dolt"` |
| `db.readonly` | bool | Whether store is read-only |

### `dolt.exec`

Emitted for each SQL write statement via `execContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"exec"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |
| `db.system` | string | `"dolt"` |

### `dolt.query_row`

Emitted for single-row queries via `queryRowContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"query_row"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |
| `db.system` | string | `"dolt"` |

### `dolt.commit`

Emitted for DOLT_COMMIT operations.

| Attribute | Type | Description |
|---|---|---|
| `commit_msg` | string | Commit message |

### `dolt.push`

Emitted for DOLT_PUSH operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.branch` | string | Branch being pushed |

### `dolt.pull`

Emitted for DOLT_PULL operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.branch` | string | Branch being pulled |

### `dolt.merge`

Emitted for DOLT_MERGE operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.merge_branch` | string | Branch being merged |

### `dolt.branch`

Emitted for DOLT_BRANCH operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.branch` | string | Branch name |

### `dolt.checkout`

Emitted for DOLT_CHECKOUT operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.branch` | string | Branch being checked out |

---

## 5. Dolt Server Events (Roadmap — not yet implemented)

`internal/doltserver/` has no OTel imports. The events below are planned for Tier 1.

### `doltserver.start` *(planned)*

| Attribute | Type | Description |
|---|---|---|
| `port` | int | Port server is listening on |
| `data_dir` | string | Path to Dolt data directory |
| `pid` | int | Process ID of server |

### `doltserver.stop` *(planned)*

| Attribute | Type | Description |
|---|---|---|
| `pid` | int | Process ID of stopped server |
| `reason` | string | Stop reason (`graceful`, `forced`, `idle_timeout`, `crash`) |

---

## 6. Hooks Events

### `hook.exec`

Emitted for hook execution. **Span only** — no metric counters or histograms exist for hooks. Duration aggregation is a Tier 3 roadmap item.

| Attribute | Type | Description |
|---|---|---|
| `hook.event` | string | Event type (`create`, `update`, `close`, `delete`, etc.) |
| `hook.path` | string | Absolute path to hook script |
| `bd.issue_id` | string | ID of triggering issue |

Stdout/stderr are added as span **events** (not attributes):
- `hook.stdout` event: `output` (string, truncated), `bytes` (int, original size)
- `hook.stderr` event: `output` (string, truncated), `bytes` (int, original size)

---

## 7. AI Events

Emitted by the compaction engine (`bd compact`) via `internal/compact/haiku.go`, and by duplicate detection (`bd find-duplicates --method ai`) via `cmd/bd/find_duplicates.go`. Both use the Anthropic SDK directly via `ANTHROPIC_API_KEY`.

> **Note**: Only `compact/haiku.go` records to the `bd.ai.*` OTel metric instruments. `find_duplicates.go` records token counts as span attributes only.

### `anthropic.messages.new`

One span per Anthropic API call. The `bd.ai.operation` attribute distinguishes the two callers.

| Attribute | Type | Description |
|---|---|---|
| `bd.ai.model` | string | Model used (e.g. `"claude-haiku-4-5"`) |
| `bd.ai.operation` | string | `"compact"` or `"find_duplicates"` |
| `bd.ai.input_tokens` | int | Input tokens consumed |
| `bd.ai.output_tokens` | int | Output tokens generated |
| `bd.ai.attempts` | int | Number of attempts (including retries) |
| `bd.ai.batch_size` | int | Candidate pairs evaluated (`find_duplicates` only) |

**Retry policy**: exponential backoff, up to 3 attempts, on HTTP 429, 5xx, and network timeout errors.

---

## 8. Metrics Reference

| Metric (code name) | Type | Labels | Status |
|--------|------|--------|--------|
| `bd.storage.operations` | Counter | `db.operation` | ✅ Implemented |
| `bd.storage.operation.duration` | Histogram (ms) | `db.operation` | ✅ Implemented |
| `bd.storage.errors` | Counter | `db.operation` | ✅ Implemented |
| `bd.issue.count` | Gauge | `status` | ✅ Implemented |
| `bd.db.retry_count` | Counter | — | ✅ Implemented |
| `bd.db.lock_wait_ms` | Histogram | — | 🔲 Registered, not recorded |
| `bd.db.circuit_trips` | Counter | — | ✅ Implemented |
| `bd.db.circuit_rejected` | Counter | — | ✅ Implemented |
| `bd.ai.input_tokens` | Counter | `bd.ai.model` | ✅ Implemented (compact only) |
| `bd.ai.output_tokens` | Counter | `bd.ai.model` | ✅ Implemented (compact only) |
| `bd.ai.request.duration` | Histogram (ms) | `bd.ai.model` | ✅ Implemented (compact only) |

---

## 9. Recommended Indexed Attributes

```
host, os, bd.command, bd.version, bd.actor, db.operation, db.statement,
bd.issue.id, bd.issue.type, hook.event, hook.path, bd.ai.model, bd.ai.operation
```

---

## 10. Configuration and Backend

Environment variables, backend compatibility, Dolt system tables, and roadmap are documented in [otel-architecture.md](otel-architecture.md) to avoid duplication.

Key variables: `BD_OTEL_METRICS_URL`, `BD_OTEL_LOGS_URL`, `BD_OTEL_STDOUT`.

---

## Appendix: Source Reference Audit

Audited against **`main` @ `371df32b`**. All line numbers below refer to that commit.

Every metric name, span name, and attribute listed in this document is backed by a specific source location. This table exists to prevent documentation drift and to make re-verification straightforward after code changes.

### Metrics (`internal/telemetry/storage.go`, `internal/storage/dolt/store.go`, `internal/compact/haiku.go`)

| Metric (SDK name) | Type | Source |
|-------------------|------|--------|
| `bd.storage.operations` | Counter | `storage.go:38` — `m.Int64Counter("bd.storage.operations")` |
| `bd.storage.operation.duration` | Histogram | `storage.go:41` — `m.Float64Histogram("bd.storage.operation.duration")` |
| `bd.storage.errors` | Counter | `storage.go:45` — `m.Int64Counter("bd.storage.errors")` |
| `bd.issue.count` | Gauge | `storage.go:48` — `m.Int64Gauge("bd.issue.count")` |
| `bd.db.retry_count` | Counter | `store.go:302` — `m.Int64Counter("bd.db.retry_count")` |
| `bd.db.lock_wait_ms` | Histogram | `store.go:306` — registered; `.Record()` not called anywhere |
| `bd.db.circuit_trips` | Counter | `store.go:310` — `m.Int64Counter("bd.db.circuit_trips")` |
| `bd.db.circuit_rejected` | Counter | `store.go:314` — `m.Int64Counter("bd.db.circuit_rejected")` |
| `bd.ai.input_tokens` | Counter | `haiku.go:110` — `m.Int64Counter("bd.ai.input_tokens")` |
| `bd.ai.output_tokens` | Counter | `haiku.go:114` — `m.Int64Counter("bd.ai.output_tokens")` |
| `bd.ai.request.duration` | Histogram | `haiku.go:118` — `m.Float64Histogram("bd.ai.request.duration")` |

### Spans and attributes

| Span name | Attributes | Source |
|-----------|-----------|--------|
| `bd.command.<name>` | `bd.command`, `bd.version`, `bd.args` | `cmd/bd/main.go:262-266` |
| `bd.command.<name>` | `bd.actor` (added later) | `cmd/bd/main.go:474` |
| `storage.<op>` (all methods) | `db.operation` + method-specific attrs | `internal/telemetry/storage.go:62-69` |
| `dolt.query` | `db.operation="query"`, `db.statement`, `db.system="dolt"` | `store.go:400-405` |
| `dolt.exec` | `db.operation="exec"`, `db.statement`, `db.system="dolt"` | `store.go:363-368` |
| `dolt.query_row` | `db.operation="query_row"`, `db.statement`, `db.system="dolt"` | `store.go:429-434` |
| `dolt.commit` | `commit_msg` | `store.go:1086` |
| `dolt.push` | `dolt.branch` | `store.go:1231, 1266` |
| `dolt.pull` | `dolt.branch` | `store.go:1295` |
| `dolt.merge` | `dolt.merge_branch` | `store.go:1389` |
| `dolt.branch` | `dolt.branch` | `store.go:1357` |
| `dolt.checkout` | `dolt.branch` | `store.go:1372` |
| `hook.exec` | `hook.event`, `hook.path`, `bd.issue_id` | `hooks_unix.go:31-36` |
| `hook.exec` events | `hook.stdout` / `hook.stderr` with `output`, `bytes` | `hooks_otel.go:14, 20` |
| `anthropic.messages.new` | `bd.ai.model`, `bd.ai.operation` | `haiku.go:129-130` |
| `anthropic.messages.new` | `bd.ai.input_tokens`, `bd.ai.output_tokens`, `bd.ai.attempts` | `haiku.go:165-167` |
| `anthropic.messages.new` | `bd.ai.batch_size` (find_duplicates only) | `find_duplicates.go:433` |
