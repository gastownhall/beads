# OpenTelemetry Data Model

Last reviewed: 2026-08-07

Freshness source: `internal/telemetry/`, `internal/storage/dolt/store.go`,
`internal/compact/haiku.go`, `cmd/bd/find_duplicates.go`, and hook execution
code under `internal/hooks/`.

Complete schema of all telemetry events emitted by Beads. Each event consists of:

1. **Span** (→ stdout/console only, when `OTEL_TRACES_EXPORTER=console`; the legacy `BD_OTEL_STDOUT=true` translates to that — no remote/OTLP trace backend is wired) with full structured attributes
2. **Metric counter/histogram** (→ any OTLP v1.x+ backend, defaults to VictoriaMetrics) for aggregation

Telemetry is hard opt-in: nothing is emitted unless `BD_OTEL_ENABLED=true` is set (a legacy `BD_OTEL_*` variable also activates it); standard `OTEL_*` variables on their own do not.

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
| `storage.<Method>` | Storage (direct) | ✅ Implemented |
| `storage.<Role>.<Method>` | Storage (issueops surface) | ✅ Implemented |
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
| `bd.args` | string | Full arguments passed to command, scrubbed via `scrubArgsForTelemetry` (secret-flag values and DSN userinfo redacted) |
| `bd.actor` | string | Actor identity (set after actor resolution) |

---

## 3. Storage Events

Every `storage.DoltStorage` method AND every issueops role method is instrumented: the facade wave's role decorators in `internal/telemetry/` emit `storage.<Method>` and `storage.<Role>.<Method>` spans (e.g. `storage.IssueReader.Ready`, `storage.ReadyClaimer.ClaimNext`, `storage.Sweeper.Sweep`), all feeding the same `bd.storage.*` metrics with `db.operation` as the label. The tables below are representative examples, not an exhaustive enumeration.

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

### Naming rule and complete inventory

Every storage span is named `storage.` + the operation name, and carries that
same bare name in the `db.operation` attribute
(`internal/telemetry/storage.go`, `InstrumentedStorage.op`). The sections above
detail the highest-traffic spans; the rest follow the identical shape, so they
are inventoried here rather than repeated.

There are two families.

**Direct storage operations** — `storage.<Method>`, one per storage method
(72 spans):

```
AddDependency AddIssueComment AddLabel CloseIssue CloseIssueChecked
CountDependencies CountDependents CountEvents CountIssueComments CountIssues
CountIssuesByGroup CreateIssue CreateIssues DeleteIssue GetAllConfig
GetAllEventsSince GetBlockedIssues GetConfig GetDependencies
GetDependenciesWithMetadata GetDependencyTree GetDependents
GetDependentsWithMetadata GetEpicsEligibleForClosure GetEvents GetIssue
GetIssueByExternalRef GetIssueComments GetIssueCommentsPage GetIssuesByIDs
GetIssuesByLabel GetLabels GetLocalMetadata GetProvenanceByRef
GetProvenanceEvents GetReadyWork GetReadyWorkWithCounts GetStatistics
IterAllEventsSince IterBlockedIssues IterDependenciesWithMetadata
IterDependentsWithMetadata IterEvents IterIssueComments IterIssues
IterReadyWork IterWisps ListWisps MergeMetadata MergeSlotAcquire
MergeSlotCheck MergeSlotCreate MergeSlotRelease RecordProvenanceEvent
RemoveDependency RemoveLabel ReopenIssue RunInIssueLifecycleTransaction
RunInTransaction SearchIssueIDs SearchIssues SearchIssuesWithCounts SetConfig
SetLocalMetadata SlotClear SlotGet SlotSet UnclaimIssue UnclaimIssueIfAssignee
UpdateIssue UpdateIssueChecked UpdateIssueType
```

**Issueops role operations** — `storage.<Role>.<Method>`, emitted when a caller
goes through the guarded issueops surface instead of calling storage directly
(39 spans):

