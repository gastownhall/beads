---
title: "bd undefer"
description: "이슈의 연기를 취소하여 open 상태로 복원합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc undefer`에서 생성되었습니다.

이슈의 연기를 취소하여 open 상태로 복원합니다.

다시 작업할 수 있도록 이슈를 아이스박스에서 되돌립니다.
차단 요소가 없으면 이슈가 'bd ready'에 표시됩니다.

예시:
  bd undefer bd-abc        # 단일 이슈 연기 취소
  bd undefer bd-abc bd-def # 여러 이슈 연기 취소

```
bd undefer [id...] [flags]
```
