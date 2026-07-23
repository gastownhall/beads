---
title: "bd q"
description: "빠른 캡처로 이슈를 생성하고 이슈 ID만 출력합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc q`에서 생성되었습니다.

빠른 캡처로 이슈를 생성하고 이슈 ID만 출력합니다.
스크립팅과 AI 에이전트 통합용으로 설계되었습니다.

예시:
  bd q "로그인 버그 수정"          # 출력: bd-a1b2
  ISSUE=$(bd q "새 기능")          # 변수에 ID 저장
  bd q "작업" | xargs bd show      # 다른 명령으로 파이프

```
bd q [title] [flags]
```

**플래그:**

```
  -l, --labels strings    레이블
  -p, --priority string   우선순위(0-4 또는 P0-P4)(기본값 "2")
  -t, --type string       이슈 유형(기본값 "task")
```