```
BatchCloser.CloseBatch BatchCreator.CreateBatch
BlockingAnnotator.AnnotateBlocking Bootstrapper.Bootstrap Commenter.AddComment
Counter.Count Counter.CountByGroup CycleDetector.DetectCycles Deleter.Delete
DependencyEditor.AddDependencies DependencyEditor.RemoveDependency
EdgeReader.ReadEdges InitVerifier.VerifyIdentity IssueClaimer.Claim
IssueOperations.Close IssueOperations.Create IssueOperations.Reopen
IssueOperations.Update IssueReader.Get IssueReader.List IssueReader.Ready
IssueRelations.Related Memories.Forget Memories.List Memories.Recall
Memories.Remember Querier.Query ReadyClaimer.ClaimNext ReadyCounter.CountReady
StatsReporter.AssigneeStats StatsReporter.Stats Sweeper.Sweep
TreeWalker.WalkTree VersionReconciler.ReconcileVersion
VersionReconciler.RecordedVersion WorkspaceConfig.GetSetting
WorkspaceConfig.ListSettings WorkspaceConfig.SetSetting
WorkspaceConfig.UnsetSetting
```

The two families do **not** double-count. The role decorators wrap the
*uninstrumented* store (`InstrumentedStorage.IssueLifecycle` calls `Unwrap()`
before wrapping), so one logical operation through the issueops front door emits
its role span only — not a second `storage.<Method>` span beneath it. A create
via issueops is one `storage.IssueOperations.Create`; a create via direct
storage is one `storage.CreateIssue`. Counters and duration histograms are
therefore comparable across both paths.

---

## 4. Dolt Backend Events

**Shared attributes**: every `dolt.*` SQL span carries the fixed attribute set from `doltSpanAttrs()` — `db.system` (string, `"dolt"`), `db.readonly` (bool, whether the store is read-only), and `db.server_mode` (bool, currently always `true`). The tables below list only each span's additional attributes.

### `dolt.query`

Emitted for each SQL read query via `queryContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"query"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |

### `dolt.exec`

Emitted for each SQL write statement via `execContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"exec"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |

### `dolt.query_row`

Emitted for single-row queries via `queryRowContext()`.

| Attribute | Type | Description |
|---|---|---|
| `db.operation` | string | `"query_row"` |
| `db.statement` | string | SQL statement (truncated to 300 chars) |

### `dolt.commit`

Emitted for DOLT_COMMIT operations. Carries only the shared `doltSpanAttrs()` (see the note at the top of this section) — the commit message no longer appears on this span. It survives only as `db.commit_msg` on the `storage.RunInTransaction` / `storage.RunInIssueLifecycleTransaction` spans (`internal/telemetry/storage.go:556,564`).

### `dolt.push`

Emitted for DOLT_PUSH operations. The span name is `dolt.push`, or `dolt.force_push` for force pushes.

| Attribute | Type | Description |
|---|---|---|
| `dolt.remote` | string | Remote being pushed to |
| `dolt.branch` | string | Branch being pushed |

### `dolt.pull`

