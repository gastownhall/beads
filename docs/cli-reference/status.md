---
title: "bd status"
description: "이슈 데이터베이스 상태와 통계의 빠른 스냅샷을 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc status`에서 생성되었습니다.

이슈 데이터베이스 상태와 통계의 빠른 스냅샷을 표시합니다.

이 명령은 상태별(open, in_progress, blocked, closed) 이슈 수, 준비된 작업,
확장 통계(고정된 이슈, 평균 리드 타임), git 이력의 최근 24시간 활동 요약을
제공합니다.

'git status'가 작업 트리 상태를 보여 주는 것과 유사하게 'bd status'는 여러 쿼리 없이
이슈 데이터베이스의 빠른 개요를 제공합니다.

사용 사례:
  - 빠른 프로젝트 상태 검사
  - 새 기여자 온보딩
  - 셸 프롬프트 또는 CI/CD와 통합
  - 일일 스탠드업 참고

예시:
  bd status                    # 활동이 포함된 요약 표시
  bd status --no-activity      # git 활동 건너뛰기(더 빠름)
  bd status --json             # JSON 형식 출력
  bd status --assigned         # 현재 사용자에게 할당된 이슈 표시
  bd stats                     # bd status의 별칭

```
bd status [flags]
```

**별칭:** stats

**플래그:**

```
      --all           모든 이슈 표시(기본 동작)
      --assigned      현재 사용자에게 할당된 이슈 표시
      --no-activity   git 활동 추적 건너뛰기(더 빠름)
```
