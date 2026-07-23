---
title: "bd preflight"
description: "기여자를 위한 일반적인 PR 전 검사 체크리스트를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc preflight`에서 생성되었습니다.

기여자를 위한 일반적인 PR 전 검사 체크리스트를 표시합니다.

이 명령은 CI로 push하기 전에 일반적인 문제를 찾는 데 도움이 됩니다:
- 로컬에서 실행하지 않은 테스트
- 린트 오류
- 포맷되지 않은 Go 파일
- .beads/issues.jsonl 오염
- 오래된 nix vendorHash
- 버전 불일치

예시:
  bd preflight              # 체크리스트 표시
  bd preflight --check      # 검사 자동 실행
  bd preflight --check --json  # 프로그래밍용 JSON 출력
  bd preflight --check --skip-lint  # 린트 검사 명시적으로 건너뛰기


```
bd preflight [flags]
```

**플래그:**

```
      --check       검사 자동 실행
      --fix         가능한 경우 이슈 자동 수정(아직 구현되지 않음)
      --json        결과를 JSON으로 출력
      --skip-lint   린트 검사를 명시적으로 건너뛰기
```
