---
title: "bd duplicate"
description: "이슈를 기준 이슈의 중복으로 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc duplicate`에서 생성되었습니다.

이슈를 기준 이슈의 중복으로 표시합니다.

중복 이슈는 기준 이슈에 대한 참조와 함께 자동으로 닫힙니다.
유사한 보고가 많은 대규모 이슈 데이터베이스에 필수적입니다.

예시:
  bd duplicate bd-abc --of bd-xyz    # bd-abc를 bd-xyz의 중복으로 표시

```
bd duplicate <id> --of <canonical> [flags]
```

**플래그:**

```
      --of string   기준 이슈 ID(필수)
```
