---
title: "bd merge-slot"
description: "merge-slot gate는 병합 큐의 충돌 해결을 직렬화합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc merge-slot`에서 생성되었습니다.

merge-slot gate는 병합 큐의 충돌 해결을 직렬화합니다.

병합 슬롯은 배타적 접근 기본 요소로, 한 번에 하나의 에이전트만 보유할 수 있습니다.
이는 여러 polecat이 충돌 해결을 두고 경쟁하면서 연쇄 충돌을 만드는
"monkey knife fights"를 방지합니다.

각 rig에는 하나의 병합 슬롯 bead가 있습니다: &lt;prefix&gt;-merge-slot(gt:slot 레이블).
슬롯은 다음을 사용합니다:
  - status=open: 슬롯 사용 가능
  - status=in_progress: 슬롯 보유 중
  - metadata.holder: 현재 슬롯 보유자
  - metadata.waiters: 우선순위순 대기자 큐

예시:
  bd merge-slot create              # 현재 rig의 병합 슬롯 생성
  bd merge-slot check               # 슬롯 사용 가능 여부 확인
  bd merge-slot acquire             # 슬롯 획득 시도
  bd merge-slot release             # 슬롯 해제

```
bd merge-slot [flags]
```

## bd merge-slot acquire

배타적 접근을 위해 병합 슬롯 획득을 시도합니다.

슬롯을 사용할 수 있으면(status=open) 획득합니다:
  - status를 in_progress로 설정
  - holder를 요청자로 설정

슬롯을 보유 중이면(status=in_progress) --wait를 전달하지 않는 한
명령이 실패합니다. 이 옵션은 요청자를 대기자 큐에 추가합니다.

--holder로 획득자를 지정합니다(기본값: BEADS_ACTOR 환경 변수).

```
bd merge-slot acquire [flags]
```

**플래그:**

```
      --holder string   슬롯 획득자(기본값: BEADS_ACTOR)
      --wait            슬롯을 보유 중이면 대기자 목록에 추가
```

## bd merge-slot check

병합 슬롯을 사용할 수 있는지 또는 보유 중인지 확인합니다.

반환값:
  - available: 슬롯 획득 가능
  - held by &lt;holder&gt;: 슬롯을 현재 보유 중
  - not found: 이 rig에 병합 슬롯이 없음

```
bd merge-slot check [flags]
```

## bd merge-slot create

직렬화된 충돌 해결을 위한 병합 슬롯 bead를 생성합니다.

슬롯 ID는 beads 접두사를 기반으로 자동 생성됩니다(예: gt-merge-slot).
슬롯은 status=open(사용 가능)으로 생성됩니다.

```
bd merge-slot create [flags]
```

## bd merge-slot release

충돌 해결이 완료된 후 병합 슬롯을 해제합니다.

상태를 open으로 되돌리고 holder 필드를 지웁니다.
대기자가 있으면 우선순위가 가장 높은 대기자가 획득해야 합니다.

```
bd merge-slot release [flags]
```

**플래그:**

```
      --holder string   슬롯 해제자(검증용)
```
