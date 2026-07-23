---
title: "bd edit"
description: "구성된 $EDITOR로 이슈 필드를 편집합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc edit`에서 생성되었습니다.

구성된 $EDITOR로 이슈 필드를 편집합니다.

기본적으로 설명을 편집합니다. 다른 필드를 편집하려면 플래그를 사용하세요.

예시:
  bd edit bd-42                    # 설명 편집
  bd edit bd-42 --title            # 제목 편집
  bd edit bd-42 --design           # 설계 메모 편집
  bd edit bd-42 --notes            # 메모 편집
  bd edit bd-42 --acceptance       # 인수 기준 편집

```
bd edit [id] [flags]
```

**플래그:**

```
      --acceptance    인수 기준 편집
      --description   설명 편집(기본값)
      --design        설계 메모 편집
      --notes         메모 편집
      --title         제목 편집
```
