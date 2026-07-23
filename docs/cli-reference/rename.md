---
title: "bd rename"
description: "이슈 ID를 다른 ID로 변경합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc rename`에서 생성되었습니다.

이슈 ID를 다른 ID로 변경합니다.

다음을 업데이트합니다:
- 이슈의 기본 ID
- 다른 이슈의 모든 참조(설명, 제목, 메모 등)
- 이 이슈를 향하거나 이 이슈에서 나가는 의존성
- 레이블, 댓글, 이벤트

예시:
  bd rename bd-w382l bd-dolt     # 기억하기 쉬운 ID로 변경
  bd rename gt-abc123 gt-auth    # 설명적인 ID 사용

참고: 새 ID는 이 데이터베이스에 유효한 접두사를 사용해야 합니다.

```
bd rename <old-id> <new-id> [flags]
```
