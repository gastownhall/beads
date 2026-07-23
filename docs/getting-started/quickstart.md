---
title: 빠른 시작
description: 몇 분 안에 Beads를 초기화하고, 의존성이 있는 이슈를 만들고, 준비된 작업을 찾고, 팀과 동기화합니다.
---

몇 분 안에 Beads를 시작해 보세요.

## Beads를 사용하는 이유

평면적인 이슈 추적기(GitHub Issues, Jira 등)는 열린 항목 목록을 보여 주고 사용자가
하나를 선택하게 합니다. 그러나 그 항목이 아직 끝나지 않은 다른 작업에 의존한다면
시간을 낭비하게 됩니다. AI 에이전트와 사람으로 구성된 팀 전체에서 이런 일이
반복되면 작업이 계속 헛돕니다.

Beads는 **이슈 간 의존성**을 추적하고 **준비 큐**를 계산합니다. 활성 차단 요소가
없는 항목만 표시됩니다. 차이는 다음과 같습니다.

**평면 추적기(GitHub Issues):**
```
열린 이슈: 데이터베이스 설정, API 생성, 인증 추가
→ 에이전트가 "인증 추가"를 선택하고 즉시 막힘
```

**Beads:**
```bash
$ bd ready
1. [P1] [task] bd-1: Set up database

$ bd ready --explain --json | jq '.blocked[0]'
{
  "id": "bd-3",
  "title": "인증 추가",
  "blocked_by": [{"id": "bd-2", "title": "API 생성", "status": "open"}]
}
```

에이전트는 매번 올바른 작업을 선택하므로 주기를 낭비하지 않습니다.

## 설치

[전체 설치 가이드](/getting-started/installation)에 따라 Homebrew, 설치 script, npm
또는 `go install`로 `bd`를 설치하세요.

**이 저장소의 클론에서 개발하는 경우:** 바이너리에 올바른 build metadata를 넣고
일관된 설치 경로를 사용하려면 `make install`을 실행하세요. 동작을 정확히 아는 경우가
아니라면 Makefile 없이 임의로 `go build` 또는 `go install`을 실행하지 마세요.
저장소의 `README`와 `AGENTS.md`를 참고하세요.

```bash
bd --help
```

## 초기화

저장소에서 처음 실행하는 경우입니다.

```bash
# 기본 설정(contributor 모드 질문 표시)
bd init

# AI 에이전트용(비대화형)
bd init --quiet

# OSS contributor(별도 계획 저장소를 사용하는 fork 워크플로)
bd init --contributor

# 팀원(협업용 브랜치 워크플로)
bd init --team

# 보호된 main 브랜치(GitHub/GitLab)
# 참고: Dolt는 Git ref와 별도로 refs/dolt/data 아래에 데이터를 저장하므로
# --branch flag가 필요하지 않음
```

wizard는 다음 작업을 수행합니다.

- `.beads/` 디렉터리와 embedded Dolt 데이터베이스 생성
- flag가 제공되지 않으면 **역할**(maintainer 또는 contributor) 질문
- 기존 Git 이슈가 있으면 import
- Git hook 설치(`--skip-hooks`로 생략)

참고:

- Dolt는 기본이자 유일한 저장소 backend입니다. 데이터는 `.beads/embeddeddolt/`에 저장됩니다.
- 기본적으로 Dolt는 **embedded 모드**로 실행됩니다. 프로세스 내부에서 실행되므로 서버가 필요 없습니다.
- 여러 writer가 있는 설정에서는 `bd init --server`로 `dolt sql-server`에 연결하세요.
- 이전 설치의 이슈를 가져오려면 `bd init --from-jsonl`을 실행하세요.

### 역할 설정

`bd init` 실행 중 `Contributing to someone else's repo? [y/N]`라는 질문이 표시됩니다.

- fork에 기여하는 경우 **Y**로 답하세요. contributor wizard가 실행됩니다.
- maintainer이거나 push 권한이 있으면 **N**으로 답하세요.

이 답변은 Beads의 이슈 라우팅 방식을 결정하는 `git config beads.role`을 설정합니다.

| 역할 | 사용 사례 | 이슈 저장 위치 |
|------|----------|---------------|
| `maintainer` | 저장소 소유자, push 권한이 있는 팀 | 저장소 내부 `.beads/` |
| `contributor` | fork contributor, OSS contributor | 별도 계획 저장소 |

수동으로 설정할 수도 있습니다.

```bash
# contributor로 설정
git config beads.role contributor

# maintainer로 설정
git config beads.role maintainer

# 현재 역할 확인
git config --get beads.role
```

