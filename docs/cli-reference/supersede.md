---
title: "bd supersede"
description: "이슈를 최신 버전으로 대체되었다고 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc supersede`에서 생성되었습니다.

이슈를 최신 버전으로 대체되었다고 표시합니다.

대체된 이슈는 대체 항목에 대한 참조와 함께 자동으로 닫힙니다.
설계 문서, 사양, 계속 변경되는 아티팩트에 유용합니다.

예시:
  bd supersede bd-old --with bd-new    # bd-old가 bd-new로 대체되었다고 표시

```
bd supersede <id> --with <new> [flags]
```

**플래그:**

```
      --with string   대체 이슈 ID(필수)
```
