---
title: "bd ping"
description: "bd가 데이터베이스에 연결할 수 있는지 확인하는 경량 상태 검사입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc ping`에서 생성되었습니다.

bd가 데이터베이스에 연결할 수 있는지 확인하는 경량 상태 검사입니다.

단계:
  1. .beads 워크스페이스 해석
  2. 저장소 열기(임베디드 또는 서버)
  3. 간단한 쿼리 실행(이슈 수)
  4. 소요 시간 보고

성공 시 0, 실패 시 1로 종료합니다.

예시:
  bd ping              # 빠른 연결 검사
  bd ping --json       # 자동화용 구조화 출력

```
bd ping [flags]
```
