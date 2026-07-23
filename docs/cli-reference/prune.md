---
title: "bd prune"
description: "닫힌 비임시 bead와 관련 데이터를 영구 삭제합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc prune`에서 생성되었습니다.

닫힌 비임시 bead와 관련 데이터를 영구 삭제합니다.

더 이상 유용하지 않은 닫힌 일반 bead(task, feature, bug, chore 등)를 정리할 때
사용합니다. 일반적인 사례는 수명이 긴 저장소에서 닫힌 작업이 쌓여
자동 내보내기가 비대해지거나 쿼리가 느려지는 경우입니다.

--older-than 또는 --pattern이 필요합니다. 이 플래그는 안전장치입니다.
없으면 습관적으로 입력한 `--force`가 저장소의 모든 닫힌 bead를 지울 수 있습니다.
닫힌 항목을 모두 정리하려는 경우 `--pattern '*'`을 사용하세요.

삭제: 일치하는 bead의 이슈, 의존성, 레이블, 이벤트, 댓글.
건너뜀: 고정된 bead(보호됨), open/in-progress bead, 임시 bead.

닫힌 임시 bead(wisp, 일시적 molecule)를 삭제하려면
대신 `bd purge`를 사용하세요.

많은 행을 삭제한 후 Dolt 저장소 공간을 완전히 회수하려면 `bd flatten`을 이어서 실행해
이력을 축소하고 오래된 청크를 가비지 컬렉션할 수 있게 하세요.

예시:
  bd prune --older-than 30d              # 닫힌 지 30일 넘은 bead 미리 보기
  bd prune --older-than 30d --force      # 해당 bead 삭제
  bd prune --older-than 90d --dry-run    # 통계가 포함된 상세 미리 보기
  bd prune --pattern "*" --force         # 닫힌 일반 bead 모두 삭제
  bd prune --pattern "gm-temp-*" --force # 패턴으로 범위 제한

```
bd prune [flags]
```

**플래그:**

```
      --dry-run             정리될 항목을 통계와 함께 미리 보기
  -f, --force               실제 정리(없으면 미리 보기 표시)
      --older-than string   N보다 오래전에 닫힌 bead만 정리(예: 30d, 2w, 60)
      --pattern string      ID glob 패턴과 일치하는 bead만 정리(예: 'gm-old-*')
```
