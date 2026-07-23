---
title: 관측 가능성(OpenTelemetry)
description: 로컬 VictoriaMetrics 및 Grafana 스택, 환경 변수, 메트릭 참조를 사용해 OpenTelemetry(OTLP)로 bd 메트릭과 추적 내보내기
---

Beads는 OTLP HTTP를 통해 메트릭을 내보냅니다. 텔레메트리는 **기본적으로 비활성화**되어 있으며 변수가 설정되지 않으면 오버헤드가 없습니다.

## 권장 로컬 스택

| 서비스 | 포트 | 역할 |
|---------|------|------|
| VictoriaMetrics | 8428 | OTLP 메트릭 저장소 |
| VictoriaLogs | 9428 | 향후 OTLP 로그 저장소용으로 예약됨 |
| Grafana | 9429 | 대시보드 |

```bash
# 개인 스택의 opentelemetry/ 폴더에서
docker compose up -d
```

## 구성

변수 하나면 충분합니다. 셸 프로필 또는 워크스페이스 `.env`에 추가하세요.

```bash
export BD_OTEL_METRICS_URL=http://localhost:8428/opentelemetry/api/v1/push
```

그러면 모든 `bd` 명령이 메트릭을 자동으로 푸시합니다.

로그 내보내기는 아직 구현되지 않았습니다. `BD_OTEL_LOGS_URL`은 향후 VictoriaLogs
내보내기 도구용으로 예약되어 있으며 현재는 텔레메트리를 활성화하지 않습니다.

### 셸 프로필(권장)

```bash
# ~/.zshrc 또는 ~/.bashrc
export BD_OTEL_METRICS_URL=http://localhost:8428/opentelemetry/api/v1/push
```

### 환경 변수

| 변수 | 예시 | 설명 |
|----------|---------|-------------|
| `BD_OTEL_METRICS_URL` | `http://localhost:8428/opentelemetry/api/v1/push` | 메트릭을 VictoriaMetrics로 푸시합니다. 텔레메트리를 활성화합니다. |
| `BD_OTEL_LOGS_URL` | `http://localhost:9428/insert/opentelemetry/v1/logs` | 향후 로그 내보내기용으로 예약되었습니다. 현재는 텔레메트리를 활성화하지 않습니다. |
| `BD_OTEL_STDOUT` | `true` | span과 메트릭을 stderr에 기록합니다(개발/디버그). 텔레메트리도 활성화합니다. |

### 로컬 디버그 모드

```bash
BD_OTEL_STDOUT=true bd list
```

## 확인

```bash
bd list   # 메트릭을 트리거하여 VictoriaMetrics에 표시
```

Grafana에서 확인하는 쿼리(VictoriaMetrics 데이터 소스):

```promql
bd_storage_operations_total
```

### 로컬에서 저장소 계측 확인

수집기를 실행하지 않고 저장소 데코레이터 체인이 연결되었는지 확인하려면 stdout
내보내기 도구와 함께 `bd`를 실행하고 stderr에서 `bd.storage.*` 레코드를 찾습니다.

```bash
BD_OTEL_STDOUT=true bd list 2>&1 | grep -F bd.storage.operations
```

저장소 호출(`GetReadyWork`, `GetIssue` 등)마다 적어도 한 줄이 있어야 합니다.
`bd.storage.*`와 `bd.issue.count`는 없지만 `bd.db.pool_*`가 있다면 저장소 데코레이터가
체인에 포함되지 않은 것입니다. `cmd/bd/storage_chain.go`의 `wireStorageDecorators`를
확인하세요.

---

## 메트릭

### 저장소(`bd_storage_*`)

| 메트릭 | 유형 | 속성 | 설명 |
|--------|------|------------|-------------|
| `bd_storage_operations_total` | Counter | `db.operation` | 실행된 저장소 작업 |
| `bd_storage_operation_duration_ms` | Histogram | `db.operation` | 작업 시간(ms) |
| `bd_storage_errors_total` | Counter | `db.operation` | 저장소 오류 |

> 이 메트릭은 beads SDK 래퍼인 `InstrumentedStorage`가 내보냅니다.

