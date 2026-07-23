---
title: JSON 출력 스키마 계약
description: schema_version 봉투, 명령별 필드 및 소비자 지침을 다루는 bd --json 명령의 안정적인 JSON 출력 계약
---

Last reviewed: 2026-05-08

Freshness source: `cmd/bd/output.go`, `cmd/bd/errors.go`,
`cmd/bd/protocol/json_contract_test.go`.

`--json` 출력을 지원하는 모든 `bd` 명령은 `BD_JSON_ENVELOPE=1`을 설정해 응답을
통일된 봉투로 감쌀 수 있습니다. 이 형식은 v2.0에서 기본값이 됩니다.

## 마이그레이션 가이드

### 봉투 형식 선택

```bash
export BD_JSON_ENVELOPE=1
```

### 봉투 형식(BD_JSON_ENVELOPE=1, v2.0에서 기본값)

모든 `--json` 명령은 출력을 다음과 같이 감쌉니다.

```json
{"schema_version": 1, "data": <original-payload>}
```

원본 페이로드는 `.data` 안에서 그대로 유지됩니다. 유형 손상이나 필드 주입이 없습니다.
객체, 배열, 맵에서 동일하게 작동합니다.

### 소비자 업데이트

```bash
# 이전(레거시):
bd list --json | jq '.[0].id'
bd show beads-abc --json | jq '.title'

# 이후(봉투):
bd list --json | jq '.data[0].id'
bd show beads-abc --json | jq '.data.title'

# 버전 확인:
bd show beads-abc --json | jq '.schema_version'
```

### 일정

- **현재 릴리스**: 레거시 형식이 기본값입니다. 선택하려면 `BD_JSON_ENVELOPE=1`을 설정합니다.
  환경 변수 없이 `--json`을 사용하면 stderr에 사용 중단 공지가 표시됩니다.
- **v2.0**: 봉투가 기본값이 됩니다. 한 릴리스 주기 동안 임시 탈출구로
  `BD_JSON_ENVELOPE=0`을 사용할 수 있습니다.

## 스키마 버전

현재 버전: **1**

`schema_version` 필드는 다음 상황에서 증가하는 정수입니다.
- 필드를 추가하거나, 이름을 바꾸거나, 제거할 때
- 출력 구조가 변경될 때(예: 중첩 깊이)
- 필드 유형이 변경될 때(예: 문자열에서 정수로)

추가적 변경(새 선택적 필드)은 버전을 올리지 **않습니다**.

## 출력 형식

### 봉투 모드(BD_JSON_ENVELOPE=1)

모든 명령이 통일된 봉투를 출력합니다.

```json
{
  "schema_version": 1,
  "data": {
    "id": "beads-abc",
    "title": "예시 이슈",
    "status": "open"
  }
}
```

배열도 같은 방식으로 감쌉니다.

```json
{
  "schema_version": 1,
  "data": [
    {"id": "beads-abc", "title": "첫 번째"},
    {"id": "beads-def", "title": "두 번째"}
  ]
}
```

### 레거시 모드(v2.0 전까지 기본값)

### 객체 명령(show, create, close, update 등)

단일 이슈 또는 결과를 반환하는 명령은 데이터와 함께 `schema_version`을 최상위 필드로
포함하는 JSON 객체를 출력합니다.

```json
{
  "schema_version": 1,
  "id": "beads-abc",
  "title": "예시 이슈",
  "status": "open",
  "priority": 1,
  "issue_type": "task",
  "created_at": "2026-04-20T12:00:00Z"
}
```

### 목록 명령(list, ready, search, stale 등)

여러 항목을 반환하는 명령은 원시 JSON 배열을 출력합니다.

```json
[
  {"id": "beads-abc", "title": "첫 번째", ...},
  {"id": "beads-def", "title": "두 번째", ...}
]
```

### 오류 출력(stderr)

`--json`이 활성화된 상태의 오류는 stderr에 JSON을 출력합니다.

