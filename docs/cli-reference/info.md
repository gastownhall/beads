---
title: "bd info"
description: "현재 데이터베이스 정보를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc info`에서 생성되었습니다.

현재 데이터베이스 정보를 표시합니다.

bd가 예상하지 않은 데이터베이스를 사용하는 문제를 디버깅하는 데 도움이 됩니다. 다음을 표시합니다:
  - 데이터베이스 파일의 절대 경로
  - 데이터베이스 통계(이슈 수)
  - 스키마 정보(--schema 플래그 사용)
  - 최근 버전의 새 변경 사항(--whats-new 플래그 사용)

예시:
  bd info
  bd info --json
  bd info --schema --json
  bd info --whats-new
  bd info --whats-new --json
  bd info --thanks

```
bd info [flags]
```

**플래그:**

```
      --json        JSON 형식으로 출력
      --schema      출력에 스키마 정보 포함
      --thanks      기여자 감사 페이지 표시
      --whats-new   최근 버전의 에이전트 관련 변경 사항 표시
```
