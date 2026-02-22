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

One variable is enough. Add it to your shell profile or workspace `.env`:

```bash
export BD_OTEL_METRICS_URL=http://localhost:8428/opentelemetry/api/v1/push
```

Every `bd` command will then automatically push its metrics.

### Shell profile (recommended)

```bash
# ~/.zshrc or ~/.bashrc
export BD_OTEL_METRICS_URL=http://localhost:8428/opentelemetry/api/v1/push
```

### Environment variables

| Variable | Example | Description |
|----------|---------|-------------|
| `BD_OTEL_METRICS_URL` | `http://localhost:8428/opentelemetry/api/v1/push` | Push metrics to VictoriaMetrics. Activates telemetry. |
| `BD_OTEL_STDOUT` | `true` | Write spans and metrics to stderr (dev/debug). Also activates telemetry. |

### Local debug mode

```bash
BD_OTEL_STDOUT=true bd list
```

## Verification

```bash
bd list   # triggers metrics → visible in VictoriaMetrics
```

Verification query in Grafana (VictoriaMetrics datasource):

```promql
bd_storage_operations_total
```

---

## Metrics

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
| `bd_db_lock_wait_ms` | Histogram | `dolt_lock_exclusive` | Wait time to acquire `dolt-access.lock` |

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

Spans are only exported when `BD_OTEL_STDOUT=true` — there is no trace backend in the recommended local stack.

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
      ├─ BD_OTEL_STDOUT=true  → TracerProvider stdout + MeterProvider stdout
      └─ BD_OTEL_METRICS_URL  → MeterProvider HTTP → VictoriaMetrics

internal/storage/dolt/        → bd_db_* metrics + dolt.* spans
internal/storage/ephemeral/   → ephemeral.* spans
internal/hooks/               → hook.exec span
internal/tracker/             → tracker.* spans
internal/compact/             → bd_ai_* metrics + anthropic.* spans
internal/telemetry/storage.go → bd_storage_* metrics (SDK wrapper)
```

When neither variable is set, `telemetry.Init()` installs **no-op** providers:
hot paths execute only no-op calls with no memory allocation.
