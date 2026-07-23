---
title: "bd rename-prefix"
description: "데이터베이스의 모든 이슈 접두사를 변경합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc rename-prefix`에서 생성되었습니다.

데이터베이스의 모든 이슈 접두사를 변경합니다.
모든 이슈 ID와 모든 필드의 텍스트 참조를 업데이트합니다.

사용 사례:
- 긴 접두사 단축(예: 'knowledge-work-' → 'kw-')
- 프로젝트 명명 규칙 리브랜딩
- 데이터베이스 손상 후 여러 접두사 통합
- 팀 명명 표준으로 마이그레이션

접두사 검증 규칙:
- 최대 길이: 8자
- 허용 문자: 소문자, 숫자, 하이픈
- 문자로 시작해야 함
- 하이픈으로 끝나야 함(예: 'kw-', 'work-')
- 비어 있거나 하이픈 하나만 사용할 수 없음

여러 접두사 감지 및 복구:
이슈에 여러 접두사가 있으면(손상된 데이터베이스) --repair로 통합하세요.
--repair 플래그는 잘못된 접두사가 있는 모든 이슈를 새 접두사로 변경하며,
이미 올바른 접두사가 있는 이슈는 보존합니다.

예시:
  bd rename-prefix kw-                # 'knowledge-work-'를 'kw-'로 변경
  bd rename-prefix mtg- --repair      # 여러 접두사를 'mtg-'로 통합
  bd rename-prefix team- --dry-run    # 적용하지 않고 변경 사항 미리 보기

참고: 드물게 수행하는 작업입니다. 대부분의 사용자는 이 명령이 필요하지 않습니다.

```
bd rename-prefix <new-prefix> [flags]
```

**플래그:**

```
      --dry-run   적용하지 않고 변경 사항 미리 보기
      --repair    여러 접두사가 있는 데이터베이스를 통합하여 복구
```
