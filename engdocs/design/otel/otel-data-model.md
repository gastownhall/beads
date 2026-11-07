# OpenTelemetry Data Model

Last reviewed: 2026-11-07

Freshness source: `internal/telemetry/`, `internal/storage/dolt/store.go`,
`internal/compact/haiku.go`, `cmd/bd/find_duplicates.go`, and hook execution
code under `internal/hooks/` (plus `internal/storage/issueops/blocked_recheck_report.go`
and the tracker sync spans in `internal/tracker/engine.go`, which this schema
also covers).

Complete schema of all telemetry events emitted by Beads. Each event consists of:

1. **Span** (→ stdout/console only, when `OTEL_TRACES_EXPORTER=console`; the legacy `BD_OTEL_STDOUT=true` translates to that — no remote/OTLP trace backend is wired) with full structured attributes
2. **Metric counter/histogram/gauge** (→ an OTLP HTTP receiver such as VictoriaMetrics, only when `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` is set and `OTEL_METRICS_EXPORTER` selects `otlp`, its default; and/or the console when it selects `console`) for aggregation. There is no default endpoint: with no endpoint configured, metrics are not exported

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
| `tracker.*` | Tracker sync | ✅ Implemented (span only) |

---

## 1. Identity Hierarchy

### 1.1 Instance

The outermost grouping: the OTel **resource** attached to every span and metric, built once in `telemetry.Init` (`buildResource`) from the service identity, the SDK host and process detectors, and the environment.

| Attribute | Type | Description |
|---|---|---|
| `service.name` | string | `"bd"` (overridable with `OTEL_SERVICE_NAME`) |
| `service.version` | string | Current bd version string |
| `host.name` | string | System hostname (`resource.WithHost`) |
| `process.*` | various | SDK process detector (`resource.WithProcess`): pid, executable, command line, runtime, owner |
| *(any)* | string | Extra `key=value` pairs from `OTEL_RESOURCE_ATTRIBUTES` (`resource.WithFromEnv`), which can also override the above |

No `os.*` detector is installed.

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

`telemetry.WrapStorage` (`InstrumentedStorage`) instruments the methods of the core storage interface and every issueops role accessor: it emits `storage.<Method>` spans and, through the role decorators in `internal/telemetry/`, `storage.<Role>.<Method>` spans (e.g. `storage.IssueReader.Ready`, `storage.ReadyClaimer.ClaimNext`, `storage.Sweeper.Sweep`). The `DoltStorage` capability sub-interfaces (version control, history, sync, and so on) pass through unwrapped; they are covered by the `dolt.*` spans in §4 instead. The tables below are representative examples; the complete list is the inventory at the end of this section.

**Metric labels.** `InstrumentedStorage.op` puts `db.operation` plus the method's start attributes on the span and on the `bd.storage.operations` counter. `InstrumentedStorage.done` records `bd.storage.operation.duration` and `bd.storage.errors` with the method's start attributes only, **without** `db.operation`; role spans and the methods that pass no start attributes therefore record those two instruments with no attributes at all. Start attributes such as `bd.issue.id`, `bd.actor`, `bd.query` and `db.commit_msg` are per-request values, so the series they produce are high-cardinality. Result attributes set later on the span (`bd.result.count`, etc.) are never metric labels.

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

`storage.RunInIssueLifecycleTransaction` carries the same two attributes.

### `storage.GetStatistics`

Besides its span, a successful call records the `bd.issue.count` gauge once per status (`status` = `open`, `in_progress`, `closed`, `deferred`). This is the only place the gauge is recorded.

### Naming rule and complete inventory

Every storage span is named `storage.` + the operation name, and carries that
same bare name in the `db.operation` attribute
(`internal/telemetry/storage.go`, `InstrumentedStorage.op`). The sections above
detail the highest-traffic spans; the rest follow the identical shape, so they
are inventoried here rather than repeated.

There are two families.

**Direct storage operations** — `storage.<Method>`, one per storage method
(77 spans):