**참고:** `beads.role`을 설정하지 않으면 Beads는 더 이상 권장되지 않는 URL 기반
감지로 대체합니다. `bd doctor`를 실행하여 설정 상태를 확인하세요.

## 첫 이슈

```bash
# 이슈 몇 개 생성
bd create "데이터베이스 설정" -p 1 -t task
bd create "API 생성" -p 2 -t feature
bd create "인증 추가" -p 2 -t feature

# 목록 확인
bd list
```

**참고:** 여러 에이전트나 브랜치가 동시에 작업할 때 충돌하지 않도록 이슈 ID는
해시 기반입니다(예: `bd-a1b2`, `bd-f14c`).

## 계층형 이슈(epic)

큰 기능의 작업은 계층형 ID로 구성하세요.

```bash
# epic 생성(상위 해시 ID 생성)
bd create "인증 시스템" -t epic -p 1
# 반환: bd-a3f8e9

# 하위 작업 생성(--parent로 epic에 연결)
bd create "로그인 UI 설계" -p 1 --parent bd-a3f8e9       # bd-a3f8e9.1
bd create "Backend 검증" -p 1 --parent bd-a3f8e9         # bd-a3f8e9.2
bd create "통합 테스트" -p 1 --parent bd-a3f8e9          # bd-a3f8e9.3

# 계층 구조 보기
bd dep tree bd-a3f8e9
```

출력:
```
Dependency tree for bd-a3f8e9:

> bd-a3f8e9: Auth System [epic] [P1] (open)
  > bd-a3f8e9.1: Design login UI [P1] (open)
  > bd-a3f8e9.2: Backend validation [P1] (open)
  > bd-a3f8e9.3: Integration tests [P1] (open)
```

<a id="add-dependencies"></a>

## 의존성 추가

```bash
# API가 데이터베이스에 의존
bd dep add bd-2 bd-1

# 인증이 API에 의존
bd dep add bd-3 bd-2

# 트리 보기
bd dep tree bd-3
```

출력:
```
Dependency tree for bd-3:

> bd-3: Add authentication [P2] (open)
  > bd-2: Create API [P2] (open)
    > bd-1: Set up database [P1] (open)
```

**의존성 표시:** `bd list`는 차단 의존성을 인라인으로 보여 줍니다.
```
○ bd-a1b2 [P1] [task] - Set up database
○ bd-f14c [P2] [feature] - Create API (blocked by: bd-a1b2)
○ bd-g25d [P2] [feature] - Add authentication (blocked by: bd-f14c)
```

## 준비된 작업 찾기

```bash
bd ready
```

출력:
```
Ready work (1 issues with no active blockers):

1. [P1] bd-1: Set up database
```

bd-2와 bd-3는 차단되었으므로 bd-1만 준비되어 있습니다.

**이유 확인:** 전체 그래프의 판단 근거를 보려면 `--explain`을 사용하세요.

```bash
bd ready --explain
```

출력:
```
Ready Work Explanation

● Ready (1 issues):

  bd-1 [P1] Set up database
    Reason: no blocking dependencies
    Unblocks: 1 issue(s)

● Blocked (2 issues):

  bd-2 [P2] Create API
    ← blocked by bd-1: Set up database [open]

  bd-3 [P2] Add authentication
    ← blocked by bd-2: Create API [open]

─ Summary: 1 ready, 2 blocked
```

**참고:** `bd ready`와 `bd list --status open`은 다릅니다. `list` 명령은 차단 요소와
관계없이 열린 이슈를 모두 보여 줍니다. `ready` 명령은 의존성 그래프를 계산하여
실제로 차단되지 않은 작업만 보여 줍니다.

## 큐의 작업 처리

```bash
# bd-1 작업 시작
bd update bd-1 --claim

# 완료 처리
bd close bd-1 --reason "데이터베이스 설정 완료"

# 준비된 작업 다시 확인
bd ready
```

이제 bd-2가 준비됩니다.

## 진행 상황 추적

```bash
# 차단된 이슈 확인
bd blocked

# 통계 보기
bd stats
```

## 팀 동기화

Dolt 원격을 사용하여 팀과 이슈를 공유하세요. Dolt는 표준 Git ref와 별도로 같은 Git
원격의 `refs/dolt/data` 아래에 데이터를 저장합니다. `origin`이 있는 저장소에서는
`bd init`이 해당 Dolt 원격을 자동으로 설정합니다.

