---
title: "bd restore"
description: "압축된 이슈의 압축 전 콘텐츠를 복원합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc restore`에서 생성되었습니다.

압축된 이슈의 압축 전 콘텐츠를 복원합니다.

이슈를 압축하면 description/design/notes/acceptance criteria가 요약되고 원본은
압축 스냅샷에 보관됩니다. 이 명령은 해당 원본 콘텐츠를 복구합니다.

기본적으로 읽기 전용이며 데이터베이스를 수정하지 않고 보관된 콘텐츠를 표시합니다.
--apply를 전달하면 원본 콘텐츠를 이슈에 다시 쓰고 압축 수준을 한 단계 낮춥니다.

보관된 스냅샷이 없으면(예: 스냅샷 보관 전 구버전 bd에서 압축된 이슈) Dolt 버전
이력에서 최선의 방식으로 재구성합니다. 이 결과는 표시만 가능하고 적용할 수 없습니다.

```
bd restore <issue-id> [flags]
```

**플래그:**

```
      --apply   복원된 콘텐츠를 이슈에 다시 쓰기(기본값: 표시만)
      --json    복원 결과를 JSON 형식으로 출력
```
