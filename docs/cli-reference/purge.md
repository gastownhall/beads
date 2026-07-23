---
title: "bd purge"
description: "닫힌 임시 bead와 관련 데이터를 영구 삭제합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc purge`에서 생성되었습니다.

닫힌 임시 bead와 관련 데이터를 영구 삭제합니다.

닫힌 임시 bead(wisp, 일시적 molecule)는 빠르게 쌓이며 닫힌 뒤에는 가치가 없습니다.
이 명령은 저장소 공간을 회수하기 위해 이를 제거합니다.

삭제: 일치하는 bead의 이슈, 의존성, 레이블, 이벤트, 댓글.
건너뜀: 고정된 bead(보호됨).

닫힌 비임시 bead(일반 task, feature, bug 등)를 삭제하려면
대신 `bd prune`을 사용하세요.

많은 행을 삭제한 후 Dolt 저장소 공간을 완전히 회수하려면 `bd flatten`을 이어서 실행해
이력을 축소하고 오래된 청크를 가비지 컬렉션할 수 있게 하세요.

예시:
  bd purge                           # 제거될 항목 미리 보기
  bd purge --force                   # 닫힌 임시 bead 모두 삭제
  bd purge --older-than 7d --force   # 닫힌 지 7일 이상 된 항목만 제거
  bd purge --pattern "*-wisp-*"      # 일치하는 ID 패턴만 제거
  bd purge --dry-run                 # 통계가 포함된 상세 미리 보기

```
bd purge [flags]
```

**플래그:**

```
      --dry-run             제거될 항목을 통계와 함께 미리 보기
  -f, --force               실제 제거(없으면 미리 보기 표시)
      --older-than string   N보다 오래전에 닫힌 bead만 제거(예: 7d, 2w, 30)
      --pattern string      ID glob 패턴과 일치하는 bead만 제거(예: *-wisp-*)
```
