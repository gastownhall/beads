---
title: "bd create-form"
description: "대화형 터미널 양식으로 새 이슈를 생성합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc create-form`에서 생성되었습니다.

대화형 터미널 양식으로 새 이슈를 생성합니다.

이 명령은 제목, 설명, 유형, 우선순위, 레이블 등의 필드가 있는 사용자 친화적인
이슈 생성 양식 인터페이스를 제공합니다.

기존 상위 이슈 아래에 하위 이슈를 생성하려면 --parent를 사용하세요.
하위 이슈에는 자동 생성된 계층형 ID(예: parent-id.1)가 부여됩니다.

양식은 키보드 탐색을 사용합니다:
  - Tab/Shift+Tab: 필드 간 이동
  - Enter: 양식 제출(마지막 필드 또는 제출 버튼에서)
  - Ctrl+C: 취소 후 종료
  - 방향키: 선택 필드 내 탐색

```
bd create-form [flags]
```

**플래그:**

```
      --parent string   계층형 하위 이슈를 생성할 상위 이슈 ID(예: 'bd-a3f8e9')
```
