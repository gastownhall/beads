---
title: "bd compact"
description: "N일보다 오래된 Dolt 커밋을 단일 커밋으로 squash합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc compact`에서 생성되었습니다.

N일보다 오래된 Dolt 커밋을 단일 커밋으로 squash합니다.

최근 커밋(보존 기간 내)은 cherry-pick으로 보존됩니다. 최근 변경 추적은 유지하면서
자동 커밋 이력으로 인한 Dolt 저장소 오버헤드를 줄입니다.

의미 기반 이슈 압축(닫힌 이슈 요약)에는 'bd admin compact'를 사용하세요.
전체 이력 squash에는 'bd flatten'을 사용하세요.

작동 방식:
  1. --days 임곗값보다 오래된 커밋 식별
  2. 모든 오래된 이력에서 squash된 기본 커밋 생성
  3. 그 위에 최근 커밋 cherry-pick
  4. main 브랜치를 압축된 버전으로 교체
  5. 공간 회수를 위해 Dolt GC 실행

예시:
  bd compact --dry-run               # 미리 보기: 커밋 내역 표시
  bd compact --force                 # 30일보다 오래된 커밋 squash
  bd compact --days 7 --force        # 최근 7일 이력만 유지
  bd compact --days 90 --force       # 보수적: 90일 이상 된 커밋 squash

```
bd compact [flags]
```

**플래그:**

```
      --days int   N일 이내의 새 커밋 유지(기본값 30)
      --dry-run    변경 없이 미리 보기
  -f, --force      커밋 squash 확인
```
