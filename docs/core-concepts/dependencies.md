---
title: 의존성과 Gate
description: 차단 및 비차단 의존성으로 작업 순서를 지정하고 PR, CI 또는 타이머를 기다리는 Gate 사용하기
---

Beads에는 작업 순서를 지정하는 완전한 의존성 시스템과 외부 조건(PR 병합, CI 실행,
타이머)을 의존성 그래프에 연결하는 Gate 시스템이 포함되어 있습니다.

## 의존성 추가

```bash
# issue-2는 issue-1에 의존(issue-1이 issue-2를 차단)
bd dep add issue-2 issue-1

# 단축형: issue-1이 issue-2를 차단
bd dep issue-1 --blocks issue-2

# 대체 플래그(동일함)
bd dep add issue-2 --blocked-by issue-1
bd dep add issue-2 --depends-on issue-1
```

issue-1이 열려 있으면 issue-2는 `bd ready`에 나타나지 않습니다. issue-1이 닫히면
issue-2의 차단이 자동으로 해제됩니다.

## 의존성 제거

```bash
bd dep remove issue-2 issue-1
bd dep rm issue-2 issue-1        # 별칭
```

## 의존성 유형

의존성에는 작업을 차단할지 결정하는 유형이 있습니다.

**차단 유형**(`bd ready`에 영향):

| 유형 | 의미 | 예시 |
|------|---------|---------|
| `blocks`(기본값) | A가 닫힐 때까지 B를 시작할 수 없음 | 작업 순서 지정 |
| `parent-child` | 부모가 차단되면 자식도 차단됨 | Epic 계층 |
| `conditional-blocks` | A가 실패할 때만 B 실행 | 오류 처리 경로 |
| `waits-for` | B가 A의 모든 자식을 기다림 | 팬아웃 집계 |

**비차단 유형**(그래프 주석 전용):

| 유형 | 의미 |
|------|---------|
| `related` | 정보 링크 |
| `tracks` | 다른 이슈의 진행 상황 추적 |
| `discovered-from` | 다른 이슈 작업 중 발견 |
| `caused-by` | 근본 원인 링크 |
| `validates` | 테스트 또는 검증 링크 |
| `supersedes` | 다른 이슈 대체 |

`--type`으로 지정합니다.

```bash
bd dep add issue-2 issue-1 --type tracks
bd dep add issue-2 issue-1 --type caused-by
```

## 준비된 작업 찾기

`bd ready`는 열려 있는 차단 의존성이 없는 이슈를 표시합니다.

```bash
bd ready
```

출력:
```
📋 Ready work (1 issues with no blockers):

1. [P1] bd-a1b2: 데이터베이스 설정
```

모든 차단 의존성이 닫히면 이슈가 준비 상태가 됩니다.

```bash
# 준비된 작업 필터링
bd ready --priority 1              # 우선순위별
bd ready --label backend           # 레이블별
bd ready --assignee alice          # 담당자별
bd ready --unassigned              # 미할당 항목만
bd ready --type task               # 이슈 유형별
bd ready --sort oldest             # 오래된 항목부터
```

## 차단된 이슈 보기

```bash
bd blocked
```

차단된 모든 이슈와 차단 원인을 표시합니다. 이슈를 닫은 뒤 방금 차단이 해제된 항목을
확인할 때 사용하세요.

## 의존성 시각화

### 의존성 트리

```bash
bd dep tree issue-id                    # 이 이슈가 의존하는 항목은?
bd dep tree issue-id --direction=up     # 이 이슈에 의존하는 항목은?
bd dep tree issue-id --direction=both   # 양방향
bd dep tree issue-id --status=open      # 열린 이슈만
bd dep tree issue-id --max-depth=3      # 깊이 제한
bd dep tree issue-id --format=mermaid   # Mermaid.js 출력
```

### 의존성 그래프

```bash
bd graph issue-id                       # 단일 이슈 DAG
bd graph --all                          # 모든 열린 이슈

# 출력 형식
bd graph --compact issue-id             # 이슈당 한 줄
bd graph --box issue-id                 # 계층을 표현한 ASCII 상자
bd graph --dot issue-id | dot -Tsvg > graph.svg   # Graphviz
bd graph --html issue-id > graph.html   # 대화형 D3.js
```

그래프는 이슈를 계층으로 구성합니다.
- **계층 0**: 의존성 없음(즉시 시작 가능)
- **계층 1**: 계층 0에 의존
- **상위 계층**: 하위 계층에 의존
- **같은 계층**: 병렬 실행 가능

### 의존성 목록

```bash
bd dep list issue-id                    # 이 항목이 의존하는 항목은?
bd dep list issue-id --direction=up     # 이 항목에 의존하는 항목은?
bd dep list issue-id --type=tracks      # 유형별 필터링
```

### 순환 감지

```bash
bd dep cycles
```

Beads는 쓰기 시점에도 순환을 거부합니다. `bd dep add`는 커밋하기 전에 순환을 확인합니다.

## 저장소 간 의존성

의존성은 다른 beads rig의 이슈를 참조할 수 있습니다.

```bash
bd dep add local-issue external:other-project:remote-issue
```

외부 의존성은 항상 차단합니다. 원격 이슈가 닫히면 `bd ready`에 변경 사항이 반영됩니다
(쿼리 시점에 확인).

## Gate

Gate는 외부 조건이 충족될 때까지 의존 작업을 차단하는 특수 이슈입니다. 작업을 추적하는
beads와 코드, CI 또는 시간을 추적하는 외부 시스템 사이의 간극을 연결합니다.

### Gate가 해결하는 문제

Dolt(서버 또는 임베디드)를 사용하면 이슈 상태와 코드 상태가 분리됩니다. beads 이슈를
닫는 것은 "작업 완료"를 뜻하지만 코드는 여전히 기능 브랜치에서 PR 검토를 기다릴 수 있습니다.

