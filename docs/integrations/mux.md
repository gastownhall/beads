---
title: Mux
description: 관리형 AGENTS.md 섹션, 선택적 계층형 지침 파일, Mux hook으로 Mux용 Beads 설정
---

`AGENTS.md`, 선택적 계층형 Mux 지침 파일, Mux hook을 통해 Mux에서 Beads를 사용합니다.

```bash
bd setup mux
bd setup mux --check
```

기본 설정은 루트 `AGENTS.md`에 관리형 Beads 섹션을 작성합니다.

## 워크스페이스 및 전역 계층

Mux는 워크스페이스 및 전역 지침 계층도 지원합니다.

```bash
bd setup mux --project
bd setup mux --global
bd setup mux --project --global
```

프로젝트 설정은 `.mux/AGENTS.md`를 작성하고 `.mux/` 아래에 Mux hook 파일을 설치합니다.

- `.mux/init`
- `.mux/tool_post`
- `.mux/tool_env`

전역 설정은 `~/.mux/AGENTS.md`를 작성합니다.

## 제거

```bash
bd setup mux --remove
bd setup mux --project --remove
bd setup mux --global --remove
```
