---
title: "bd recompute-blocked"
description: "모든 이슈와 wisp의 비정규화된 is_blocked 플래그를 다시 계산합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc recompute-blocked`에서 생성되었습니다.

모든 이슈와 wisp의 비정규화된 is_blocked 플래그를 다시 계산합니다.

is_blocked는 의존성 그래프에서 파생되며 로컬 쓰기와 병합에서 변경된 범위에 대한
pull 후 재계산으로 자동 유지됩니다. 병합 커밋 후 재계산에 실패하거나 충돌한 pull을
수동으로 해결하여 범위 재계산을 건너뛰면 플래그가 오래될 수 있고, 이후 아무것도
병합하지 않는 pull은 이를 새로 고치지 않습니다(bd-6dnrw.37). 'bd ready'는 이 플래그를
신뢰하므로 오래된 값이 준비된 작업을 조용히 숨기거나 차단된 작업을 노출할 수 있습니다.

이 명령은 조건 없이 전체 재계산을 실행하고 결과를 커밋합니다.
멱등성을 가지므로 일관된 데이터베이스에서는 아무것도 변경하지 않습니다. 서버 모드
전용인 'bd doctor'와 달리 임베디드 모드와 서버 모드에서 모두 작동합니다.

예시:
  bd recompute-blocked          # 오래된 is_blocked 플래그 복구
  bd recompute-blocked --json   # 기계 판독 가능한 &#123;"rows_corrected": N&#125;

```
bd recompute-blocked [flags]
```
