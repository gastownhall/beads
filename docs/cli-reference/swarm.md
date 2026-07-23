---
title: "bd swarm"
description: "epic의 병렬 작업을 조정하는 swarm 관리 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc swarm`에서 생성되었습니다.

epic의 병렬 작업을 조정하는 swarm 관리 명령입니다.

swarm은 epic과 하위 이슈로 정의된 구조화된 작업 집합이며,
의존성이 작업의 DAG(방향성 비순환 그래프)를 구성합니다.

```
bd swarm [flags]
```

## bd swarm create

epic의 병렬 작업을 오케스트레이션할 swarm molecule을 생성합니다.

swarm molecule은 다음과 같습니다:
- 오케스트레이션하는 epic에 연결
- 검색을 위한 mol_type=swarm 보유
- coordinator 지정(선택 사항)
- 모든 coordinator 에이전트가 선택 가능

단일 이슈(epic 아님)를 지정하면 자동으로 감쌉니다:
- 해당 이슈만 하위 이슈로 갖는 epic 생성
- 해당 epic의 swarm molecule 생성

예시:
  bd swarm create bd-epic-123                          # epic의 swarm 생성
  bd swarm create bd-epic-123 --coordinator=observer/   # 특정 coordinator 지정
  bd swarm create bd-task-456                          # 단일 이슈 자동 감싸기

```
bd swarm create [epic-id] [flags]
```

**플래그:**

```
      --coordinator string   coordinator 주소(예: my-project/witness)
      --force                이미 있어도 새 swarm 생성
```

## bd swarm list

모든 swarm molecule과 상태를 나열합니다.

각 swarm molecule에 다음을 표시합니다:
- 진행률(완료/전체 이슈)
- 활성 워커
- epic ID와 제목

예시:
  bd swarm list         # 모든 swarm 나열
  bd swarm list --json  # 기계 판독 가능 출력

```
bd swarm list [flags]
```

## bd swarm status

beads에서 계산한 swarm의 현재 상태를 표시합니다.

다음 중 하나를 받습니다:
- epic ID(해당 epic의 하위 이슈 상태 표시)
- swarm molecule ID(링크를 따라 epic 검색)

상태별로 그룹화한 이슈를 표시합니다:
- 완료: 닫힌 이슈
- 활성: 현재 in_progress인 이슈(담당자 있음)
- 준비: 모든 의존성이 충족된 열린 이슈
- 차단: 의존성을 기다리는 열린 이슈

상태는 별도로 저장되지 않고 beads에서 계산됩니다.
beads가 변경되면 상태도 변경됩니다.

예시:
  bd swarm status gt-epic-123       # epic별 swarm 상태 표시
  bd swarm status gt-swarm-456      # swarm molecule을 통해 상태 표시
  bd swarm status gt-epic-123 --json  # 기계 판독 가능 출력

```
bd swarm status [epic-or-swarm-id] [flags]
```

## bd swarm validate

epic 구조가 swarm 실행 준비 상태인지 검증합니다.

검사 항목:
- 올바른 의존성 방향(시간이 아닌 요구 사항 기반)
- 고립 이슈(종속 항목이 없는 루트)
- 누락된 의존성(무언가에 의존해야 하는 리프)
- 순환(해결 불가)
- 연결되지 않은 하위 그래프

보고 항목:
- 준비된 프런트(병렬 작업 웨이브)
- 예상 워커 세션
- 최대 병렬도
- 잠재적 문제 경고

예시:
  bd swarm validate gt-epic-123           # epic 구조 검증
  bd swarm validate gt-epic-123 --verbose # 상세 이슈 그래프 포함

```
bd swarm validate [epic-id] [flags]
```

**플래그:**

```
      --verbose   출력에 상세 이슈 그래프 포함
```