```
AddDependency AddIssueComment AddLabel CloseIssue CloseIssueChecked
CountDependencies CountDependents CountEvents CountIssueComments CountIssues
CountIssuesByGroup CreateIssue CreateIssues DeleteIssue GetAllConfig
GetAllEventsSince GetBlockedIssues GetConfig GetConfigByPrefix GetDependencies
GetDependenciesWithMetadata GetDependencyTree GetDependents
GetDependentsWithMetadata GetEpicsEligibleForClosure GetEvents GetIssue
GetIssueByExternalRef GetIssueComments GetIssueCommentsPage GetIssuesByIDs
GetIssuesByLabel GetLabels GetLocalMetadata GetProvenanceByRef
GetProvenanceEvents GetReadyWork GetReadyWorkWithCounts
GetReadyWorkWithCountsAndTotal GetStatistics IterAllEventsSince
IterBlockedIssues IterDependenciesWithMetadata IterDependentsWithMetadata
IterEvents IterIssueComments IterIssues IterReadyWork IterWisps ListWisps
MergeMetadata MergeSlotAcquire MergeSlotCheck MergeSlotCreate MergeSlotRelease
RecordProvenanceEvent RemoveDependency RemoveLabel RenameLabel ReopenIssue
RunInIssueLifecycleTransaction RunInTransaction SearchDescendants
SearchIssueIDs SearchIssues
SearchIssueSummaries SearchIssuesWithCounts SetConfig SetLocalMetadata
SlotClear SlotGet SlotSet UnclaimIssue UnclaimIssueIfAssignee UpdateIssue
UpdateIssueChecked UpdateIssueType
```

**Issueops role operations** — `storage.<Role>.<Method>`, emitted when a caller
goes through the guarded issueops surface instead of calling storage directly
(48 spans):

```
BatchApplier.ApplyBatch BatchCloser.CloseBatch BatchCreator.CreateBatch
BatchGetter.GetMany BlockedLister.ListBlocked
BlockingAnnotator.AnnotateBlocking Bootstrapper.Bootstrap Commenter.AddComment
Counter.Count Counter.CountByGroup CycleDetector.DetectCycles Deleter.Delete
DependencyEditor.AddDependencies DependencyEditor.RemoveDependency
EdgeReader.ReadEdges GraphCounter.CountEdges Heartbeater.Heartbeat
InitVerifier.VerifyIdentity IssueClaimer.Claim IssueOperations.Close
IssueOperations.Create IssueOperations.Reopen IssueOperations.Update
IssueReader.Get IssueReader.List IssueReader.Ready IssueRelations.Related
LeaseReclaimer.Reclaim Memories.Forget Memories.List Memories.Recall
Memories.Remember MetadataCAS.CompareAndSetKey MoleculeStepper.Advance
Querier.Query ReadyClaimer.ClaimNext ReadyCounter.CountReady
Releaser.Release StatsReporter.AssigneeStats StatsReporter.Stats
Sweeper.Sweep TreeWalker.WalkTree VersionReconciler.ReconcileVersion
VersionReconciler.RecordedVersion WorkspaceConfig.GetSetting
WorkspaceConfig.ListSettings WorkspaceConfig.SetSetting
WorkspaceConfig.UnsetSetting
```

`storage.GetConfigByPrefix` fires only when a caller holds the instrumented
store: `cmd/bd` discovers the prefix reader with `storage.UnwrapStore`, which
peels past this layer, so on that path no span is emitted.

The two families do **not** double-count. The role decorators wrap the
*uninstrumented* store (`InstrumentedStorage.IssueLifecycle` calls `Unwrap()`
before wrapping), so one logical operation through the issueops front door emits
its role span only — not a second `storage.<Method>` span beneath it. A create
via issueops is one `storage.IssueOperations.Create`; a create via direct
storage is one `storage.CreateIssue`. Operation counts are therefore comparable
across both paths (per `db.operation`); durations are not separable by
operation (see *Metric labels* above).

