---
title: "bd lint"
description: "이슈 유형별 권장 섹션의 누락 여부를 검사합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc lint`에서 생성되었습니다.

이슈 유형별 권장 섹션의 누락 여부를 검사합니다.

기본적으로 열린 이슈를 모두 린트합니다. 특정 이슈를 린트하려면 이슈 ID를 지정하세요.

유형별 섹션 요구 사항:
  bug:      재현 단계, 인수 기준
  task:     인수 기준
  feature:  인수 기준
  epic:     성공 기준
  chore:    (없음)

예시:
  bd lint                    # 열린 이슈 모두 린트
  bd lint bd-abc             # 특정 이슈 린트
  bd lint bd-abc bd-def      # 여러 이슈 린트
  bd lint --type bug         # 버그만 린트
  bd lint --status all       # 모든 이슈 린트(닫힌 이슈 포함)


```
bd lint [issue-id...] [flags]
```

**플래그:**

```
  -s, --status string   상태로 필터링(기본값: open, 전체에는 'all' 사용)
  -t, --type string     이슈 유형으로 필터링(bug, task, feature, epic)
```
