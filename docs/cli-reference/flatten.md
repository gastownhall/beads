---
title: "bd flatten"
description: "최후의 수단: 모든 Dolt 커밋 이력을 단일 커밋으로 squash합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc flatten`에서 생성되었습니다.

최후의 수단: 모든 Dolt 커밋 이력을 단일 커밋으로 squash합니다.

Tim Sehn 레시피를 사용합니다:
  1. 현재 상태에서 새 브랜치 생성
  2. 초기 커밋으로 soft-reset(모든 데이터 보존)
  3. 모든 항목을 단일 스냅샷으로 커밋
  4. main 브랜치를 새로 평탄화한 브랜치로 교체
  5. 오래된 이력의 공간 회수를 위해 Dolt GC 실행

되돌릴 수 없으며 모든 커밋 이력이 손실됩니다. 결과 데이터베이스에는 현재 데이터를
모두 포함한 커밋 하나만 존재합니다.

다음 경우 사용하세요:
  - .beads/dolt 디렉터리가 매우 커진 경우
  - 커밋 수준 이력(time travel)이 필요 없는 경우
  - 최소 저장소로 새로 시작하려는 경우

예시:
  bd flatten --dry-run               # 미리 보기: 커밋 수와 디스크 사용량 표시
  bd flatten --force                 # 모든 이력을 실제로 squash
  bd flatten --force --json          # JSON 출력

```
bd flatten [flags]
```

**플래그:**

```
      --dry-run   변경 없이 미리 보기
  -f, --force     되돌릴 수 없는 이력 squash 확인
```