```bash
# 원격 확인. 초기화할 때 저장소에 origin이 없었다면 추가
bd dolt remote list
bd dolt remote add origin git+ssh://git@github.com/org/repo.git  # 필요한 경우

# 이슈 push
bd dolt push

# 팀원의 변경 사항 pull
bd dolt pull
```

팀원이 저장소를 클론하면 `bd bootstrap`이 `refs/dolt/data`의 기존 데이터베이스를
자동 감지하고 클론한 뒤 이후 `bd dolt push` 및 `bd dolt pull`에 사용할 `origin`을
연결합니다.

CLI 세부 정보는 [`bd dolt`](/cli-reference/dolt), 원격 설정은
[Dolt 아키텍처](/architecture/dolt), federation은
[federation](/multi-agent/federation)을 참고하세요.

## 선택 사항: Notion 동기화

프로젝트 이슈를 Notion에 보관한다면 먼저 통합 token을 저장하세요.

```bash
bd config set notion.token <your-token>
```

그런 다음 상위 페이지 아래에 새 Beads 데이터베이스를 만들거나 기존 대상에 연결하세요.

```bash
bd notion init --parent <page-id>
# 또는
bd notion connect --url <notion-database-or-data-source-url>
```

같은 인증 값을 `NOTION_TOKEN`으로 제공할 수도 있습니다. 고급 설정을 위한 우회
방법으로 `notion.data_source_id`를 직접 설정하는 방식도 계속 지원됩니다.

활성 인증 소스와 대상 schema의 준비 여부를 확인하세요.

```bash
bd notion status
bd notion status --json
```

동기화를 미리 보거나 실행하세요.

```bash
bd notion sync --dry-run
bd notion sync
bd notion sync --pull
bd notion sync --push
```

## 데이터베이스 위치

기본 embedded 모드에서는 데이터가 저장소 내부의 `.beads/embeddeddolt/`에
저장됩니다. server 모드에서는 외부 `dolt sql-server`가 데이터를 관리합니다.

## 데이터베이스 마이그레이션

`bd`를 업그레이드한 뒤 `bd migrate`로 이전 데이터베이스 파일을 확인하고
마이그레이션하세요.

```bash
# 마이그레이션 계획 검사(AI 에이전트)
bd migrate --inspect --json

# schema 및 설정 확인
bd info --schema --json

# 마이그레이션 변경 사항 미리 보기
bd migrate --dry-run

# 이전 데이터베이스를 beads.db로 마이그레이션
bd migrate

# 마이그레이션 후 이전 파일 정리
bd migrate --yes
```

**AI 에이전트:** 실행 전에 `--inspect`로 마이그레이션 안전성을 분석하세요. 시스템은
필수 설정 key와 데이터 무결성 invariant를 검증합니다.

## 데이터베이스 유지보수

프로젝트에 종료된 이슈가 쌓이면 데이터베이스가 커집니다. 다음 명령으로 크기를 관리하세요.

```bash
# 압축 통계 보기
bd admin compact --stats

# 압축 후보 미리 보기(종료 후 30일 이상)
bd admin compact --analyze --json

# 에이전트가 생성한 요약 적용
bd admin compact --apply --id bd-42 --summary summary.txt

# 종료된 이슈 즉시 삭제(주의: 영구 삭제)
bd admin cleanup --force
```

**압축 시점:**

- 오래전에 종료된 이슈가 많고 데이터베이스 파일이 10MB를 초과할 때
- 주요 프로젝트 milestone이 끝나 이전 이슈가 더 이상 관련 없을 때
- 프로젝트 단계를 보관하기 전

**참고:** 압축은 영구적인 점진적 축약입니다. 원본 내용은 버려지지만 `bd restore <id>`로
압축 전 snapshot에서 복구할 수 있으며, Dolt 기록을 대체 수단으로 사용합니다.

## 다음 단계

- 레이블 추가: `bd create "작업" -l "backend,urgent"`
- 준비된 작업 필터링: `bd ready --priority 1`
- 그래프 설명 보기: `bd ready --explain`
- 그래프 무결성 확인: `bd graph check`
- 이슈 검색: `bd list --status open`
- cycle 감지: `bd dep cycles`
- PR/CI 동기화용 Gate: [`bd gate`](/cli-reference/gate)
- 더 많은 동기화 사례: [`bd dolt`](/cli-reference/dolt)
- 전체 명령 목록: [CLI 참조](/cli-reference/index)

개요와 상세 문서 링크는 [저장소 README](https://github.com/gastownhall/beads/blob/main/README.md)를 참고하세요.
