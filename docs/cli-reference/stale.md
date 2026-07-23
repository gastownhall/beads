---
title: "bd stale"
description: "최근 업데이트되지 않아 확인이 필요할 수 있는 이슈를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc stale`에서 생성되었습니다.

최근 업데이트되지 않아 확인이 필요할 수 있는 이슈를 표시합니다.
다음을 식별하는 데 도움이 됩니다:
- 최근 활동이 없는 진행 중 이슈(중단되었을 수 있음)
- 잊힌 열린 이슈
- 오래되었거나 더 이상 관련 없을 수 있는 이슈

```
bd stale [flags]
```

**플래그:**

```
  -d, --days int        이 일수 동안 업데이트되지 않은 이슈(기본값 30)
  -n, --limit int       표시할 최대 이슈 수(기본값 50)
  -s, --status string   상태로 필터링(open|in_progress|blocked|deferred)
```
