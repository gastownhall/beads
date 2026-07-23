---
title: "bd gate"
description: "gate는 워크플로 단계를 차단하는 비동기 대기 조건입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc gate`에서 생성되었습니다.

gate는 워크플로 단계를 차단하는 비동기 대기 조건입니다.

formula 단계에 gate 필드가 있으면 gate가 자동으로 생성됩니다.
차단된 단계가 진행하려면 수동 또는 watcher를 통해 닫혀야 합니다.

gate 유형:
  human   - 수동 bd close 필요(1단계)
  timer   - timeout 후 만료(2단계)
  gh:run  - GitHub 워크플로 대기(3단계)
  gh:pr   - PR 병합 대기(3단계)
  bead    - rig 간 bead가 닫히기를 대기(4단계)

bead gate의 await_id 형식은 &lt;rig&gt;:&lt;bead-id&gt;입니다(예: "other-project:op-abc123").

예시:
  bd gate list           # 열린 gate 모두 표시
  bd gate list --all     # 닫힌 gate를 포함해 모두 표시
  bd gate check          # 열린 gate 모두 평가
  bd gate check --type=bead  # bead gate만 평가
  bd gate resolve &lt;id&gt;   # gate를 수동으로 닫기

```
bd gate [flags]
```

## bd gate add-waiter

에이전트를 gate bead의 대기자로 등록합니다.

gate가 닫히면 대기자는 'bd gate wake'를 통해 깨우기 알림을 받습니다.
대기자는 일반적으로 워커 주소입니다(예: "my-project/workers/agent-1").

'bd done --phase-complete'가 gate 깨우기 알림을 등록하는 데 사용합니다.

```
bd gate add-waiter <gate-id> <waiter> [flags]
```

## bd gate check

gate 조건을 평가하고 해결된 gate를 자동으로 닫습니다.

기본적으로 열린 gate를 모두 검사합니다. gate 유형으로 필터링하려면 --type을 사용하세요.

gate 유형:
  gh       - 모든 GitHub gate 검사(gh:run 및 gh:pr)
  gh:run   - GitHub Actions 워크플로 실행 검사
  gh:pr    - pull request 병합 상태 검사
  timer    - timer gate 검사(timeout에 따라 자동 만료)
  bead     - rig 간 bead gate 검사
  all      - 모든 gate 유형 검사

GitHub gate는 상태 쿼리에 'gh' CLI를 사용합니다:
  - gh:run은 'gh run view &lt;id&gt; --json status,conclusion' 검사
  - gh:pr은 'gh pr view &lt;id&gt; --json state,title' 검사

gate는 다음 경우 해결됩니다:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - timer: current time &gt; created_at + timeout
  - bead: target bead status=closed

gate는 다음 경우 에스컬레이션됩니다:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED

예시:
  bd gate check              # 모든 gate 검사
  bd gate check --type=gh    # GitHub gate만 검사
  bd gate check --type=gh:run # 워크플로 실행 gate만 검사
  bd gate check --type=timer # timer gate만 검사
  bd gate check --type=bead  # rig 간 bead gate만 검사
  bd gate check --dry-run    # 변경 없이 수행될 작업 표시
  bd gate check --escalate   # 만료/실패한 gate 에스컬레이션

```
bd gate check [flags]
```

**플래그:**

```
      --dry-run       변경 없이 수행될 작업 표시
  -e, --escalate      실패/만료한 gate 에스컬레이션
  -l, --limit int     결과 수 제한(기본값 100)(기본값 100)
  -t, --type string   검사할 gate 유형(gh, gh:run, gh:pr, timer, bead, all)
```

## bd gate create

해결될 때까지 다른 이슈를 차단하는 임시 gate 이슈를 생성합니다.

gate가 'bd gate resolve'로 해결될 때까지 차단된 이슈는 'bd ready'에 표시되지 않습니다.

gate 유형:
  human   - 수동 'bd gate resolve' 필요(기본값)
  timer   - --timeout 기간 후 자동 해결
  gh:run  - GitHub Actions 워크플로 대기
  gh:pr   - PR 병합 대기

예시:
  bd gate create --blocks bd-abc
  bd gate create --type=human --blocks bd-abc --reason="설계 검토 필요"
  bd gate create --type=timer --blocks bd-abc --timeout=2h
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42

```
bd gate create [flags]
```

**플래그:**

```
      --await-id string   조건 식별자(실행 ID, PR 번호 등)
      --blocks string     차단할 이슈 ID(필수)
  -r, --reason string     gate 사유
      --timeout string    timeout 기간(예: 2h, 30m)
  -t, --type string       gate 유형(human, timer, gh:run, gh:pr)(기본값 "human")
```

## bd gate discover

CI/CD 완료를 기다리는 gate의 GitHub 워크플로 실행 ID를 검색합니다.

이 명령은 await_id가 없는 await_type="gh:run" 열린 gate를 찾고 최근 GitHub 워크플로
실행을 쿼리한 뒤 휴리스틱으로 일치시킵니다:
  - 브랜치 이름 일치
  - 커밋 SHA 일치
  - 시간 근접성(gate 생성 후 5분 이내 실행)

일치하면 gate의 await_id가 GitHub 실행 ID로 업데이트되어 이후 폴링으로 실행 상태를
검사할 수 있습니다.

예시:
  bd gate discover           # 일치하는 모든 gate의 실행 ID 자동 검색
  bd gate discover --dry-run # 일치할 항목 미리 보기(업데이트 없음)
  bd gate discover --branch main --limit 10  # 'main' 브랜치 실행만 일치

```
bd gate discover [flags]
```

**플래그:**

```
  -b, --branch string      브랜치로 실행 필터링(기본값: 현재 브랜치)
  -n, --dry-run            미리 보기 모드: 업데이트하지 않고 일치 항목 표시
  -l, --limit int          GitHub에서 쿼리할 최대 실행 수(기본값 10)
  -a, --max-age duration   gate/실행 일치의 최대 나이(기본값 30m0s)
```

## bd gate list

현재 beads 데이터베이스의 모든 gate 이슈를 나열합니다.

기본적으로 열린 gate만 표시합니다. 닫힌 gate를 포함하려면 --all을 사용하세요.

```
bd gate list [flags]
```

**플래그:**

```
  -a, --all         닫힌 gate를 포함해 모두 표시
  -n, --limit int   결과 수 제한(기본값 50)(기본값 50)
```

## bd gate resolve

gate 이슈를 닫아 이를 기다리는 단계의 차단을 해제합니다.

'bd close &lt;gate-id&gt;'와 같지만 이름이 더 명시적입니다.
gate 해결 이유의 컨텍스트를 제공하려면 --reason을 사용하세요.

```
bd gate resolve <gate-id> [flags]
```

**플래그:**

```
  -r, --reason string   gate 해결 사유
```

## bd gate show

대기자를 포함한 gate 이슈의 세부 정보를 표시합니다.

'bd show'와 유사하지만 이슈가 gate인지 검증합니다.

```
bd gate show <gate-id> [flags]
```