```json
{
  "schema_version": 1,
  "error": "issue not found: beads-xyz",
  "code": "not_found"
}
```

## 명령별 필드 계약

### bd list --json

항목별 필수 필드:
- `id`(string): 이슈 ID(예: "beads-abc")
- `title`(string): 이슈 제목
- `status`(string): open, in_progress, closed, deferred
- `priority`(number): 0~4
- `issue_type`(string): bug, feature, task, epic, chore
- `created_at`(string): RFC3339 타임스탬프

선택적 필드:
- `description`, `owner`, `updated_at`, `closed_at`
- `labels`(string[]): 연결된 레이블
- `dependencies`(object[]): 의존성 레코드
- `dependency_count`, `dependent_count`, `comment_count`(number)
- `parent`(string|null): 부모 이슈 ID

### bd ready --json

`bd list --json`과 같은 스키마입니다. 차단되지 않은 이슈만 남도록 항목을 필터링합니다.
각 항목에는 `dependency_count`, `dependent_count`, `comment_count`와 선택적 `parent`
필드가 포함됩니다.

### bd blocked --json

해결되지 않은 의존성에 차단된 이슈를 반환합니다.
각 항목에는 모든 표준 이슈 필드와 다음이 포함됩니다.
- `blocked_by_count`(number): 차단 의존성 수
- `blocked_by`(string[]): 차단 이슈 ID

### bd show --json

단일 객체를 반환합니다(`items`로 감싸지 않음). 목록 항목과 같은 필수 필드에 다음이
추가됩니다.
- `description`(string)
- `acceptance_criteria`(string)
- `dependencies`(object[]): 전체 의존성 레코드
- `comments`(object[]): 댓글 스레드

### `import --json`

`--json`이 활성화되면 요약 객체를 반환합니다.
- `source`(string): 파일 경로 또는 "stdin"
- `created`(number): 생성된 이슈 수
- `updated`(number): 업데이트된 기존 이슈 수
- `skipped`(number): 건너뛴 이슈 수(오래된 행 + 중복 제거)
- `dedup_skipped`(number): `--dedup` 제목 일치로 건너뛴 이슈 수
- `memories`(number): 가져온 메모리 레코드 수
- `ids`(string[]): 생성된 이슈 ID
- `updated_issues`(object[]): 업데이트로 변경된 항목의 이슈별 요약
- `tie_kept_local_ids`(string[]): 로컬 상태가 우선한 동일 `updated_at` 행
- `stale_skipped_ids`(string[]): 로컬 이슈보다 오래되어 건너뛴 행
- `skipped_dependencies`(string[]): 대상 ID가 없어 건너뛴 의존성 에지
- `dry_run`(boolean): `--dry-run` 활성화 여부

### bd export --json

봉투로 감싸지 않은 JSONL(한 줄에 JSON 객체 하나)을 출력합니다. 각 줄은 `_type`
(`"issue"` / `"memory"`)으로 구분되는 독립된 이슈 또는 메모리 레코드입니다.
내보내기 줄에는 `schema_version`이 **포함되지 않습니다**. 이 필드는 교환 스트림이 아니라
`--json` 명령 봉투에 속합니다. 교환 형식 자체의 버전 마커는 선택적 `_schema` 헤더
레코드(`{"_schema":"beads-jsonl/1"}`)이며 리더는 이를 건너뜁니다.

## 소비자 지침

1. 객체 출력에서 **`schema_version`을 확인**합니다. 버전이 예상보다 높으면 경고를
   기록하되 파싱을 시도합니다(추가적 변경은 이전 버전과 호환됨).

2. **목록 명령은** 출력을 JSON 배열로 직접 파싱합니다.

3. **알 수 없는 필드를 무시**합니다. 스키마 버전을 올리지 않고 새 필드가 추가될 수 있습니다.

4. `--format json`이 아니라 **`--json` 플래그를 사용**합니다. `--json` 플래그가 안정적인
   계약이며 `--format`은 사람이 읽는 변형용입니다.