Emitted for DOLT_PULL operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.remote` | string | Remote being pulled from |
| `dolt.branch` | string | Branch being pulled |

### `dolt.merge`

Emitted for DOLT_MERGE operations.

| Attribute | Type | Description |
|---|---|---|
| `dolt.merge_branch` | string | Branch being merged |
| `dolt.conflicts` | int | Conflict count (set when conflicts are detected) |

### `dolt.merge_with_strategy`

Emitted for `bd vc merge --strategy` merges (pinned-connection merge/resolve/commit sequence).

| Attribute | Type | Description |
|---|---|---|
| `dolt.merge_branch` | string | Branch being merged |
| `dolt.merge_strategy` | string | Conflict-resolution strategy |

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

Emitted by the compaction engine (`bd compact`) via `internal/compact/haiku.go`, and by duplicate detection (`bd find-duplicates --method ai`) via `cmd/bd/find_duplicates.go`. Both use the Anthropic SDK with Anthropic-compatible credentials from `ANTHROPIC_API_KEY`, `MINIMAX_API_KEY`, or `ai.api_key`.

> **Note**: Only `compact/haiku.go` records to the `bd.ai.*` OTel metric instruments. `find_duplicates.go` records token counts and duration as span attributes only.

### `anthropic.messages.new`

One span per Anthropic API call. The `bd.ai.operation` attribute distinguishes the two callers.

| Attribute | Type | Description |
|---|---|---|
| `bd.ai.model` | string | Model used (e.g. `"claude-haiku-4-5"`) |
| `bd.ai.operation` | string | `"compact"` or `"find_duplicates"` |
| `bd.ai.input_tokens` | int | Input tokens consumed |
| `bd.ai.output_tokens` | int | Output tokens generated |
| `bd.ai.attempts` | int | Number of attempts, including retries (`compact` only) |
| `bd.ai.batch_size` | int | Candidate pairs evaluated (`find_duplicates` only) |
| `bd.ai.duration_ms` | float | Request duration in ms (`find_duplicates` only; `compact` records the `bd.ai.request.duration` metric instead) |

**Retry policy** (compact only; `find_duplicates` makes a single unretried call): exponential backoff, up to 4 attempts (1 initial + up to 3 retries, `maxRetries = 3`), on HTTP 429, 5xx, and network timeout errors.

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
| `bd.db.serialization_errors` | Counter | — | ✅ Implemented |
| `bd.write_retries_total` | Counter | `type` (`serialization` \| `connection`) | ✅ Implemented |
| `bd.db.conn_acquire_ms` | Histogram | — | ✅ Implemented |
| `bd.db.pool_wait_count` | Counter | — | ✅ Implemented |
| `bd.db.pool_wait_ms` | Histogram | — | ✅ Implemented |
| `bd.claim_verify_lost_total` | Counter | `op` (`claim` \| `unclaim` \| `guarded-update` \| `ready-claim`) | ✅ Implemented |
| `bd.claim_verify_recovered_total` | Counter | `op`, `outcome` | 🔲 Registered, not recorded |
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

Key variables: `BD_OTEL_ENABLED=true` (master switch) plus the standard SDK vars — `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`, `OTEL_TRACES_EXPORTER`/`OTEL_METRICS_EXPORTER=console`, `OTEL_SDK_DISABLED`. The legacy `BD_OTEL_METRICS_URL`/`BD_OTEL_LOGS_URL`/`BD_OTEL_STDOUT` trio is deprecated but still honored: each is translated to its `OTEL_*` equivalent with a stderr deprecation warning (commit `1cf7490c7`, #3859).

---

## Appendix: Source Reference Audit

Audited against **`main` @ `5eb7c25dd`**. All line numbers below refer to that commit.

Every metric name, span name, and attribute listed in this document is backed by a specific source location. This table exists to prevent documentation drift and to make re-verification straightforward after code changes.

### Metrics (`internal/telemetry/storage.go`, `internal/storage/dolt/store.go`, `internal/compact/haiku.go`)

| Metric (SDK name) | Type | Source |
|-------------------|------|--------|
| `bd.storage.operations` | Counter | `storage.go:46` — `m.Int64Counter("bd.storage.operations")` |
| `bd.storage.operation.duration` | Histogram | `storage.go:49` — `m.Float64Histogram("bd.storage.operation.duration")` |
| `bd.storage.errors` | Counter | `storage.go:53` — `m.Int64Counter("bd.storage.errors")` |
| `bd.issue.count` | Gauge | `storage.go:56` — `m.Int64Gauge("bd.issue.count")` |
| `bd.db.retry_count` | Counter | `store.go:872` — `m.Int64Counter("bd.db.retry_count")` |
| `bd.db.lock_wait_ms` | Histogram | `store.go:876` — registered; `.Record()` not called anywhere |
| `bd.db.circuit_trips` | Counter | `store.go:880` — `m.Int64Counter("bd.db.circuit_trips")` |
| `bd.db.circuit_rejected` | Counter | `store.go:884` — `m.Int64Counter("bd.db.circuit_rejected")` |
| `bd.db.serialization_errors` | Counter | `store.go:888` — registered; recorded at `store.go:1103,1116` |
| `bd.write_retries_total` | Counter | `store.go:892` — registered; recorded at `store.go:1104,1117,1124` (`type=serialization\|connection`) |
| `bd.db.conn_acquire_ms` | Histogram | `store.go:896` — registered; recorded at `transaction.go:167` |
| `bd.db.pool_wait_count` | Counter | `store.go:900` — registered; recorded at `transaction.go:174` |
| `bd.db.pool_wait_ms` | Histogram | `store.go:904` — registered; recorded at `transaction.go:176` |
| `bd.claim_verify_lost_total` | Counter | `store.go:908` — registered; recorded at `claim_verify.go:196`, `issues.go:439` |
| `bd.claim_verify_recovered_total` | Counter | `store.go:912` — registered; `.Add()` not called anywhere |
| `bd.ai.input_tokens` | Counter | `haiku.go:117` — `m.Int64Counter("bd.ai.input_tokens")` |
| `bd.ai.output_tokens` | Counter | `haiku.go:121` — `m.Int64Counter("bd.ai.output_tokens")` |
| `bd.ai.request.duration` | Histogram | `haiku.go:125` — `m.Float64Histogram("bd.ai.request.duration")` |

### Spans and attributes

| Span name | Attributes | Source |
|-----------|-----------|--------|
| `bd.command.<name>` | `bd.command`, `bd.version`, `bd.args` (scrubbed) | built in `cmd/bd/command_telemetry.go` (`startCommandSpan`/`commandSpanAttrs`), called from `cmd/bd/main.go:938` |
| `bd.command.<name>` | `bd.actor` (added later) | `cmd/bd/main.go:1367` |
| `storage.<op>` / `storage.<Role>.<Method>` (all methods) | `db.operation` + method-specific attrs | `internal/telemetry/storage.go:75-76` (role decorators throughout `internal/telemetry/`) |
| `dolt.query` | `db.operation="query"`, `db.statement` + `doltSpanAttrs()` | `store.go:1309` |
| `dolt.exec` | `db.operation="exec"`, `db.statement` + `doltSpanAttrs()` | `store.go:1168` |
| `dolt.query_row` | `db.operation="query_row"`, `db.statement` + `doltSpanAttrs()` | `store.go:1338` |
| `dolt.commit` | `doltSpanAttrs()` only | `store.go:2901` |
| `dolt.push` / `dolt.force_push` | `dolt.remote`, `dolt.branch` | `store.go:3674-3682` |
| `dolt.pull` | `dolt.remote`, `dolt.branch` | `store.go:3777-3781` |
| `dolt.merge` | `dolt.merge_branch`; `dolt.conflicts` | `store.go:4348`; conflicts at `store.go:4370` |
| `dolt.merge_with_strategy` | `dolt.merge_branch`, `dolt.merge_strategy` | `store.go:4395` |
| `dolt.branch` | `dolt.branch` | `store.go:4309-4312` |
| `dolt.checkout` | `dolt.branch` | `store.go:4326-4329` |
| `hook.exec` | `hook.event`, `hook.path`, `bd.issue_id` | `hooks_unix.go:31-36` |
| `hook.exec` events | `hook.stdout` / `hook.stderr` with `output`, `bytes` | `hooks_otel.go:14, 20` |
| `anthropic.messages.new` | `bd.ai.model`, `bd.ai.operation` | `haiku.go:133-138` |
| `anthropic.messages.new` | `bd.ai.input_tokens`, `bd.ai.output_tokens`, `bd.ai.attempts` | `haiku.go:171-175` |
| `anthropic.messages.new` | `bd.ai.batch_size`; `bd.ai.duration_ms` (find_duplicates only) | `find_duplicates.go:466-470`; `find_duplicates.go:491` |
