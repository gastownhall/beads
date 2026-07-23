---
title: "bd statuses"
description: "유효한 모든 이슈 상태와 범주를 나열합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc statuses`에서 생성되었습니다.

유효한 모든 이슈 상태와 범주를 나열합니다.

기본 제공 상태(open, in_progress, blocked 등)는 항상 유효합니다.
추가 상태는 status.custom으로 구성할 수 있습니다:

  bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen"

범주는 동작을 제어합니다:
  active  — 'bd ready'와 기본 'bd list'에 표시
  wip     — 'bd ready'에서 제외, 기본 'bd list'에 표시
  done    — 'bd ready'와 기본 'bd list'에서 제외
  frozen  — 'bd ready'와 기본 'bd list'에서 제외

범주가 없는 상태(레거시 형식)는 유효하지만 'bd ready'에서 제외됩니다.

예시:
  bd statuses            # 아이콘 및 범주와 함께 모든 상태 나열
  bd statuses --json     # JSON으로 출력


```
bd statuses [flags]
```
