---
title: "bd duplicates"
description: "콘텐츠(제목, 설명, 설계, 인수 기준)가 동일한 이슈를 찾습니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc duplicates`에서 생성되었습니다.

콘텐츠(제목, 설명, 설계, 인수 기준)가 동일한 이슈를 찾습니다.
이슈를 콘텐츠 해시로 그룹화하고 제안된 병합 대상과 함께 중복을 보고합니다.
병합 대상은 다음 기준으로 선택됩니다:
1. 참조 수(가장 많이 참조된 이슈 우선)
2. 참조 수가 같으면 사전순으로 가장 작은 ID
상태가 일치하는 이슈만 그룹화합니다(open은 open끼리, closed는 closed끼리).
예시:
  bd duplicates                    # 모든 중복 그룹 표시
  bd duplicates --auto-merge       # 모든 중복 자동 병합
  bd duplicates --dry-run          # 병합될 항목 표시

```
bd duplicates [flags]
```

**플래그:**

```
      --auto-merge   모든 중복 자동 병합
      --dry-run      변경하지 않고 병합될 항목 표시
```
