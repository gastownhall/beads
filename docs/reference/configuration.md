---
title: 구성
description: 우선순위, 비밀, 자동 커밋, 백업, 통합을 포함한 config.yaml 및 데이터베이스 저장 설정 전반의 bd 구성 전체 참조
---

beads의 전체 구성 참조입니다.

Last reviewed: 2026-07-10

Freshness source: `cmd/bd/main.go`, `cmd/bd/config.go`, `internal/configfile/`.

beads에는 서로 보완하는 두 가지 구성 시스템이 있습니다.

1. **도구 수준 구성**(YAML, [Viper](https://github.com/spf13/viper)로 관리) — 시작 플래그와 도구 동작을 `config.yaml` 파일에 저장합니다. 출력 형식, 자동 커밋 동작, CLI 사용성과 같은 사용자 환경 설정입니다.
2. **프로젝트 수준 구성**(`bd config`로 관리) — 통합 자격 증명, 상태 맵, 프로젝트별 설정을 Dolt 데이터베이스에 저장합니다. 일부 키는 대신 `config.yaml`로 라우팅됩니다(아래 [YAML 전용 키](#yaml-only-keys-startup-settings) 참조).

의도적으로 분리했습니다. 도구 설정은 사용자별이고 프로젝트 구성은 팀에서 공유하며 `bd dolt push`를 실행할 때 데이터베이스와 함께 이동합니다. 같은 이유로 데이터베이스에는 비밀 저장이 거부됩니다. [보안](#security-where-secrets-live)을 참조하세요.

Dolt가 유일한 저장소 백엔드입니다. 임베디드 모드(기본값)는 `.beads/embeddeddolt/`에 데이터를 저장하고 서버 모드(`bd init --server` 또는 `BEADS_DOLT_SERVER_MODE=1`)는 `.beads/dolt/`를 사용합니다. [Dolt 아키텍처](/architecture/dolt)를 참조하세요.

## 구성 위치

`config.yaml`은 다음 순서로 검색하며 뒤의 파일이 앞의 파일을 재정의합니다.

1. `~/.beads/config.yaml`(레거시 사용자 수준, 가장 낮은 우선순위)
2. `~/.config/bd/config.yaml`(사용자 수준, 네이티브 사용자 구성 디렉터리가 다른 플랫폼에서도 이 정확한 경로 확인)
3. `<repo>/.beads/config.yaml`(프로젝트 수준, 현재 디렉터리에서 위로 탐색)
4. `$BEADS_DIR/config.yaml`(가장 높은 우선순위, `BEADS_DIR`이 다른 워크스페이스를 가리킬 때)

커밋하면 안 되는 머신별 재정의를 위해 프로젝트 `config.yaml` 옆의 `config.local.yaml`도 마지막에 병합됩니다.

## 우선순위

Viper 관리(YAML) 키의 우선순위는 높은 순서부터 다음과 같습니다.

1. **명령줄 플래그**(예: `--json`, `--db`, `--actor`)
2. **환경 변수**(`BD_*`와 소수의 레거시 `BEADS_*` 이름 — 아래 참조)
3. **`config.yaml`** 파일(위에 나열한 순서)
4. **내장 기본값**

`bd config set`으로 작성한 프로젝트 수준 키(Jira, Linear, GitHub, 상태 맵 등)는 Dolt 데이터베이스에 저장됩니다. 명령 실행 시 읽으며 환경 변수 재정의는 없습니다.

config.yaml 값이나 환경 변수가 데이터베이스 키를 가리면 `bd config list`가 재정의 경고를 표시하고 `bd config show`는 모든 유효 키의 소스를 보고합니다.

## 구성 관리

```bash
# 값 설정(config.yaml 또는 데이터베이스로 자동 라우팅)
bd config set jira.url "https://company.atlassian.net"
bd config set validation.on-create warn   # YAML 전용 키

# 여러 값을 한 번에 설정(단일 자동 커밋, 쓰기 전 검증)
bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ

# 값 가져오기
bd config get jira.url
bd config get --json jira.url
# → {"key":"jira.url","value":"https://company.atlassian.net"}

# 데이터베이스에 저장된 모든 구성 나열(재정의 경고 포함)
bd config list

# 출처와 함께 모든 유효 구성 표시(환경 / config.yaml / 기본값 / 데이터베이스)
bd config show
bd config show --source config.yaml
bd config show --json

# 동기화 관련 구성 검증
bd config validate

# 값 제거
bd config unset jira.url
```

`bd config set`은 쓰기를 올바른 위치로 자동 라우팅합니다. YAML 네임스페이스의 키(아래 참조)는 프로젝트 `config.yaml`에 쓰고 나머지는 Dolt 데이터베이스에 씁니다. `beads.role`은 git 구성에 저장됩니다.

인식되지 않는 키는 유사 키 제안과 함께 경고를 표시합니다. 사용자 정의 키에는 `custom.*` 네임스페이스를 사용하세요.

<a id="yaml-only-keys-startup-settings"></a>

## YAML 전용 키(시작 설정)

이 키는 데이터베이스를 열기 전에 읽으므로 데이터베이스가 아니라 `config.yaml`에 있어야 합니다. `bd config set`으로 쓰면 `config.yaml`이 자동으로 업데이트됩니다.

YAML로 라우팅되는 전체 네임스페이스는 다음과 같습니다.

`routing.*`, `sync.*`, `git.*`, `directory.*`, `repos.*`, `external_projects.*`, `validation.*`, `hierarchy.*`, `ai.*`, `backup.*`, `export.*`, `dolt.*`, `federation.*`, `metrics.*`, `list.*`

다음 개별 키도 포함됩니다.

`no-db`, `json`, `db`, `actor`, `identity`, `no-push`, `no-git-ops`, `agent.profile`, `create.require-description`, `import.auto`, `import.path`, `prime.max-memories`, `prime.max-memory-chars`, 비밀 키 `github.token`, `gitlab.token`, `jira.api_token`, `ado.pat`, `linear.api_key`, `linear.oauth_client_id`, `linear.oauth_client_secret`.

이름에 `api_key`, `api-key`, `secret`, `token`, `password`가 포함된 키는 비밀로 취급됩니다. `--force-git-tracked`를 전달하지 않으면 git에서 추적되는 `config.yaml` 파일에 쓰기를 거부합니다. 대신 값을 환경 변수(예: `LINEAR_API_KEY`)로 내보내는 방식을 선호하세요.

## 도구 수준 설정(config.yaml)

| 설정 | 플래그 | 환경 변수 | 기본값 | 설명 |
|---|---|---|---|---|
| `json` | `--json` | `BD_JSON` | `false` | 스크립팅용 JSON 출력 |
| `db` | `--db` | `BD_DB` | (자동 검색) | 데이터베이스 경로 |
| `actor` | `--actor` | `BEADS_ACTOR` | `git config user.name` | 감사 추적의 행위자 이름([행위자 ID](#actor-identity-resolution) 참조) |
| `identity` | `--identity` | `BEADS_IDENTITY` | (git 사용자 / 호스트명) | `bd mail` 발신자 ID |
| `no-db` | `--no-db` | `BD_NO_DAEMON`(관련) | `false` | 데이터베이스를 열지 않고 실행 |
| `no-push` | `--no-push` | `BD_NO_PUSH` | `false` | `bd dolt push`에서 원격 푸시 건너뛰기 |
| `no-git-ops` | — | — | `false` | `bd prime` 종료 프로토콜의 git 작업 비활성화 |
| `agent.profile` | — | `BD_AGENT_PROFILE` | `conservative` | `bd prime`이 git/커밋 권한에 사용하는 정책 프로필: `conservative`, `minimal`, `team-maintainer`; 잘못된 값은 `conservative`로 대체 |
| `prime.max-memories` | `--max-memories` | `BD_PRIME_MAX_MEMORIES` | `0` | `bd prime`이 주입하는 지속 메모리 최대 개수(0 = 무제한) |
| `prime.max-memory-chars` | `--max-memory-chars` | `BD_PRIME_MAX_MEMORY_CHARS` | `0` | `bd prime`이 메모리 단위 경계에서 주입하는 메모리 항목의 최대 총 바이트(0 = 무제한) |
| `dolt.auto-commit` | `--dolt-auto-commit` | `BD_DOLT_AUTO_COMMIT` | `on` | 성공한 각 쓰기 후 Dolt 기록 커밋 생성([아래](#auto-commit-sql-commits-vs-dolt-commits) 참조) |
| `dolt.auto-push` | — | `BD_DOLT_AUTO_PUSH` | `false` | 쓰기 후 Dolt 원격에 자동 푸시(선택, [아래](#auto-push) 참조) |
| `dolt.auto-push-interval` | — | `BD_DOLT_AUTO_PUSH_INTERVAL` | `5m` | 자동 푸시 간 최소 시간 |
| `dolt.auto-push-timeout` | — | `BD_DOLT_AUTO_PUSH_TIMEOUT` | `30s` | 단일 자동 푸시 시도의 시간 제한 |
| `dolt.shared-server` | `--shared-server` | `BEADS_DOLT_SHARED_SERVER` | `false` | `~/.beads/shared-server/`에서 하나의 Dolt 서버 공유 |
| `dolt.max-conns` | — | `BEADS_DOLT_MAX_CONNS` | `10` | 연결 풀 크기 |
| `git.author` | — | `BD_GIT_AUTHOR` | (없음) | beads 커밋의 커밋 작성자 재정의 |
| `git.no-gpg-sign` | — | `BD_GIT_NO_GPG_SIGN` | `false` | beads 커밋의 GPG 서명 비활성화 |
| `create.require-description` | — | `BD_CREATE_REQUIRE_DESCRIPTION` | `false` | `bd create`에 설명 필수 |
| `validation.on-create` | — | `BD_VALIDATION_ON_CREATE` | `none` | 템플릿 검증: `none`, `warn`, `error` |
| `validation.on-close` | — | `BD_VALIDATION_ON_CLOSE` | `none` | 닫을 때 템플릿 검증 |
| `validation.on-sync` | — | `BD_VALIDATION_ON_SYNC` | `none` | 동기화 전 템플릿 검증 |
| `validation.metadata.mode` | — | — | `none` | 메타데이터 스키마 검증 |
| `hierarchy.max-depth` | — | — | `3` | 계층적 ID 최대 중첩 깊이 |
| `backup.enabled` | — | `BD_BACKUP_ENABLED` | `false` | `.beads/backup/`에 주기적인 Dolt 네이티브 백업 활성화([아래](#auto-backup) 참조) |
| `backup.interval` | — | `BD_BACKUP_INTERVAL` | `15m` | 자동 백업 간 최소 시간 |
| `backup.git-push` | — | — | `false` | 백업 저장소 자동 푸시 |
| `backup.git-repo` | — | `BD_BACKUP_GIT_REPO` | (없음) | 백업 git 저장소 URL. 설정하면 해당 저장소 안의 `backup/` 디렉터리에 백업 |
| `export.auto` | — | — | `false` | 각 쓰기 후 `.beads/issues.jsonl` 내보내기 새로 고침. 머신 간 동기화는 아님 |
| `export.path` | — | — | `issues.jsonl` | `.beads/` 기준 상대 출력 파일명 |
| `export.interval` | — | — | `60s` | 자동 내보내기 간 최소 시간 |
| `export.git-add` | — | — | `false` | 내보내기 파일에 `git add` 실행 |
| `import.auto` | — | `BD_IMPORT_AUTO` | `true` | 자동 JSONL 가져오기 마스터 스위치: Dolt 원격이 없을 때의 git 훅 대체 수단과 `.beads/issues.jsonl`은 있지만 데이터베이스가 비었을 때의 복구 가져오기. `false`는 모든 자동 가져오기를 비활성화하며 명시적 `bd import`는 항상 작동 |
| `import.path` | — | — | `issues.jsonl` | 암시적 JSONL 가져오기(`bd init --from-jsonl` 및 빈 DB 자동 가져오기 포함)에 사용할 `.beads/` 기준 상대 입력 파일명. 이식성을 위해 상대 경로 사용 |
| `routing.mode` | — | — | (없음) | 다중 저장소 라우팅: `auto`, `maintainer`, `contributor`, `explicit` |
| `routing.default` | — | — | `.` | 기본 라우팅 대상 |
| `routing.maintainer` | — | — | `.` | 유지 관리자 라우팅 경로 |
| `routing.contributor` | — | — | `~/.beads-planning` | 기여자 라우팅 경로 |
| `list.limit` | `--limit` / `-n` | `BD_LIST_LIMIT` | `50` | `bd list` 결과 기본 제한 |
| `directory.labels` | — | — | `{}` | 모노레포의 디렉터리 패턴 → 레이블 매핑 |
| `external_projects` | — | — | `{}` | 저장소 간 의존성을 위한 프로젝트 이름 → 경로 매핑 |
| `federation.remote` | — | `BD_FEDERATION_REMOTE` | (없음) | Dolt 원격 URL(`dolthub://`, `gs://`, `s3://`, `az://`, `file://`) |
| `federation.sovereignty` | — | `BD_FEDERATION_SOVEREIGNTY` | (없음) | 주권 계층: `T1`, `T2`, `T3`, `T4`([아래](#sync-and-federation) 참조) |
| `federation.allowed-remote-patterns` | — | — | `[]` | 허용되는 원격 URL을 제한하는 glob 패턴 |
| `federation.exclude_types` | — | — | `[wisp]` | 페더레이션 푸시에서 제외할 이슈 유형 |
| `sync.require_confirmation_on_mass_delete` | — | — | `false` | 병합으로 이슈 대부분이 삭제될 때 푸시 전 확인 요청 |
| `output.title-length` | — | — | `255` | 피드백의 제목 표시 길이(`0`은 숨김). 아래 라우팅 참고 참조 |
| `ai.model` | — | `BD_AI_MODEL` | `claude-haiku-4-5-20251001` | 기본 AI 모델 |
| `agents.file` | — | — | `AGENTS.md` | 에이전트 지침 파일명. 아래 라우팅 참고 참조 |

<Warning>
**JSONL 내보내기는 선택 사항입니다**

`export.auto`와 `export.git-add`는 명시적으로 구성하지 않으면 비활성화됩니다.
`.beads/issues.jsonl`은 뷰어, 교환, 이슈 수준 마이그레이션을 위한 선택적 내보내기입니다.
정식 원본도, 머신 간 동기화도, 전체 데이터베이스 백업도 아닙니다.

최신 상태로 git에 스테이징된 JSONL 파일에 의존하는 워크플로는 다음과 같이 선택해야 합니다.

```bash
bd config set export.auto true
bd config set export.git-add true
```

동기화에는 `bd dolt push` / `bd dolt pull`을, 복원 가능한 데이터베이스 백업에는
`bd backup`을 사용하세요.
</Warning>

라우팅 참고: `output.title-length`와 `agents.file`은 기능적으로 도구 수준 설정이지만 `bd config set`은 Dolt 데이터베이스에 기록합니다. `config.yaml`에 직접 설정하면 일반적으로 그곳에서 읽습니다.

`bd config show`가 출처를 포함해 현재 머신에서 유효한 항목의 원본입니다.

## Dolt 기록, 백업, 푸시

성공한 각 쓰기 명령 후 자동 커밋, 자동 백업, 자동 푸시 순서로 세 가지 동작이 실행됩니다.

<a id="auto-commit-sql-commits-vs-dolt-commits"></a>

### 자동 커밋: SQL 커밋과 Dolt 커밋

두 종류의 "커밋"이 있습니다.

- **SQL 트랜잭션 커밋** — `bd` 명령이 테이블을 성공적으로 업데이트할 때 발생(Dolt *작업 세트*에 지속됨).
- **Dolt 버전 관리 커밋** — 해당 변경 사항을 Dolt *기록*에 기록(`bd history`에 표시되며 푸시/풀/병합 워크플로가 작업하는 대상).

기본적으로(`dolt.auto-commit: on`) `bd`는 성공한 각 쓰기 명령 후 Dolt 기록 커밋을 생성하므로 변경 사항이 작업 세트에만 남지 않습니다. 시간이 지나며 쓰기 명령당 하나씩 Dolt 커밋이 늘어나는 비용이 있지만 의도된 동작입니다. `bd compact`로 이전 기록을 스쿼시하세요.

명령 하나에 비활성화:

```bash
bd --dolt-auto-commit off create "이 항목은 기록 커밋 없음"
```

또는 `config.yaml`에서:

```yaml
dolt:
  auto-commit: off
```

<a id="auto-backup"></a>

### 자동 백업

`.beads/backup/`에 주기적으로 만드는 Dolt 네이티브 백업은 실제 데이터베이스와 독립적인 복구 경로를 제공합니다. 로컬 Dolt 커밋(`dolt.auto-commit` 사용)이 여전히 주 안전망이고 백업은 보조 계층입니다. `bd export` 또는 `.beads/issues.jsonl`과 달리 테이블, 브랜치, 커밋 기록, 작업 세트 데이터를 보존하는 전체 데이터베이스 백업입니다.

```yaml
backup:
  enabled: true    # 쓰기 명령 후 자동 백업 활성화
  interval: 15m    # 자동 백업 간 최소 시간
```

작동 방식:

- 각 쓰기 명령 후 `bd`가 Dolt HEAD 커밋 해시를 마지막 백업 상태와 비교합니다.
- 데이터가 변경되고 제한 간격이 지났으면 Dolt 네이티브 백업을 `.beads/backup/`(또는 구성된 경우 `backup.git-repo` 안의 `backup/` 디렉터리)에 동기화합니다.
- 상태는 백업 디렉터리 안의 `backup_state.json`에서 추적됩니다.

수동 명령([bd backup](/cli-reference/backup) 참조):

```bash
bd backup init <path>     # 대상 등록(파일 시스템 또는 DoltHub URL)
bd backup sync            # 구성된 대상에 푸시
bd backup restore [path]  # 백업에서 복원(덮어쓰려면 --force)
bd backup remove          # 대상 등록 해제
bd backup status          # 구성과 마지막 동기화 시간 표시
```

<a id="auto-push"></a>

### 자동 푸시

기본적으로 `bd`는 쓰기 명령 후 자동으로 푸시하지 않습니다. 여러 기록자가 경쟁할 때 git 프로토콜 Dolt 원격에 동시 푸시하면 원격 기록이 손상되거나 고립될 수 있으므로 자동 푸시는 명시적 선택 사항입니다.

```yaml
dolt:
  auto-push: true         # 명시적 선택. 단일 기록자 설정에 안전
  auto-push-interval: 5m  # 자동 푸시 간 최소 시간
  auto-push-timeout: 30s  # 원격에 연결할 수 없을 때 한 푸시 시도 제한
```

작동 방식:

- 각 쓰기 명령 후(자동 커밋과 자동 백업 후) `bd`가 푸시 시점인지 확인합니다.
- 푸시는 디바운스됩니다. 마지막 푸시가 `dolt.auto-push-interval`보다 최근이면 건너뜁니다.
- 변경 감지: 마지막 푸시 후 Dolt HEAD 커밋이 바뀌지 않았으면 건너뜁니다.
- 푸시 실패는 경고일 뿐 치명적이지 않으며 실패한 시도도 제한됩니다.
- 마지막 푸시 시간과 커밋은 머신별 파일 `.beads/push-state.json`에서 추적됩니다(머신 간 병합 충돌을 피하기 위해 데이터베이스에는 저장하지 않음).

푸시하기 전에 `bd`는 30초 시간 제한 내에서 `dolt fsck --quiet`로 로컬 청크 저장소를 확인합니다. 큰 저장소는 런타임 전용 `BEADS_FSCK_TIMEOUT` 환경 변수로 늘리세요(`2m` 같은 기간 또는 `90` 같은 초 단위 숫자 허용).

<a id="actor-identity-resolution"></a>

## 행위자 ID 확인

행위자 이름(`created_by`와 감사 추적에 사용)은 다음 순서로 확인됩니다.

1. `--actor` 플래그(명시적 재정의)
2. `BEADS_ACTOR` 환경 변수
3. `BD_ACTOR` 환경 변수(사용 중단된 별칭)
4. `git config user.name`
5. `$USER` 환경 변수
6. `"unknown"`(최종 대체값)

대부분의 개발자는 구성할 필요가 없습니다. 이슈 작성자가 커밋 작성자와 자동으로 일치합니다. 재정의하려면 셸 프로필에 `BEADS_ACTOR`를 설정하세요.

```bash
export BEADS_ACTOR="my-github-handle"
```

## 프로젝트 수준 설정(데이터베이스)

`bd config set`으로 Dolt 데이터베이스에 기록하며 환경 변수 재정의는 없습니다. 일반적인 네임스페이스는 다음과 같습니다.

| 네임스페이스 | 용도 |
|---|---|
| `jira.*` | Jira 통합(URL, project(s), status_map, type_map, custom_fields) |
| `linear.*` | Linear 통합(team_id(s), priority_map, state_map, label_type_map, relation_map) |
| `github.*` | GitHub 통합(org, repo, label_map) |
| `gitlab.*` | GitLab 통합 |
| `ado.*` | Azure DevOps 통합(org, project(s), state_map, type_map) |
| `notion.*` | Notion 통합 |
| `custom.*` | 사용자 정의 / 사용자 지정 통합 |
| `<tracker>.last_sync` | 각 트래커 동기화 후 자동 업데이트. 증분 동기화 활성화 |
| `status.custom` | 선택적 동작 범주가 있는 사용자 지정 상태([아래](#custom-statuses-and-types) 참조) |
| `types.custom` | 쉼표로 구분된 사용자 지정 이슈 유형 목록 |
| `types.infra` | 버전 관리되는 issues 테이블 대신 wisps 테이블로 라우팅되는 인프라 유형 |
| `compact_tier1_days`, `compact_tier2_days` | `bd admin compact` 계층 적격성의 일 단위 기간 임곗값(기본값 `30`, `90`) |
| `issue_id_mode` | `hash`(기본값) \| `counter`([아래](#sequential-counter-ids) 참조) |
| `min_hash_length`, `max_hash_length` | 적응형 ID 범위(기본값 `3`, `8`) |
| `max_collision_prob` | 해시 ID 충돌 허용치(기본값 `0.25`) |
| `doctor.suppress.*` | 검사 slug별 특정 `bd doctor` 경고 억제(경고만 해당, 오류는 항상 표시) |

이슈 접두사(`issue_prefix`)는 `bd config set`으로 설정할 수 **없습니다**. `bd init --prefix`, `bd bootstrap` 또는 `bd rename-prefix`를 사용하세요.

<a id="custom-statuses-and-types"></a>

### 사용자 지정 상태와 유형

사용자 지정 상태는 내장 상태(`open`, `in_progress`, `blocked`, `deferred`, `closed`)를 보완합니다. 각 항목은 `name` 또는 `name:category`입니다.

```bash
bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen,archived:done"
```

범주는 상태 동작을 제어합니다.

| 범주 | `bd ready`에 표시 | 기본 `bd list`에 표시 |
|---|---|---|
| `active` | 예 | 예 |
| `wip` | 아니요 | 예 |
| `done` | 아니요 | 아니요(종료) |
| `frozen` | 아니요 | 아니요(보류) |
| (없음) | 아니요 | 예(이전 버전과 호환) |

사용자 지정 유형은 내장 이슈 유형을 확장합니다.

```bash
bd config set types.custom "agent,molecule,event"
```

`bd statuses`와 `bd types`로 구성된 모든 항목을 나열합니다.

<a id="sequential-counter-ids"></a>

### 순차 카운터 ID

기본적으로 beads는 해시 기반 ID(예: `bd-a3f2`)를 생성합니다. 짧은 순차 ID(`bd-1`, `bd-2`, ...)를 선호하는 프로젝트는 카운터 모드를 활성화하세요.

```bash
bd config set issue_id_mode counter

bd create "첫 번째 이슈" -p 1    # → bd-1
bd create "두 번째 이슈" -p 2    # → bd-2
```

| 값 | 동작 |
|---|---|
| `hash` | (기본값) 해시 기반 ID, 적응형 길이, 충돌 안전 |
| `counter` | 접두사별 순차 정수: `bd-1`, `bd-2`, `bd-3`, ... |

카운터 모드 동작:

- 각 접두사(`bd`, `plug` 등)에 독립적인 카운터가 있어 다중 저장소 또는 라우팅 설정에서 섞이지 않습니다.
- 카운터는 데이터베이스에 원자적으로 저장되므로 단일 Dolt 세션 내 동시 생성이 안전합니다.
- 처음 사용할 때(기존 저장소를 카운터 모드로 전환하는 경우 포함) 해당 접두사의 기존 숫자 ID 중 최댓값을 기준으로 카운터를 초기화하므로 새 ID가 이전 ID와 충돌하지 않습니다.
- `bd create`의 명시적 `--id` 플래그는 ID 생성을 완전히 우회하며 카운터가 증가하지 않습니다.
- 카운터 모드는 일반 이슈에만 적용되며 Wisp에는 적용되지 않습니다.

절충점 — 해시와 카운터:

| | 해시 ID | 카운터 ID |
|---|---|---|
| 사람이 읽기 쉬움 | 낮음(`bd-a3f2`) | 높음(`bd-1`) |
| 분산/동시 안전성 | 뛰어남(브랜치 간 충돌 없음) | 주의 필요(병렬 브랜치에서 카운터가 갈라질 수 있음) |
| 예측 가능성 | 예측 불가 | 순차적 |
| 적합한 용도 | 다중 에이전트, 다중 브랜치 워크플로 | 단일 기록자 또는 프로젝트 관리 UI |

### 적응형 해시 ID

해시 ID는 데이터베이스에 맞게 크기를 조정합니다. 길이는 `min_hash_length`에서 시작해 충돌 확률을 `max_collision_prob` 아래로 유지하도록 `max_hash_length`까지 늘어납니다.

```bash
bd config set max_collision_prob "0.01"   # 더 엄격한 충돌 허용치(기본값 0.25)
bd config set min_hash_length "5"         # 최소 5자 ID 강제(기본값 3)
bd config set max_hash_length "8"         # 상한(기본값 8)
```

<a id="sync-and-federation"></a>

## 동기화와 페더레이션

Beads는 셀 수준 병합과 함께 Dolt 원격(`bd dolt push` / `bd dolt pull`)으로만 동기화합니다. 이슈 이식에는 `bd export`를, 복원 가능한 데이터베이스 백업에는 `bd backup`을 사용하세요.

페더레이션 설정은 `config.yaml`에 있습니다.

```yaml
federation:
  remote: dolthub://myorg/beads
  sovereignty: T2
```

- `federation.remote`: Dolt 원격 URL(`dolthub://org/beads`, `gs://bucket/beads`, `s3://bucket/beads`, `az://account.blob.core.windows.net/container/beads`, `file://...`)
- `federation.sovereignty`: 데이터 주권 계층:
  - `T1`: 완전한 주권 — 데이터가 통제된 인프라를 벗어나지 않음
  - `T2`: 지역 주권 — 데이터가 지역/관할권 내에 유지됨
  - `T3`: 공급자 주권 — 신뢰할 수 있는 클라우드 공급자에 데이터 저장
  - `T4`: 제한 없음 — 데이터를 어디에나 저장 가능

`bd config validate`는 원격 URL 형식, 주권 계층, `federation.allowed-remote-patterns`, `routing.mode`를 확인합니다.

## 통합 구성

트래커 설정은 트래커 네임스페이스 아래의 프로젝트 수준 구성입니다. 비밀(`jira.api_token`, `linear.api_key`, `github.token`, `gitlab.token`, `ado.pat`)은 YAML로 라우팅되며 환경 변수로 제공하는 편이 좋습니다. 모든 트래커는 동기화 후 `<tracker>.last_sync`를 자동 기록해 증분 동기화를 활성화합니다.

### Jira

```bash
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"
bd config set jira.projects "PROJ1,PROJ2"   # 여러 프로젝트(쉼표로 구분)
export JIRA_API_TOKEN="YOUR_TOKEN"          # 또는: bd config set jira.api_token ...

# bd 상태를 Jira 상태에 매핑
bd config set jira.status_map.open "To Do"
bd config set jira.status_map.in_progress "In Progress"
bd config set jira.status_map.closed "Done"

# bd 이슈 유형을 Jira 이슈 유형에 매핑
bd config set jira.type_map.bug "Bug"
bd config set jira.type_map.feature "Story"
bd config set jira.type_map.task "Task"

# 푸시된 이슈에 Jira 사용자 지정 필드 설정
bd config set jira.custom_fields.customfield_10042 '{"value":"AI Platform"}'
bd config set jira.custom_fields.Story.customfield_10042 '{"value":"AI Platform"}'
```

`jira.custom_fields.<field>`는 Jira에 푸시되는 모든 이슈에 적용됩니다. `jira.custom_fields.<JiraType>.<field>`는 매핑된 Jira 이슈 유형이 `<JiraType>`과 일치할 때만 적용됩니다. 유형별 필드는 같은 필드 키의 전역 필드를 재정의합니다. `{` 또는 `[`로 시작하는 값은 JSON으로 전송되고(선택형 필드에 유용), 그 밖의 값은 문자열로 전송됩니다. `jira.url`, `jira.project`/`jira.projects`, `jira.api_token`은 `JIRA_URL`, `JIRA_PROJECT`/`JIRA_PROJECTS`, `JIRA_API_TOKEN` 환경 변수를 대체값으로 사용합니다. [bd jira](/cli-reference/jira)를 참조하세요.

### Linear

```bash
export LINEAR_API_KEY="lin_api_YOUR_API_KEY"    # 설정(Settings) → API → 개인 API 키(Personal API keys)

bd config set linear.team_id "team-uuid-here"
bd config set linear.team_ids "uuid-1,uuid-2"   # 여러 팀(또는 LINEAR_TEAM_IDS)
```

`linear.team_ids`가 설정되면 `bd linear sync`가 나열된 모든 팀의 이슈를 가져옵니다. 여러 팀이 구성된 상태에서 푸시하려면 명시적 `--team`이 필요합니다. 단수형 `linear.team_id`도 계속 지원됩니다.

매핑 네임스페이스인 `linear.priority_map.*`(Linear 0~4 → beads 0~4), `linear.state_map.*`(Linear 상태 유형과 사용자 지정 상태 이름 → beads 상태, 예: `bd config set linear.state_map.in_review in_progress`), `linear.label_type_map.*`(Linear 레이블 → bd 이슈 유형), `linear.relation_map.*`(Linear 관계 → bd 의존성, `--relations`로 풀할 때만 가져옴)의 기본값은 [bd linear](/cli-reference/linear)에 문서화되어 있습니다.

오래된 상태 감지: 풀에 성공할 때마다 `bd`는 `.beads/last_pull`(`.beads/.gitignore` 템플릿에 포함된 로컬 전용 머신별 파일)에 타임스탬프를 기록합니다. `bd linear sync --pull-if-stale`은 데이터가 임곗값(`--threshold`, 기본값 20m)보다 오래된 경우에만 풀하며 5분 디바운스로 에이전트 루프를 방지합니다. `bd prime`과 다른 핵심 명령은 Linear에 접속하지 않습니다. 에이전트 세션에서 데이터를 최신 상태로 유지하려면 세션 시작 훅에서 `bd linear sync --pull-if-stale`을 실행하세요.

### GitHub

```bash
bd config set github.org "myorg"
bd config set github.repo "myrepo"
export GITHUB_TOKEN="YOUR_TOKEN"    # 또는: bd config set github.token ...

# bd 레이블을 GitHub 레이블에 매핑
bd config set github.label_map.bug "bug"
bd config set github.label_map.feature "enhancement"
```

[bd github](/cli-reference/github)를 참조하세요.

### Azure DevOps

연결 키(`ado.pat`, `ado.org`, `ado.project`, `ado.projects`, `ado.url`)에는 각각 대응하는 `AZURE_DEVOPS_*` 환경 변수가 있으며 구성 키가 환경 변수보다 우선합니다. `ado.projects`가 설정되면 `bd ado sync`가 나열된 모든 프로젝트의 작업 항목을 단일 쿼리로 가져옵니다. 상태 맵은 Agile 프로세스 템플릿이 기본값이며(Scrum 또는 CMMI에는 `ado.state_map.*` / `ado.type_map.*`로 재정의), 우선순위 매핑(ADO 1~4 ↔ beads 0~4, 백로그는 낮음으로 합침)은 자동이며 구성할 수 없습니다. 전체 설정, 매핑 표, 동기화 명령은 [Azure DevOps 통합](/integrations/azure-devops)과 [bd ado](/cli-reference/ado)를 참조하세요.

## 환경 변수

Viper 환경 변수 접두사는 `BD_`입니다. 구성 키는 대문자로 바꾸고 `.`와 `-`를 `_`로 대체해 환경 변수에 매핑합니다(예: `dolt.auto-commit` → `BD_DOLT_AUTO_COMMIT`, `validation.on-create` → `BD_VALIDATION_ON_CREATE`).

자주 사용하는 일부 변수:

| 변수 | 설명 |
|---|---|
| `BD_DB`, `BEADS_DB` | 데이터베이스 경로(레거시 `BEADS_DB`도 계속 적용) |
| `BD_JSON` | JSON 출력 강제 |
| `BD_DOLT_AUTO_COMMIT` | `dolt.auto-commit` 재정의(`on`/`off`) |
| `BD_DOLT_AUTO_PUSH`, `BD_DOLT_AUTO_PUSH_INTERVAL`, `BD_DOLT_AUTO_PUSH_TIMEOUT` | 자동 푸시 설정 재정의 |
| `BD_BACKUP_ENABLED`, `BD_BACKUP_INTERVAL`, `BD_BACKUP_GIT_REPO` | 백업 설정 재정의 |
| `BD_AGENT_PROFILE` | `agent.profile` 재정의 |
| `BD_AI_MODEL` | AI 모델 재정의 |
| `BD_FEDERATION_REMOTE`, `BD_FEDERATION_SOVEREIGNTY` | 페더레이션 설정 재정의 |
| `BD_VALIDATION_ON_CREATE` / `_ON_CLOSE` / `_ON_SYNC` | 검증 모드 재정의 |
| `BD_NO_PAGER`, `BD_PAGER` | 페이저 동작 |
| `BD_NON_INTERACTIVE` | 프롬프트 비활성화 |
| `BD_DEBUG` | 디버그 로깅 활성화 |
| `BEADS_DIR` | 활성 beads 워크스페이스 디렉터리 강제 |
| `BEADS_ACTOR` | 행위자 ID(사용 중단된 별칭 `BD_ACTOR`보다 우선) |
| `BEADS_IDENTITY` | `bd mail` 발신자 ID |
| `BEADS_FSCK_TIMEOUT` | 푸시 전 `dolt fsck --quiet` 무결성 검사의 런타임 전용 시간 제한(기본값 `30s`) |
| `BEADS_DOLT_SERVER_MODE`, `BEADS_DOLT_SHARED_SERVER`, `BEADS_DOLT_DATA_DIR`, `BEADS_DOLT_PORT`, ... | 임베디드/서버 Dolt 재정의 |

통합 비밀은 트래커별 규칙을 따릅니다. `LINEAR_API_KEY`, `GITHUB_TOKEN`, `GITLAB_TOKEN`, `JIRA_API_TOKEN`, `AZURE_DEVOPS_PAT`, `ANTHROPIC_API_KEY`입니다. git에서 추적되는 프로젝트에서는 값을 `config.yaml`에 저장하는 것보다 이 방식을 선호합니다.

`bd config show`는 모든 유효 키의 소스를 표시해 재정의를 명확히 합니다.

<a id="security-where-secrets-live"></a>

## 보안: 비밀 저장 위치

- 토큰과 API 키는 Dolt 데이터베이스에 절대 저장되지 않습니다. 데이터베이스 구성은 원격에 푸시되므로 비밀을 노출하고 GitHub 비밀 스캔을 트리거할 수 있습니다. `bd config set`은 대신 비밀 키를 로컬 `config.yaml`로 라우팅합니다.
- `--force-git-tracked`를 전달하지 않으면 git에서 추적되는 `config.yaml`에 비밀 쓰기를 거부합니다. 환경 변수가 더 안전한 기본값입니다.
- `bd init`은 데이터베이스 디렉터리(`embeddeddolt/`, `dolt/`), 런타임 파일, 푸시 상태, 페더레이션 자격 증명 키가 git에 들어가지 않도록 `.beads/.gitignore`를 작성합니다.

## `.beads/config.yaml` 예시

```yaml
# 스크립팅용 기본 JSON 출력
json: true

# Dolt 기록 및 동기화
dolt:
  auto-commit: on    # 성공한 각 쓰기 후 Dolt 커밋 생성
  auto-push: false   # 단일 기록자 설정의 선택 사항

# 이슈 생성 정책
create:
  require-description: true

validation:
  on-create: warn    # 필수 섹션이 누락된 이슈 생성 시 경고
  on-close: none
  on-sync: none

# beads 커밋의 Git 커밋 서명(GH#600)
git:
  author: "beads-bot <beads@example.com>"
  no-gpg-sign: true

# .beads/backup/에 주기적인 Dolt 네이티브 백업
backup:
  enabled: true
  interval: 15m

# 뷰어/교환을 위해 쓰기 후 issues.jsonl 자동 내보내기(선택)
export:
  auto: false
  path: issues.jsonl
  interval: 60s
  git-add: false

# 선택적 Dolt 페더레이션
federation:
  remote: dolthub://myorg/beads
  sovereignty: T2

# 모노레포용 디렉터리 인식 레이블 범위(GH#541)
directory:
  labels:
    packages/maverick: maverick
    packages/agency: agency

# 프로젝트 간 의존성 해결(bd-h807)
external_projects:
  beads: ../beads
  other-project: /absolute/path/to/other-project

output:
  title-length: 255
```

커밋하면 안 되는 머신별 재정의는 `.beads/config.local.yaml`에 넣으세요. 마지막에 병합됩니다.

## 명령별 재정의

```bash
bd --db /tmp/test.db list           # 명령 하나의 데이터베이스 재정의
bd --json --actor "ci-bot" create "항목 수정"  # 여러 플래그
```

## 스크립트에서 사용

구성은 스크립팅용으로 설계되었으며 모든 `bd config` 하위 명령이 `--json`을 받습니다.

```bash
# 값 하나 가져오기({"key":"jira.url","value":"..."})
JIRA_URL=$(bd config get --json jira.url | jq -r '.value')

# 모든 데이터베이스 구성을 평면 객체로 가져오기
bd config list --json | jq -r '.["jira.project"]'
```

## 활성 구성 보기

```bash
bd config show                # 출처가 있는 유효 구성
bd config show --json         # 기계 판독 가능
bd config list                # 데이터베이스에 저장된 구성
bd info --json | jq '.config' # 빠른 스냅샷
```
