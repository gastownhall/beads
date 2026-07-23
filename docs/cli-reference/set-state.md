---
title: "bd set-state"
description: "이슈의 운영 상태를 원자적으로 설정합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc set-state`에서 생성되었습니다.

이슈의 운영 상태를 원자적으로 설정합니다.

이 명령은 다음을 수행합니다:
1. 상태 변경을 기록하는 event bead 생성(신뢰할 수 있는 원본)
2. 해당 차원의 기존 레이블 제거
3. 새 dimension:value 레이블 추가(빠른 조회 캐시)

상태 레이블은 &lt;dimension&gt;:&lt;value&gt; 규칙을 따릅니다. 예:
  patrol:active, patrol:muted
  mode:normal, mode:degraded
  health:healthy, health:failing

예시:
  bd set-state agent-abc patrol=muted --reason "멈춘 워커 조사 중"
  bd set-state agent-abc mode=degraded --reason "높은 오류율 감지"
  bd set-state agent-abc health=healthy

--reason 플래그는 event bead의 컨텍스트를 제공합니다(권장).

```
bd set-state <issue-id> <dimension>=<value> [flags]
```

**플래그:**

```
      --reason string   상태 변경 사유(이벤트에 기록)
```
