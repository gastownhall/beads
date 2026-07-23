---
title: "bd history"
description: "이슈가 수정된 모든 커밋을 포함해 전체 버전 이력을 표시합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc history`에서 생성되었습니다.

이슈가 수정된 모든 커밋을 포함해 전체 버전 이력을 표시합니다.

예시:
  bd history bd-123           # 이슈 bd-123의 모든 이력 표시
  bd history bd-123 --limit 5 # 최근 변경 사항 5개 표시

```
bd history <id> [flags]
```

**플래그:**

```
      --limit int   이력 항목 수 제한(0 = 전체)
```
