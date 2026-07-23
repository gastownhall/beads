---
title: 통합
description: bd setup 레시피부터 MCP 기반 클라이언트까지 모든 Beads 편집기 및 에이전트 통합 살펴보기
---

Beads 통합 페이지는 저장소에 있는 두 가지 지원 소스를 기반으로 합니다.

- `internal/recipes/recipes.go`의 내장 `bd setup` 레시피
- 저장소에 이미 포함된 공식 MCP 통합 및 편집기 가이드

설치된 `bd` 바이너리가 지원하는 내장 설정 레시피를 보려면 다음 명령을 실행하세요.

```bash
bd setup --list
```

## 내장 설정 레시피

| 레시피 | 통합 | 주요 설정 위치 |
|--------|-------------|-----------------------|
| `aider` | [Aider](/integrations/aider) | `.aider.conf.yml` 및 `.aider/` 지침 |
| `claude` | [Claude Code](/integrations/claude-code) | Claude hook 및 `CLAUDE.md` |
| `codex` | [Codex](/integrations/codex) | Beads 스킬, `AGENTS.md`, Codex 훅 |
| `cody` | [Sourcegraph Cody](/integrations/cody) | `.cody/rules/beads.md` |
| `cursor` | [Cursor](/integrations/cursor) | `.cursor/rules/beads.mdc` 및 `.cursor/hooks.json` |
| `factory` | [Factory.ai Droid](/integrations/factory) | `AGENTS.md` |
| `gemini` | [Gemini CLI](/integrations/gemini) | Gemini hook 및 `GEMINI.md` |
| `junie` | [Junie](/integrations/junie) | `.junie/guidelines.md` 및 MCP 설정 |
| `kilocode` | [Kilo Code](/integrations/kilocode) | `.kilocode/rules/beads.md` |
| `mux` | [Mux](/integrations/mux) | `AGENTS.md`, 선택적 `.mux/AGENTS.md`, Mux hook |
| `opencode` | [OpenCode](/integrations/opencode) | `AGENTS.md` |
| `windsurf` | [Windsurf](/integrations/windsurf) | `.windsurf/rules/beads.md` |

## MCP 기반 통합

다음 통합은 전용 `bd setup` 레시피 대신 Beads MCP 서버를 사용합니다.

- [MCP 서버](/integrations/mcp-server) — 모든 MCP 지원 클라이언트에서 사용할 수 있는
  Beads MCP 서버입니다.
- [GitHub Copilot](/integrations/github-copilot) — MCP를 통해 VS Code에서 사용하는
  Copilot입니다.
- [GitHub Copilot CLI](/integrations/copilot-cli) — Copilot 코딩 에이전트 CLI입니다.

## 기타 통합 방식

- [Claude Code 플러그인](/integrations/claude-code-plugin) — slash 명령과 MCP 도구가
  포함된 패키지형 플러그인입니다(`/plugin install beads`).
- [Azure DevOps](/integrations/azure-devops) — Beads를 ADO 작업 항목과 동기화하기 위한
  설정 참조입니다.
