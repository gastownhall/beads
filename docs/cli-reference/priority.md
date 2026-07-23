---
title: "bd priority"
description: "이슈의 우선순위를 설정합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc priority`에서 생성되었습니다.

이슈의 우선순위를 설정합니다.

'bd update &lt;id&gt; --priority &lt;n&gt;'의 단축 명령입니다.

우선순위 수준:
  0 - 치명적(보안, 데이터 손실, 빌드 실패)
  1 - 높음(주요 기능, 중요한 버그)
  2 - 중간(기본값)
  3 - 낮음(다듬기, 최적화)
  4 - 백로그(향후 아이디어)

예시:
  bd priority bd-123 0    # 치명적
  bd priority bd-123 2    # 중간

```
bd priority <id> <n> [flags]
```
