---
title: "bd state"
description: "이슈 레이블에서 상태 차원의 현재 값을 조회합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc state`에서 생성되었습니다.

이슈 레이블에서 상태 차원의 현재 값을 조회합니다.

상태 레이블은 &lt;dimension&gt;:&lt;value&gt; 규칙을 따릅니다. 예:
  patrol:active
  mode:degraded
  health:healthy

이 명령은 지정한 차원의 값을 추출합니다.

예시:
  bd state witness-abc patrol     # 출력: active
  bd state witness-abc mode       # 출력: normal
  bd state witness-abc health     # 출력: healthy

```
bd state <issue-id> <dimension> [flags]
```

## bd state list

이슈의 모든 상태 레이블(dimension:value 형식)을 나열합니다.

상태 규칙을 따르는 레이블만 표시하도록 필터링합니다.

예시:
  bd state list witness-abc
  # 출력:
  #   patrol: active
  #   mode: normal
  #   health: healthy

```
bd state list <issue-id> [flags]
```