```
issue-1: beads에서 닫힘       (작업 완료)
PR #42:  GitHub에서 열림      (코드가 아직 main에 없음)
issue-2: issue-1에 의해 차단  (시작해야 하나?)
```

파일 기반 저장소(JSONL)에서는 이슈 업데이트와 코드가 같은 커밋에 원자적으로 반영됩니다.
Dolt에서는 그렇지 않습니다. Gate는 의존성이 beads 이슈 상태뿐 아니라 외부 조건을
기다리게 해 이 문제를 해결합니다.

### Gate 유형

| 유형 | 조건 | 자동 해결 |
|------|-----------|-----------------|
| `gh:pr` | PR 병합됨 | `gh pr view`가 MERGED 반환 |
| `gh:run` | CI 통과 | `gh run view`가 completed + success 반환 |
| `timer` | 시간 경과 | 현재 시간이 시간 제한을 초과 |
| `bead` | rig 간 이슈 닫힘 | 원격 bead 상태 확인 |
| `human` | 수동 승인 | `bd gate resolve <id>` |

### Gate 생성

```bash
# PR #42 병합 대기
bd create --type=gate --title="PR #42 병합 대기" \
  --await-type=gh:pr --await-id=42

# CI 실행 대기
bd create --type=gate --title="CI 대기" \
  --await-type=gh:run --await-id=12345

# 30분 대기
bd create --type=gate --title="대기 시간" \
  --await-type=timer --await-id=30m

# rig 간 bead가 닫히기를 기다림
bd create --type=gate --title="업스트림 수정 대기" \
  --await-type=bead --await-id=other-rig:issue-id

# 수동 승인 Gate
bd create --type=gate --title="배포 승인"
```

### Gate를 의존성에 연결

Gate는 이슈입니다. 다른 이슈와 마찬가지로 의존성 그래프에 연결합니다.

```bash
# issue-2가 Gate 대기(Gate는 PR #42 대기)
bd dep add issue-2 <gate-id>
```

### Gate 확인

`bd gate check`는 열려 있는 모든 Gate를 평가하고 해결된 Gate를 닫습니다.

```bash
bd gate check                    # 모든 Gate 확인
bd gate check --type=gh:pr       # PR Gate만
bd gate check --type=gh:run      # CI Gate만
bd gate check --type=timer       # 타이머만
bd gate check --dry-run          # 변경 없이 미리 보기
bd gate check --escalate         # 실패한 Gate 에스컬레이션
```

에스컬레이션은 조건이 실패한 Gate(예: 병합 없이 PR이 닫힘, CI 실행 실패)를 표시해
주의가 필요한 항목으로 드러냅니다.

### Gate 나열 및 검사

```bash
bd gate list                     # 열린 Gate
bd gate list --all               # 닫힌 Gate 포함
bd gate show <gate-id>           # 전체 세부 정보
```

### 수동 해결

`human` Gate 또는 재정의의 경우 다음을 실행합니다.

```bash
bd gate resolve <gate-id> --reason "팀 리드가 승인함"
```

### CI 실행 ID 찾기

실행 시작 전에 `gh:run` Gate를 생성하면 `bd gate discover`가 휴리스틱(커밋 SHA,
브랜치, 타이밍)을 사용해 Gate를 GitHub Actions 실행과 일치시킵니다.

```bash
bd gate discover                 # Gate를 실행과 자동으로 일치시킴
bd gate discover --dry-run       # 일치 결과 미리 보기
bd gate discover --branch main   # 브랜치별 필터링
```

### Gate 확인 자동화

`bd gate check`를 주기적으로 실행해 해결된 Gate를 자동으로 닫습니다.

- **CI 단계**: GitHub Actions 워크플로에 추가합니다.
- **Cron**: `*/5 * * * * cd /path/to/repo && bd gate check`
- **에이전트 훅**: 세션 시작 시 또는 PR 작업 후 실행합니다.

## 사용법

### PR 병합 Gate(일반적)

에이전트 A가 작업을 마치고 PR을 연 다음, 에이전트 B가 병합을 기다리도록 Gate를 생성합니다.

```bash
# 에이전트 A
bd update issue-1 --status=in_progress
# ... 코드 작성, PR #42 열기 ...
bd create --type=gate --title="PR #42 병합 대기" \
  --await-type=gh:pr --await-id=42
bd dep add issue-2 <gate-id>
bd close issue-1

# 에이전트 B
bd ready                         # issue-2가 표시되지 않음(Gate 열림)
# ... PR #42 병합 ...
bd gate check                    # Gate 닫힘
bd ready                         # issue-2 표시
```

### 배포 전 CI Gate

```bash
bd create --type=gate --title="main의 CI 통과" \
  --await-type=gh:run --await-id=<run-id>
bd dep add deploy-task <gate-id>
```

### 단계 순서가 있는 Epic

```bash
bd create "인증 시스템" -t epic
bd create "설계" --parent <epic>
bd create "구현" --parent <epic>
bd create "테스트" --parent <epic>

bd dep add <implement> <design>
bd dep add <test> <implement>

bd dep tree <epic>
bd ready                         # "설계"만 준비됨
```

## 함께 보기

- [빠른 시작](/getting-started/quickstart) — 의존성 사용 첫 단계
- [Molecules](/workflows/molecules) — Gate와 의존성을 사용하는 Molecule 워크플로
- [에이전트 조정](/multi-agent/coordination) — 저장소 간 의존성 패턴
- [Beads용 Dolt 백엔드](/architecture/dolt) — Dolt 백엔드 구성
- [CLI 참조](/cli-reference/index) — 전체 명령 참조
