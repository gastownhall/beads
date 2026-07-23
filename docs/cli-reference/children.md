---
title: "bd children"
description: "지정한 상위 bead의 모든 하위 bead를 나열합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc children`에서 생성되었습니다.

지정한 상위 bead의 모든 하위 bead를 나열합니다.

'bd list --parent &lt;id&gt; --status all'의 편의 별칭입니다. 일반 'bd list'와 달리
children은 기본적으로 닫힌 이슈를 포함합니다. 주 사용 사례가 상위 항목 아래의
모든 작업을 검사하는 것이기 때문입니다.

예시:
  bd children hq-abc123        # hq-abc123의 모든 하위 항목 나열
  bd children hq-abc123 --json # 하위 항목을 JSON 형식으로 나열
  bd children hq-abc123 --pretty # 하위 항목을 트리 형식으로 표시

```
bd children <parent-id> [flags]
```

**플래그:**

```
      --pretty   하위 항목을 트리 형식으로 표시
```
