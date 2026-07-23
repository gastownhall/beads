---
title: Gemini CLI
description: bd prime을 실행하는 SessionStart hook과 GEMINI.md 워크플로 지침으로 Gemini CLI용 Beads 설정
---

SessionStart hook과 `GEMINI.md` 지침을 통해 Gemini CLI에서 Beads를 사용합니다.

```bash
bd setup gemini
bd setup gemini --check
```

기본적으로 설정은 `~/.gemini/settings.json`에 전역 hook을 설치합니다. 프로젝트 로컬 hook을 사용하려면 다음을 실행하세요.

```bash
bd setup gemini --project
```

hook은 `bd prime --hook-json`을 실행하여 세션 시작 시 Gemini에 간결한 Beads 워크플로 컨텍스트를 전달합니다. 설정 과정은 `GEMINI.md`에도 Beads 지침을 작성합니다.

## Stealth 모드

설정 중 Git 작업을 피해야 하는 CI 등의 환경에서는 다음을 실행하세요.

```bash
bd setup gemini --stealth
bd setup gemini --project --stealth
```

## 제거

```bash
bd setup gemini --remove
bd setup gemini --project --remove
```
