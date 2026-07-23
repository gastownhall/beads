---
title: "bd onboard"
description: "bd 통합을 위해 에이전트 지침 파일에 추가할 최소 스니펫을 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc onboard`에서 생성되었습니다.

bd 통합을 위해 에이전트 지침 파일에 추가할 최소 스니펫을 표시합니다.

기본 에이전트 지침 파일은 AGENTS.md입니다. 다른 파일 이름(예: BEADS.md)을
구성하려면 'bd init --agents-file'을 사용하세요.

전체 워크플로 컨텍스트를 위해 'bd prime'을 가리키는 작은 스니펫(약 10줄)을
출력합니다. 이는 'bd init'이 기본적으로 생성하는 것과 같은 최소 프로필입니다.
이 접근 방식은 다음과 같습니다:

  • 에이전트 파일을 간결하게 유지(지침으로 비대해지지 않음)
  • bd prime이 동적이며 항상 최신인 워크플로 세부 정보 제공
  • 훅이 세션 시작 시 bd prime을 자동 주입

훅 출력을 자동 주입하지 않는 에이전트나 환경에서는 전체 명령 참조를 포함하도록
'bd init --agents-profile=full'을 사용하세요.

```
bd onboard [flags]
```
