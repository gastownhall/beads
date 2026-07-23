---
title: "bd gc"
description: "독립 실행형 Beads 데이터베이스의 전체 수명 주기 가비지 컬렉션입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc gc`에서 생성되었습니다.

독립 실행형 Beads 데이터베이스의 전체 수명 주기 가비지 컬렉션입니다.

세 단계를 순서대로 실행합니다:
  1. DECAY   — N일보다 오래된 닫힌 이슈 삭제(기본값 90)
  2. COMPACT — 오래된 Dolt 커밋을 더 적은 커밋으로 squash(bd compact)
  3. GC      — 디스크 공간 회수를 위해 Dolt 가비지 컬렉션 실행

각 단계를 개별적으로 건너뛸 수 있습니다. 변경하지 않고 모든 단계를 미리 보려면
--dry-run을 사용하세요.

예시:
  bd gc                              # 기본값으로 전체 GC(90일 decay)
  bd gc --dry-run                    # 수행될 작업 미리 보기
  bd gc --older-than 30              # 닫힌 지 30일 이상 된 이슈 decay
  bd gc --skip-decay                 # 이슈 삭제를 건너뛰고 compact+GC만 수행
  bd gc --skip-dolt                  # Dolt GC를 건너뛰고 decay+compact만 수행
  bd gc --force                      # 확인 프롬프트 건너뛰기

```
bd gc [flags]
```

**플래그:**

```
      --dry-run          변경 없이 미리 보기
  -f, --force            확인 프롬프트 건너뛰기
      --older-than int   N일보다 오래된 닫힌 이슈 삭제(기본값 90)
      --skip-decay       이슈 삭제 단계 건너뛰기
      --skip-dolt        Dolt 가비지 컬렉션 단계 건너뛰기
```
