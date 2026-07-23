---
title: "bd defer"
description: "나중을 위해 이슈를 연기해 보류합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc defer`에서 생성되었습니다.

나중을 위해 이슈를 연기해 보류합니다.

연기된 이슈는 특정 항목에 차단된 것이 아니라 향후 검토를 위해 의도적으로 보류됩니다.
차단된 이슈와 달리 작업을 막는 의존성이 없습니다. 닫힌 이슈와 달리 다시 검토됩니다.

연기된 이슈는 'bd ready'에 표시되지 않지만 'bd list'에는 계속 표시됩니다.

예시:
  bd defer bd-abc                  # 단일 이슈 연기(상태 기반)
  bd defer bd-abc --until=tomorrow # 특정 시간까지 연기
  bd defer bd-abc --reason="API 접근 대기 중"
  bd defer bd-abc bd-def           # 여러 이슈 연기

```
bd defer [id...] [flags]
```

**플래그:**

```
      --reason string   이 이슈를 연기하는 이유 기록(notes에 추가)
      --until string    특정 시간까지 연기(예: +1h, tomorrow, next monday)
```
