---
title: "bd delete"
description: "하나 이상의 이슈와 해당 이슈의 모든 참조를 삭제합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc delete`에서 생성되었습니다.

하나 이상의 이슈와 해당 이슈의 모든 참조를 삭제합니다.
이 명령은 다음을 수행합니다:
1. 이슈와 관련된 모든 의존성 링크 제거(모든 유형, 양방향)
2. 직접 연결된 이슈의 텍스트 참조를 "[deleted:ID]"로 업데이트
3. 데이터베이스에서 이슈 영구 삭제

되돌릴 수 없는 파괴적 작업입니다. 주의해서 사용하세요.

일괄 삭제:
여러 이슈를 한 번에 삭제:
  bd delete bd-1 bd-2 bd-3 --force

파일에서 삭제(한 줄에 ID 하나):
  bd delete --from-file deletions.txt --force

삭제 전 미리 보기:
  bd delete --from-file deletions.txt --dry-run

의존성 처리:
기본값: 삭제 집합에 없는 종속 항목이 이슈에 있으면 실패
  bd delete bd-1 bd-2

연쇄: 모든 종속 항목을 재귀적으로 삭제
  bd delete bd-1 --cascade --force

강제: 삭제하고 종속 항목을 고립시킴
  bd delete bd-1 --force

```
bd delete <issue-id> [issue-id...] [flags]
```

**플래그:**

```
      --cascade            모든 종속 이슈를 재귀적으로 삭제
      --dry-run            변경하지 않고 삭제될 항목 미리 보기
  -f, --force              실제 삭제(이 플래그가 없으면 미리 보기 표시)
      --from-file string   파일에서 이슈 ID 읽기(한 줄에 하나)
```
