---
title: "bd promote"
description: "wisp(임시 이슈)를 영구 bead로 승격합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc promote`에서 생성되었습니다.

wisp(임시 이슈)를 영구 bead로 승격합니다.

이슈를 wisps 테이블(dolt_ignored)에서 영구 issues 테이블(Dolt 버전 관리)로
복사하며 레이블, 의존성, 이벤트, 댓글을 보존합니다. 원래 ID가 보존되어
모든 링크가 계속 작동합니다.

승격 및 선택적 사유를 기록하는 댓글이 추가됩니다.

예시:
  bd promote bd-wisp-abc123
  bd promote bd-wisp-abc123 --reason "장기 추적할 가치가 있음"

```
bd promote <wisp-id> [flags]
```

**플래그:**

```
  -r, --reason string   승격 사유
```