---

## 4. Dolt Backend Events

**Shared attributes**: every `dolt.*` span carries the fixed attribute set from `doltSpanAttrs()` — `db.system` (string, `"dolt"`), `db.readonly` (bool, whether the store is read-only), and `db.server_mode` (bool, currently always `true`). The tables below list only each span's additional attributes.

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

Emitted for DOLT_COMMIT operations. Carries only the shared `doltSpanAttrs()` (see the note at the top of this section) — the commit message no longer appears on this span. It survives only as `db.commit_msg` on the `storage.RunInTransaction` / `storage.RunInIssueLifecycleTransaction` spans (`internal/telemetry/storage.go:674,682`).

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
| `dolt.merge_strategy` | string | Conflict-resolution strategy (`ours` or `theirs`) |
| `dolt.conflicts` | int | Conflict count (set when conflicts are detected) |

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

Emitted for hook execution, as a root span (hooks run asynchronously, so the span has no parent). **Span only** — no metric counters or histograms exist for hooks. Duration aggregation is a Tier 3 roadmap item. On js/wasm builds hook execution is unsupported: the span is still emitted, and it ends with an error status (`hook execution is not supported on js/wasm`).

| Attribute | Type | Description |
|---|---|---|
| `hook.event` | string | Event type: `create`, `update` or `close` (the only hook events, run from `.beads/hooks/on_create`, `on_update`, `on_close`) |
| `hook.path` | string | Path to the hook script |
| `bd.issue_id` | string | ID of triggering issue |

Stdout/stderr are added as span **events** (not attributes), each only when non-empty:
- `hook.stdout` event: `output` (string, truncated to 1024 bytes), `bytes` (int, original size)
- `hook.stderr` event: `output` (string, truncated to 1024 bytes), `bytes` (int, original size)

---

## 7. AI Events

Emitted by the compaction engine (`bd compact`) via `internal/compact/haiku.go`, and by duplicate detection (`bd find-duplicates --method ai`) via `cmd/bd/find_duplicates.go`. Both use the Anthropic SDK with Anthropic-compatible credentials from `ANTHROPIC_API_KEY`, `MINIMAX_API_KEY`, or `ai.api_key`.

> **Note**: Only `compact/haiku.go` records to the `bd.ai.*` OTel metric instruments. `find_duplicates.go` records token counts and duration as span attributes only.

### `anthropic.messages.new`

One span per Anthropic API call. The `bd.ai.operation` attribute distinguishes the two callers.

| Attribute | Type | Description |
|---|---|---|
| `bd.ai.model` | string | Model used (default `ai.model` is `"claude-haiku-4-5-20251001"`; a MiniMax key defaults to `MINIMAX_MODEL` or `"MiniMax-M2"`) |
| `bd.ai.operation` | string | `"compact"` or `"find_duplicates"` |
| `bd.ai.input_tokens` | int | Input tokens consumed |
| `bd.ai.output_tokens` | int | Output tokens generated |
| `bd.ai.attempts` | int | Number of attempts, including retries (`compact` only) |
| `bd.ai.batch_size` | int | Candidate pairs evaluated (`find_duplicates` only) |
| `bd.ai.duration_ms` | float | Request duration in ms (`find_duplicates` only; `compact` records the `bd.ai.request.duration` metric instead) |

**Retry policy** (compact only; `find_duplicates` makes a single unretried call): exponential backoff from 1 s (1 s, 2 s, 4 s), up to 4 attempts (1 initial + up to 3 retries, `maxRetries = 3`), on HTTP 429, 5xx, and network timeout errors. Context cancellation and deadline expiry are not retried.

---

## 7a. Tracker Sync Events

Emitted by the external-tracker sync engine (`internal/tracker/engine.go`, used by the `bd ado`, `bd github`, `bd gitlab`, `bd jira`, `bd linear` and `bd notion` integrations). **Span only.**