### Dolt 데이터베이스(`bd_db_*`)

| 메트릭 | 유형 | 속성 | 설명 |
|--------|------|------------|-------------|
| `bd_db_retry_count_total` | Counter | — | 서버 모드의 SQL 재시도 |
| `bd_db_lock_wait_ms` | Histogram | `dolt_lock_exclusive` | 데이터베이스 잠금 획득 대기 시간 |

### 이슈(`bd_issue_*`)

| 메트릭 | 유형 | 속성 | 설명 |
|--------|------|------------|-------------|
| `bd_issue_count` | Gauge | `status` | 상태별 이슈 수 |

`status` 값: `open`, `in_progress`, `closed`, `deferred`.

### AI (`bd_ai_*`)

| 메트릭 | 유형 | 속성 | 설명 |
|--------|------|------------|-------------|
| `bd_ai_input_tokens_total` | Counter | `bd_ai_model` | Anthropic 입력 토큰 |
| `bd_ai_output_tokens_total` | Counter | `bd_ai_model` | Anthropic 출력 토큰 |
| `bd_ai_request_duration_ms` | Histogram | `bd_ai_model` | API 호출 지연 시간 |

---

## 추적(span)

span은 `BD_OTEL_STDOUT=true`일 때만 내보냅니다. 권장 로컬 스택에는 추적 백엔드가 없습니다.

| Span | 소스 | 설명 |
|------|--------|-------------|
| `bd.command.<name>` | CLI | 명령의 총 실행 시간 |
| `dolt.exec` / `dolt.query` / `dolt.query_row` | SQL | 각 SQL 작업 |
| `dolt.commit` / `dolt.push` / `dolt.pull` / `dolt.merge` | Dolt VC | 버전 관리 절차 |
| `ephemeral.count` / `ephemeral.nuke` | SQLite | 임시 저장소 작업 |
| `hook.exec` | 훅 | 훅 실행(루트 span, 실행 후 잊기) |
| `tracker.sync` / `tracker.pull` / `tracker.push` | 동기화 | 트래커 동기화 단계 |
| `anthropic.messages.new` | AI | Claude API 호출 |

### 주요 속성

**`bd.command.<name>`**

| 속성 | 설명 |
|-----------|-------------|
| `bd.command` | 하위 명령 이름(`list`, `create` 등) |
| `bd.version` | bd 버전 |
| `bd.args` | 명령에 전달된 원시 인수(예: "create 'title' -p 2") |
| `bd.actor` | 행위자(git 구성 / 환경에서 확인) |

**`hook.exec`**

| 속성 / 이벤트 | 설명 |
|-------------------|-------------|
| `hook.event` | 이벤트 유형(`create`, `update`, `close`) |
| `hook.path` | 스크립트의 절대 경로 |
| `bd.issue_id` | 트리거한 이슈의 ID |
| 이벤트 `hook.stdout` | 스크립트 표준 출력(1,024바이트로 잘림) |
| 이벤트 `hook.stderr` | 스크립트 오류 출력(1,024바이트로 잘림) |

`hook.stdout` / `hook.stderr` 이벤트에는 `output`(텍스트)과 `bytes`(잘리기 전 원래 크기)라는 두 속성이 있습니다.

---

## 아키텍처

```
cmd/bd/main.go
  └─ telemetry.Init()
      ├─ BD_OTEL_STDOUT=true  → TracerProvider stdout + MeterProvider stdout
      └─ BD_OTEL_METRICS_URL  → MeterProvider HTTP → VictoriaMetrics

internal/storage/dolt/        → bd_db_* 메트릭 + dolt.* span
internal/storage/ephemeral/   → ephemeral.* span
internal/hooks/               → hook.exec span
internal/tracker/             → tracker.* span
internal/compact/             → bd_ai_* 메트릭 + anthropic.* span
internal/telemetry/storage.go → bd_storage_* 메트릭(SDK 래퍼)
```

두 변수 모두 설정되지 않으면 `telemetry.Init()`이 **no-op** 제공자를 설치합니다.
핫 패스에서는 메모리 할당 없이 no-op 호출만 실행됩니다.
