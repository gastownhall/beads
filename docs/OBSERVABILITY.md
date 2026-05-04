# Observability (OpenTelemetry)

Beads exports metrics via OTLP HTTP. Telemetry is **disabled by default** — zero overhead when no variable is set.

## Recommended local stack

| Service | Port | Role |
|---------|------|------|
| VictoriaMetrics | 8428 | OTLP metrics storage |
| VictoriaLogs | 9428 | OTLP log storage |
| Grafana | 9429 | Dashboards |

```bash
# From your personal stack's opentelemetry/ folder
docker compose up -d
```

## Configuration

Telemetry is **explicit opt-in**. Set `BD_OTEL_ENABLED=true` and configure the
exporter via standard OpenTelemetry SDK environment variables — both go in
your shell profile or workspace `.env`:

```bash
export BD_OTEL_ENABLED=true
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://localhost:8428/opentelemetry/api/v1/push
```

A standard `OTEL_*` variable alone will not turn bd telemetry on — `bd` won't
auto-activate from a machine-global `OTEL_*` setting that was set for some
other instrumented tool.

### Shell profile (recommended)

```bash
# ~/.zshrc or ~/.bashrc
export BD_OTEL_ENABLED=true
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://localhost:8428/opentelemetry/api/v1/push
```

### Environment variables

| Variable | Example | Description |
|----------|---------|-------------|
| `BD_OTEL_ENABLED` | `true` | **Master switch.** Activates telemetry. Without it, the variables below are ignored. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | `http://localhost:8428/opentelemetry/api/v1/push` | Push metrics to an OTLP HTTP receiver. |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | `http://localhost:9428/insert/opentelemetry/v1/logs` | Push logs to an OTLP HTTP receiver (reserved for future log export). |
| `OTEL_TRACES_EXPORTER` | `console` | Write spans to stderr (dev/debug). |
| `OTEL_METRICS_EXPORTER` | `console` | Write metrics to stderr (dev/debug). |
| `OTEL_SERVICE_NAME` | `bd` | Override the `service.name` resource attribute. |
| `OTEL_RESOURCE_ATTRIBUTES` | `deployment.environment=workstation,team=infra` | Extend or override resource attributes (comma-separated `key=value`). |
| `OTEL_SDK_DISABLED` | `true` | Force telemetry off even when `BD_OTEL_ENABLED=true` is set. |

### Resource attributes

Every metric and span carries the OTel resource describing the bd process:

| Attribute | Value | Notes |
|-----------|-------|-------|
| `service.name` | `bd` | Override with `OTEL_SERVICE_NAME`. |
| `service.version` | bd version | |
| `bd.prefix` | configured `issue_prefix` | Auto-stamped from the project's issue prefix (e.g. `myproject`, `infra`). Also stamped as a measurement attribute on every emitted metric — see below. |

Add anything else via `OTEL_RESOURCE_ATTRIBUTES`.

### Per-project metric partitioning (`bd_prefix` label)

`bd.prefix` is stamped on every emitted metric measurement, not only on the
resource. Why both: in the OTel→Prometheus translation, resource attributes
land on the `target_info` series and need a `target_info * on(job, instance)
group_left(bd_prefix) ...` join to be queryable on the metric itself — a
join that fails when the SDK doesn't emit an `instance` label, which it
often doesn't for short-lived CLI processes. Stamping the attribute on each
measurement makes `bd_prefix` a first-class Prom label on every series:

```promql
bd_storage_operations_total{bd_prefix="myproj"}
sum by (bd_prefix) (rate(bd_db_retry_count_total[5m]))
```

No join needed. All `bd_*` metrics listed below carry `bd_prefix`.

### Local debug mode

```bash
BD_OTEL_ENABLED=true OTEL_TRACES_EXPORTER=console OTEL_METRICS_EXPORTER=console bd list
```

### Legacy environment variables (deprecated)

The earlier `BD_OTEL_*` data variables are honored for backwards
compatibility. Setting any of them activates telemetry on its own (no
`BD_OTEL_ENABLED=true` required) and translates to the standard OTLP equivalent
— a legacy value wins over a pre-existing `OTEL_*` value so a machine-global
`OTEL_*` setting cannot silently redirect bd telemetry. Each `bd` invocation
that sees one logs a one-line deprecation warning to stderr:

| Legacy | Standard equivalent |
|--------|---------------------|
| `BD_OTEL_METRICS_URL` | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` |
| `BD_OTEL_LOGS_URL` | `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` |
| `BD_OTEL_STDOUT=true` | `OTEL_TRACES_EXPORTER=console` + `OTEL_METRICS_EXPORTER=console` |

## Verification

```bash
bd list   # triggers metrics → visible in VictoriaMetrics
```

Verification queries in Grafana (VictoriaMetrics datasource):

```promql
bd_storage_operations_total                              # all projects
bd_storage_operations_total{bd_prefix="myproj"}          # one project
sum by (bd_prefix) (rate(bd_storage_operations_total[5m])) # split per project
```

---

## Metrics

> Every metric below carries `bd_prefix` (the project's issue prefix) as a
> Prom label in addition to the per-metric attributes shown. Filter or split
> dashboards with `{bd_prefix="..."}` directly — no `target_info` join needed.

### Storage (`bd_storage_*`)

| Metric | Type | Attributes | Description |
|--------|------|------------|-------------|
| `bd_storage_operations_total` | Counter | `db.operation` | Storage operations executed |
| `bd_storage_operation_duration_ms` | Histogram | `db.operation` | Operation duration (ms) |
| `bd_storage_errors_total` | Counter | `db.operation` | Storage errors |

> These metrics are emitted by `InstrumentedStorage`, the beads SDK wrapper.

### Dolt database (`bd_db_*`)

| Metric | Type | Attributes | Description |
|--------|------|------------|-------------|
| `bd_db_retry_count_total` | Counter | — | SQL retries in server mode |
| `bd_db_lock_wait_ms` | Histogram | `dolt_lock_exclusive` | Wait time to acquire database locks |

### Issues (`bd_issue_*`)

| Metric | Type | Attributes | Description |
|--------|------|------------|-------------|
| `bd_issue_count` | Gauge | `status` | Number of issues by status |

`status` values: `open`, `in_progress`, `closed`, `deferred`.

### AI (`bd_ai_*`)

| Metric | Type | Attributes | Description |
|--------|------|------------|-------------|
| `bd_ai_input_tokens_total` | Counter | `bd_ai_model` | Anthropic input tokens |
| `bd_ai_output_tokens_total` | Counter | `bd_ai_model` | Anthropic output tokens |
| `bd_ai_request_duration_ms` | Histogram | `bd_ai_model` | API call latency |

---

## Traces (spans)

Spans are only exported when `OTEL_TRACES_EXPORTER=console` — there is no trace backend in the recommended local stack.

| Span | Source | Description |
|------|--------|-------------|
| `bd.command.<name>` | CLI | Total duration of the command |
| `dolt.exec` / `dolt.query` / `dolt.query_row` | SQL | Each SQL operation |
| `dolt.commit` / `dolt.push` / `dolt.pull` / `dolt.merge` | Dolt VC | Version control procedures |
| `ephemeral.count` / `ephemeral.nuke` | SQLite | Ephemeral store operations |
| `hook.exec` | Hooks | Hook execution (root span, fire-and-forget) |
| `tracker.sync` / `tracker.pull` / `tracker.push` | Sync | Tracker sync phases |
| `anthropic.messages.new` | AI | Claude API calls |

### Notable attributes

**`bd.command.<name>`**

| Attribute | Description |
|-----------|-------------|
| `bd.command` | Subcommand name (`list`, `create`, ...) |
| `bd.version` | bd version |
| `bd.args` | Raw arguments passed to the command (e.g. "create 'title' -p 2") |
| `bd.actor` | Actor (resolved from git config / env) |

**`hook.exec`**

| Attribute / Event | Description |
|-------------------|-------------|
| `hook.event` | Event type (`create`, `update`, `close`) |
| `hook.path` | Absolute path to the script |
| `bd.issue_id` | ID of the triggering issue |
| event `hook.stdout` | Script standard output (truncated to 1 024 bytes) |
| event `hook.stderr` | Script error output (truncated to 1 024 bytes) |

The `hook.stdout` / `hook.stderr` events carry two attributes: `output` (the text) and `bytes` (original size before truncation).

---

## Architecture

```
cmd/bd/main.go
  └─ telemetry.Init()
      ├─ OTEL_TRACES_EXPORTER=console        → TracerProvider stdout
      ├─ OTEL_METRICS_EXPORTER=console       → MeterProvider stdout
      └─ OTEL_EXPORTER_OTLP_METRICS_ENDPOINT → MeterProvider HTTP → VictoriaMetrics

internal/storage/dolt/        → bd_db_* metrics + dolt.* spans
internal/storage/ephemeral/   → ephemeral.* spans
internal/hooks/               → hook.exec span
internal/tracker/             → tracker.* spans
internal/compact/             → bd_ai_* metrics + anthropic.* spans
internal/telemetry/storage.go → bd_storage_* metrics (SDK wrapper)
```

When no OpenTelemetry SDK environment variable selects an exporter,
`telemetry.Init()` installs **no-op** providers: hot paths execute only no-op
calls with no memory allocation.
