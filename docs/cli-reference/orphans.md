---
title: "bd orphans"
description: "커밋 메시지에서 참조되지만 데이터베이스에 open 또는 in_progress로 남은 고립 이슈를 식별합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc orphans`에서 생성되었습니다.

커밋 메시지에서 참조되지만 데이터베이스에 open 또는 in_progress로 남은 고립 이슈를 식별합니다.

구현되었지만 공식적으로 닫히지 않은 작업을 식별하는 데 도움이 됩니다.

예시:
  bd orphans              # 고립 이슈 표시
  bd orphans --json       # 기계 판독 가능 출력
  bd orphans --details    # 전체 커밋 정보 표시
  bd orphans --fix        # 확인 후 고립 이슈 닫기
  bd orphans --label theme:personal             # 이 레이블이 있는 고립 이슈만 표시
  bd orphans --label-any theme:personal,theme:ventures  # 두 레이블 중 하나가 있는 고립 이슈

```
bd orphans [flags]
```

**플래그:**

```
      --details             전체 커밋 정보 표시
  -f, --fix                 확인 후 고립 이슈 닫기
  -l, --label strings       레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings   레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
```