| Span | Attributes |
|---|---|
| `tracker.sync` | `sync.tracker` (string), `sync.pull`, `sync.push`, `sync.dry_run` (bool); on completion `sync.pulled`, `sync.pushed`, `sync.conflicts`, `sync.created`, `sync.updated`, `sync.skipped`, `sync.errors` (int) |
| `tracker.pull` | `sync.tracker`, `sync.dry_run`; on completion `sync.created`, `sync.updated`, `sync.skipped` |
| `tracker.push` | `sync.tracker`, `sync.dry_run`; on completion `sync.created`, `sync.updated`, `sync.skipped`, `sync.errors` |
| `tracker.detect_conflicts` | `sync.tracker`; `sync.conflicts` |

---

## 8. Metrics Reference

| Metric (code name) | Type | Labels | Status |
|--------|------|--------|--------|
| `bd.storage.operations` | Counter | `db.operation` + method start attributes | ✅ Implemented |
| `bd.storage.operation.duration` | Histogram (ms) | method start attributes; no `db.operation` (§3) | ✅ Implemented |
| `bd.storage.errors` | Counter | method start attributes; no `db.operation` (§3) | ✅ Implemented |
| `bd.issue.count` | Gauge | `status` | ✅ Implemented (recorded on `GetStatistics`) |
| `bd.db.retry_count` | Counter | — | ✅ Implemented |
| `bd.db.lock_wait_ms` | Histogram (ms) | — | 🔲 Registered, not recorded |
| `bd.db.circuit_trips` | Counter | — | ✅ Implemented |
| `bd.db.circuit_rejected` | Counter | — | ✅ Implemented |
| `bd.db.serialization_errors` | Counter | — | ✅ Implemented |
| `bd.write_retries_total` | Counter | `type` (`serialization` \| `connection`) | ✅ Implemented |
| `bd.db.post_tx_commit_dropped` | Counter | — | ✅ Implemented |
| `bd.db.blocked_recheck_dropped` | Counter | — | ✅ Implemented |
| `bd.db.ignored_tx_fresh_pool` | Counter | — | ✅ Implemented |
| `bd.db.conn_acquire_ms` | Histogram (ms) | — | ✅ Implemented |
| `bd.db.pool_wait_count` | Counter | — | ✅ Implemented |
| `bd.db.pool_wait_ms` | Histogram (ms) | — | ✅ Implemented |
| `bd.db.pool_open` | Observable gauge | — | ✅ Implemented |
| `bd.db.pool_in_use` | Observable gauge | — | ✅ Implemented |
| `bd.db.pool_idle` | Observable gauge | — | ✅ Implemented |
| `bd.db.pool_max_open` | Observable gauge | — | ✅ Implemented |
| `bd.claim_verify_lost_total` | Counter | `op` (`claim` \| `unclaim` \| `guarded-update` \| `ready-claim`) | ✅ Implemented |
| `bd.claim_verify_recovered_total` | Counter | `op`, `outcome` | 🔲 Registered, not recorded |
| `bd.ai.input_tokens` | Counter | `bd.ai.model` | ✅ Implemented (compact only) |
| `bd.ai.output_tokens` | Counter | `bd.ai.model` | ✅ Implemented (compact only) |
| `bd.ai.request.duration` | Histogram (ms) | `bd.ai.model` | ✅ Implemented (compact only) |

---

## 9. Recommended Indexed Attributes

```
host.name, bd.command, bd.version, bd.actor, db.operation, db.statement,
bd.issue.id, bd.issue.type, hook.event, hook.path, bd.ai.model, bd.ai.operation,
sync.tracker
```

---

## 10. Configuration and Backend

Environment variables, backend compatibility, Dolt system tables, and roadmap are documented in [otel-architecture.md](otel-architecture.md) to avoid duplication.

