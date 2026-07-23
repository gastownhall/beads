---
title: "bd ship"
description: "프로젝트 간 의존성을 충족하도록 기능을 배포합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc ship`에서 생성되었습니다.

프로젝트 간 의존성을 충족하도록 기능을 배포합니다.

이 명령은 다음을 수행합니다:
  1. export:&lt;capability&gt; 레이블이 있는 이슈 찾기
  2. 이슈가 닫혔는지 검증(또는 --force로 재정의)
  3. provides:&lt;capability&gt; 레이블 추가

외부 프로젝트는 다음을 사용해 이 기능에 의존할 수 있습니다:
  bd dep add &lt;issue&gt; external:&lt;project&gt;:&lt;capability&gt;

외부 프로젝트에 provides:&lt;capability&gt; 레이블이 있는 닫힌 이슈가 있으면
기능이 해결됩니다.

예시:
  bd ship mol-run-assignee              # mol-run-assignee 기능 배포
  bd ship mol-run-assignee --force      # 이슈가 닫히지 않아도 배포
  bd ship mol-run-assignee --dry-run    # 변경 없이 미리 보기

```
bd ship <capability> [flags]
```

**플래그:**

```
      --dry-run   변경 없이 미리 보기
      --force     이슈가 닫히지 않아도 배포
```
