---
title: "bd link"
description: "두 이슈를 의존성으로 연결합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc link`에서 생성되었습니다.

두 이슈를 의존성으로 연결합니다.

'bd dep add &lt;id1&gt; &lt;id2&gt;'의 단축 명령입니다. 기본적으로 "blocks"
의존성(id2가 id1을 차단)을 생성합니다. 다른 관계를 지정하려면 --type을 사용하세요.

예시:
  bd link bd-123 bd-456                    # bd-456이 bd-123을 차단
  bd link bd-123 bd-456 --type related     # bd-123이 bd-456과 관련됨
  bd link bd-123 bd-456 --type parent-child

```
bd link <id1> <id2> [flags]
```

**플래그:**

```
  -t, --type string   의존성 유형(blocks|tracks|related|parent-child|discovered-from)(기본값 "blocks")
```
