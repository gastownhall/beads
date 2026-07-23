---
title: "bd show"
description: "이슈 세부 정보를 표시합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc show`에서 생성되었습니다.

이슈 세부 정보를 표시합니다

```
bd show [id...] [--id=<id>...] [--current] [flags]
```

**별칭:** view

**플래그:**

```
      --as-of string         특정 커밋 해시 또는 브랜치 시점의 이슈 표시(Dolt 필요)
      --children             이 이슈의 하위 이슈만 표시
      --current              현재 활성 이슈 표시(in-progress, hooked 또는 마지막으로 접근)
      --id stringArray       이슈 ID(플래그처럼 보이는 ID에 사용, 예: --id=gt--xyz)
      --include-comments     JSON 출력에 전체 댓글 본문 스트리밍(--json 전용, 댓글이 많은 이슈에서 느릴 수 있음)
      --include-dependents   JSON 출력에 전체 종속 이슈 스트리밍(--json 전용, hub bead에서 느릴 수 있음)
      --local-time           UTC 대신 현지 시간으로 타임스탬프 표시
      --long                 사용 가능한 모든 필드 표시(확장 메타데이터, 에이전트 신원, gate 필드 등)
      --refs                 이 이슈를 참조하는 이슈 표시(역방향 조회)
      --short                이슈별 간결한 한 줄 출력 표시
      --thread               전체 대화 스레드 표시(메시지용)
  -w, --watch                변경을 감시하고 표시 자동 새로 고침
```
