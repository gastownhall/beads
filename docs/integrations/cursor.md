---
title: Cursor
description: 항상 적용되는 프로젝트 규칙 파일로 Cursor용 Beads 설정
---

매 턴마다 에이전트에 워크플로 지침을 제공하는 프로젝트 규칙 파일을 통해
Cursor에서 Beads를 사용합니다.

```bash
bd setup cursor
bd setup cursor --check
```

`bd setup cursor`는 정식 Beads 워크플로 지침(다른 모든 편집기 통합과 같은 내용)이
담긴 항상 적용되는 규칙 **`.cursor/rules/beads.mdc`**를 설치합니다. 따라서 워크플로가
발전해도 동기화된 상태를 유지합니다. 항상 적용되므로 Cursor는 컨텍스트 압축 후를
포함하여 매 턴 이 규칙을 다시 포함합니다.

규칙이 로드되도록 설치 후 Cursor를 다시 시작하세요.

## 작동 확인

```bash
bd setup cursor --check
```

세션에서 별도로 알려 주지 않아도 에이전트가 `bd` 워크플로를 알아야 합니다. 준비된
작업을 물으면 `bd ready`를 실행해야 합니다. 실행하지 않으면
`.cursor/rules/beads.mdc`가 있는지 확인하고 Cursor를 다시 시작하세요.

컨텍스트 압축 후에는 에이전트가 `bd prime`을 실행하여 워크플로 컨텍스트, 준비된
작업, 프로젝트 메모리를 복원하게 하세요.

## 제거

```bash
bd setup cursor --remove
```

이 명령은 `.cursor/`의 나머지 내용은 그대로 두고 규칙 파일만 제거합니다.

## 관련 문서

- [IDE 설정](/getting-started/ide-setup) — 모든 편집기 통합
- [Claude Code](/integrations/claude-code)
- [Codex](/integrations/codex)
