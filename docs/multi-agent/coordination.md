---
title: 에이전트 조정
description: 여러 에이전트에서 Beads 할당과 claim, 작업 handoff, merge slot을 통한 충돌 위험 작업 직렬화
---

여러 AI 에이전트 간 작업을 조정하는 패턴입니다.

## 작업 할당

### 작업 할당 및 claim

특정 에이전트에 작업을 할당하거나 자신이 원자적으로 claim합니다.

```bash
# 에이전트에 이슈 할당
bd assign bd-42 agent-1

# 이슈 원자적 claim(assignee를 자신으로, 상태를 in_progress로 설정)
bd update bd-42 --claim

# 필터와 일치하는 첫 번째 준비된 이슈 claim
bd ready --claim --json

# claim한 이슈 해제
bd assign bd-42 ""              # assignee 지우기
bd update bd-42 --status open   # 다시 claim할 수 있게 변경
```

### 할당된 작업 확인

```bash
# agent-1이 작업 중인 항목
bd list --assignee agent-1 --status in_progress

# agent-1에게 준비된 항목
bd ready --assignee agent-1

# JSON 출력
bd list --assignee agent-1 --json
```

## handoff 패턴

### 순차 handoff

에이전트 A가 작업을 완료하고 에이전트 B에 handoff합니다.

```bash
# 에이전트 A
bd comment bd-42 "API 완료, 검토 준비됨"
bd assign bd-42 agent-b

# 에이전트 B가 인계받음
bd list --assignee agent-b  # bd-42 확인
bd update bd-42 --claim
```

### 병렬 작업

여러 에이전트가 서로 다른 이슈를 작업합니다.

```bash
# 조정자
bd assign bd-42 agent-a
bd assign bd-43 agent-b
bd assign bd-44 agent-c

# 각 에이전트가 이슈를 claim하고 독립적으로 작업
bd update bd-42 --claim

# coordinator가 진행 상황 감시
bd list --status in_progress --json
```

### fan-out / fan-in 패턴

작업을 나눈 뒤 merge합니다.

```bash
# fan-out 단계
bd create "파트 A" --parent bd-epic
bd create "파트 B" --parent bd-epic
bd create "파트 C" --parent bd-epic

bd assign bd-epic.1 agent-a
bd assign bd-epic.2 agent-b
bd assign bd-epic.3 agent-c

# fan-in: 모든 파트 대기(호출당 의존성 하나)
bd dep add bd-merge bd-epic.1
bd dep add bd-merge bd-epic.2
bd dep add bd-merge bd-epic.3
```

<Tip>
구조화된 epic fan-out에서 `bd swarm`은 epic으로부터 swarm Molecule을 생성하고
추적합니다(`bd swarm create`, `bd swarm status`).
</Tip>

## 에이전트 발견

Beads에는 에이전트 registry가 없으며 assignee는 일반 문자열입니다. 활성 에이전트를
보려면 진행 중인 작업을 assignee별로 그룹화하세요.

```bash
bd list --status in_progress --json
```

## 충돌 방지

### 원자적 claim

`--claim`은 원자적입니다. 여러 에이전트가 같은 준비 큐에서 가져올 때 첫 claim이
성공하며, 이미 보유한 claim을 반복해도 멱등적입니다. 에이전트가 작업을 스스로 고를
때는 할당보다 claim을 권장합니다.

```bash
bd ready --claim --json
```

### merge slot 직렬화

merge queue 충돌 해결처럼 충돌 위험이 있는 작업은 merge slot으로 직렬화하세요. merge
slot은 한 번에 에이전트 하나만 보유할 수 있는 독점 접근 기본 요소입니다. 각
프로젝트에는 이슈 prefix에서 이름을 만든 merge slot Bead가 하나 있습니다(예:
`bd-merge-slot`).

```bash
# 이 프로젝트의 merge slot 생성
bd merge-slot create

# 가용성 확인
bd merge-slot check

# 시작 전에 획득하고 완료 후 해제
bd merge-slot acquire
bd merge-slot release
```

## 통신 패턴

### 댓글 사용

```bash
# 에이전트 A가 note 남김
bd comment bd-42 "API 완료, frontend 통합 필요"

# 에이전트 B가 읽음
bd comments bd-42
```

### 레이블 사용

```bash
# 검토 대상으로 표시
bd update bd-42 --add-label "needs-review"

# 에이전트 B가 필터링
bd list --label-any needs-review
```

## 저장소 간 조정

에이전트는 여러 저장소에 걸친 작업을 조정할 수 있습니다.

```bash
# 다른 프로젝트가 제공하는 기능에 의존
bd dep add bd-42 external:backend:api-ready
```

다중 저장소 routing, 집계 보기, contributor/team 워크플로는
[routing](/multi-agent/routing)과 [다중 저장소
마이그레이션](/multi-agent/multi-repo-migration)에서 다룹니다.

## 모범 사례

1. **명확한 소유권** - 모든 이슈에 소유자 한 명이 있도록 작업을 할당하거나 claim합니다.
2. **handoff 문서화** - 댓글로 컨텍스트를 설명합니다.
3. **상태에 레이블 사용** - `needs-review`, `blocked`, `ready`
4. **충돌 방지** - 원자적으로 claim하고 merge slot으로 충돌 위험 작업을 직렬화합니다.
5. **진행 상황 감시** - 정기적으로 상태를 확인합니다.
6. **세션 종료 시 동기화** - 다른 에이전트가 업데이트를 볼 수 있도록 `bd dolt push`를 실행합니다.
