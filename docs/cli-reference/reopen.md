---
title: "bd reopen"
description: "상태를 'open'으로 설정하고 closed_at 타임스탬프를 지워 닫힌 이슈를 다시 엽니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc reopen`에서 생성되었습니다.

상태를 'open'으로 설정하고 closed_at 타임스탬프를 지워 닫힌 이슈를 다시 엽니다.
'bd update --status open'보다 명시적이며 Reopened 이벤트를 내보냅니다.

```
bd reopen [id...] [flags]
```

**플래그:**

```
  -r, --reason string   다시 여는 사유
```