Key variables: `BD_OTEL_ENABLED=true` (master switch) plus the standard SDK vars — `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` (or the generic `OTEL_EXPORTER_OTLP_ENDPOINT`), `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` (reserved; no log exporter is installed), `OTEL_TRACES_EXPORTER=console`, `OTEL_METRICS_EXPORTER` (comma-separated `otlp`/`console`/`none`, unset = `otlp`), `OTEL_SERVICE_NAME`/`OTEL_RESOURCE_ATTRIBUTES`, and `OTEL_SDK_DISABLED=true` (forces telemetry off even with `BD_OTEL_ENABLED=true`). Console metrics flush every 15 s, OTLP metrics every 30 s. The legacy `BD_OTEL_METRICS_URL`/`BD_OTEL_LOGS_URL`/`BD_OTEL_STDOUT` trio is deprecated but still honored: each is translated to its `OTEL_*` equivalent with a stderr deprecation warning (commit `1cf7490c7`, #3859).

---

## Appendix: Source Reference Audit

Audited against **`main` @ `087a4ec58`** (2026-11-07). All line numbers below refer to that commit.

Every metric name, span name, and attribute listed in this document is backed by a specific source location. This table exists to prevent documentation drift and to make re-verification straightforward after code changes. Paths are relative to the directory in the section heading unless given in full.

### Metrics (`internal/telemetry/storage.go`, `internal/storage/dolt/`, `internal/compact/haiku.go`)

| Metric (SDK name) | Type | Source |
|-------------------|------|--------|
| `bd.storage.operations` | Counter | `storage.go:47`; recorded in `op` (`storage.go:82`) |
| `bd.storage.operation.duration` | Histogram | `storage.go:50`; recorded in `done` (`storage.go:89`) |
| `bd.storage.errors` | Counter | `storage.go:54`; recorded in `done` |
| `bd.issue.count` | Gauge | `storage.go:57`; recorded at `storage.go:587-590` (`GetStatistics`) |
| `bd.db.retry_count` | Counter | `store.go:992`; recorded at `store.go:912` |
| `bd.db.lock_wait_ms` | Histogram | `store.go:996` — registered; `.Record()` not called anywhere |
| `bd.db.circuit_trips` | Counter | `store.go:1000`; recorded at `store.go:961,1347,4518` |
| `bd.db.circuit_rejected` | Counter | `store.go:1004`; recorded at `store.go:888,901,1298,2180` |
| `bd.db.serialization_errors` | Counter | `store.go:1008`; recorded at `store.go:1322,1335`, `ephemeral_routing.go:502` |
| `bd.write_retries_total` | Counter | `store.go:1012`; recorded at `store.go:1323,1336,1343`, `ephemeral_routing.go:503` (`type=serialization\|connection`) |
| `bd.db.post_tx_commit_dropped` | Counter | `store.go:1016`; recorded at `store.go:1466`, `transaction.go:409`, `issue_operations_tx.go:99`, `blocked_recheck_raw.go:68` |
| `bd.db.conn_acquire_ms` | Histogram | `store.go:1020`; recorded at `transaction.go:215` |
| `bd.db.pool_wait_count` | Counter | `store.go:1024`; recorded at `transaction.go:222` |
| `bd.db.pool_wait_ms` | Histogram | `store.go:1028`; recorded at `transaction.go:224` |
| `bd.claim_verify_lost_total` | Counter | `store.go:1032`; recorded at `claim_verify.go:208`, `issues.go:448` |
| `bd.claim_verify_recovered_total` | Counter | `store.go:1036` — registered; `.Add()` not called anywhere |
| `bd.db.ignored_tx_fresh_pool` | Counter | `store.go:1040`; recorded at `transaction.go:449` |
| `bd.db.pool_open` / `pool_in_use` / `pool_idle` / `pool_max_open` | Observable gauge | `store.go:1053,1061,1069,1077` (`registerPoolGauges`, called at `store.go:2560`) |
| `bd.db.blocked_recheck_dropped` | Counter | `internal/storage/issueops/blocked_recheck_report.go:17`; recorded at `:37` |
| `bd.ai.input_tokens` | Counter | `haiku.go:145`; recorded at `haiku.go:195` |
| `bd.ai.output_tokens` | Counter | `haiku.go:149`; recorded at `haiku.go:196` |
| `bd.ai.request.duration` | Histogram | `haiku.go:153`; recorded at `haiku.go:197` |

### Spans and attributes

| Span name | Attributes | Source |
|-----------|-----------|--------|
| `bd.command.<name>` | `bd.command`, `bd.version`, `bd.args` (scrubbed) | built in `cmd/bd/command_telemetry.go` (`startCommandSpan`/`commandSpanAttrs`), called from `cmd/bd/main.go:1425` |
| `bd.command.<name>` | `bd.actor` (added later) | `cmd/bd/main.go:1988` |
| *(resource)* | `service.name`, `service.version`, `host.name`, `process.*`, `OTEL_RESOURCE_ATTRIBUTES` | `internal/telemetry/telemetry.go:138-148` (`buildResource`) |
| `storage.<op>` / `storage.<Role>.<Method>` (all methods) | `db.operation` + method-specific attrs | `internal/telemetry/storage.go:76-84` (role decorators throughout `internal/telemetry/`) |
| `storage.RunInTransaction` / `storage.RunInIssueLifecycleTransaction` | `db.commit_msg` | `internal/telemetry/storage.go:674,682` |
| `dolt.query` | `db.operation="query"`, `db.statement` + `doltSpanAttrs()` | `store.go:1725` |
| `dolt.exec` | `db.operation="exec"`, `db.statement` + `doltSpanAttrs()` | `store.go:1616` |
| `dolt.query_row` | `db.operation="query_row"`, `db.statement` + `doltSpanAttrs()` | `store.go:1754` |
| *(all `dolt.*`)* | `db.system`, `db.readonly`, `db.server_mode` | `store.go:1090-1102` (`doltSpanAttrs`); `db.statement` truncation at `store.go:1106` (`spanSQL`) |
| `dolt.commit` | `doltSpanAttrs()` only | `store.go:4063` |
| `dolt.push` / `dolt.force_push` | `dolt.remote`, `dolt.branch` | `store.go:5319-5327` |
| `dolt.pull` | `dolt.remote`, `dolt.branch` | `store.go:5434-5438` |
| `dolt.merge` | `dolt.merge_branch`; `dolt.conflicts` | `store.go:6326`; conflicts at `store.go:6348` |
| `dolt.merge_with_strategy` | `dolt.merge_branch`, `dolt.merge_strategy`; `dolt.conflicts` | `store.go:6373`; conflicts at `store.go:6401` |
| `dolt.branch` | `dolt.branch` | `store.go:6287-6290` |
| `dolt.checkout` | `dolt.branch` | `store.go:6304-6307` |
| `hook.exec` | `hook.event`, `hook.path`, `bd.issue_id` | `internal/hooks/hooks_unix.go:31`, `hooks_windows.go:30`, `hooks_wasm.go:20` |
| `hook.exec` events | `hook.stdout` / `hook.stderr` with `output`, `bytes` | `internal/hooks/hooks_otel.go:14, 20`; truncation `hooks.go:172` |
| `anthropic.messages.new` | `bd.ai.model`, `bd.ai.operation` | `haiku.go:161-166` |
| `anthropic.messages.new` | `bd.ai.input_tokens`, `bd.ai.output_tokens`, `bd.ai.attempts` | `haiku.go:199-203` |
| `anthropic.messages.new` | `bd.ai.batch_size`; `bd.ai.input_tokens`, `bd.ai.output_tokens`, `bd.ai.duration_ms` (find_duplicates) | `cmd/bd/find_duplicates.go:498-502`; `find_duplicates.go:520-524` |
| `tracker.sync` / `tracker.detect_conflicts` / `tracker.pull` / `tracker.push` | `sync.*` | `internal/tracker/engine.go:179, 286, 354, 1226` |
