---
title: GitHub Copilot
description: beads-mcp 서버를 통해 VS Code의 Copilot Chat에서 자연어로 이슈를 추적하며 Beads 사용
---

Beads는 Copilot에 작업 추적을 위한 영구적이고 구조화된 메모리를 제공합니다. MCP
서버를 설정하면 편집기를 떠나지 않고 자연어로 이슈를 생성·업데이트·추적할 수 있습니다.

이 페이지에서는 **MCP를 통한 VS Code의 Copilot Chat**을 다룹니다. `bd setup
copilot`으로 설치하는 터미널 기반 Copilot CLI 플러그인은 [Copilot
CLI](/integrations/copilot-cli)를 참고하세요.

## 사전 요구 사항

- GitHub Copilot 확장이 있는 VS Code 1.96 이상
- GitHub Copilot 구독(Individual, Business 또는 Enterprise)
- Beads CLI 설치([설치 가이드](/getting-started/installation))
- Python 3.10 이상 또는 `uv` package manager

## 설정

### 빠른 설정

1. beads-mcp를 설치합니다.
   ```bash
   # uv 사용(권장)
   uv tool install beads-mcp

   # 또는 pip/pipx 사용
   pip install beads-mcp
   pipx install beads-mcp
   ```

2. 프로젝트에 `.vscode/mcp.json`을 생성합니다.
   ```json
   {
     "servers": {
       "beads": {
         "command": "beads-mcp"
       }
     }
   }
   ```

   **모든 프로젝트에 적용:** VS Code 사용자 수준 MCP 설정에 추가합니다.

   | 플랫폼 | 경로 |
   |----------|------|
   | macOS | `~/Library/Application Support/Code/User/mcp.json` |
   | Linux | `~/.config/Code/User/mcp.json` |
   | Windows | `%APPDATA%\Code\User\mcp.json` |

   ```json
   {
     "servers": {
       "beads": {
         "command": "beads-mcp",
         "args": []
       }
     }
   }
   ```

3. Beads를 초기화합니다.
   ```bash
   bd init --quiet
   ```

   이슈 데이터베이스가 있는 `.beads/` 디렉터리를 생성합니다.

4. VS Code를 다시 로드합니다.

### 설정 확인

Copilot Chat에 "작업할 준비가 된 Beads 이슈는 무엇인가요?"라고 물어보세요.

## 자연어 사용

MCP를 설정한 뒤 자연어로 대화하세요.

```
사용자: 로그인 timeout 버그를 만들어 줘
Copilot: bd-42 생성: 로그인 timeout 버그

사용자: 준비된 이슈는 무엇이야?
Copilot: 준비된 이슈 3개: bd-42, bd-99, bd-17

사용자: bd-42를 claim해 줘. 내가 맡을게
Copilot: bd-42를 claim하고 작업 시작

사용자: 관련 버그를 발견했어. session token이 갱신되지 않아.
       bd-42에 연결해서 등록해 줘.
Copilot: bd-103 생성: session token이 갱신되지 않음
         bd-42의 discovered-from으로 연결

사용자: bd-42를 "timeout 처리 수정" 사유로 종료해 줘
Copilot: bd-42 종료: timeout 처리 수정
```

동기화는 CLI에서 수행합니다. 세션 종료 시 `bd dolt push`를 실행하세요. MCP push
도구는 없습니다.

## MCP 도구

| 도구 | 설명 | 요청 예시 |
|------|-------------|---------|
| `ready` | 차단되지 않은 이슈 나열 | "무엇을 작업할 수 있어?" |
| `list` | 필터를 적용하여 이슈 나열 | "열린 버그를 모두 보여 줘" |
| `show` | 의존성과 종속 항목을 포함한 이슈 상세 정보 표시 | "bd-42 상세 정보를 보여 줘" |
| `create` | 새 이슈 생성 | "refactoring 작업을 만들어 줘" |
| `claim` | 이슈를 원자적으로 claim(assignee 및 in_progress) | "bd-42를 내가 맡을게" |
| `update` | 이슈 필드 업데이트 | "bd-42를 우선순위 1로 설정해 줘" |
| `close` | 이슈 종료 | "bd-42를 완료해 줘" |
| `dep` | 의존성 추가 | "bd-99가 bd-42를 차단해" |
| `blocked` | 차단된 이슈와 차단 요소 표시 | "내 작업을 무엇이 차단하고 있어?" |
| `stats` | 이슈 수와 평균 lead time | "backlog 상태가 어때?" |

서버는 `reopen`, `comment`, `comments`, `note`, `context`, `admin`도 노출합니다.
전체 catalog는 `discover_tools`를 호출하여 확인하세요.

## Copilot 지침

선택적으로 `.github/copilot-instructions.md`를 추가하세요.

```markdown
## 이슈 추적

이 프로젝트는 이슈 추적에 **bd(Beads)**를 사용합니다.
워크플로 컨텍스트가 필요하면 `bd prime`을 실행합니다.

빠른 참조:
- `bd ready` - 차단되지 않은 작업 찾기
- `bd create "제목" --type task --priority 2` - 이슈 생성
- `bd close <id>` - 작업 완료
- `bd dolt push` - 변경 사항을 Dolt 원격에 push(세션 종료 시 실행)
```

## CLI와 MCP 비교

| 방식 | 적합한 용도 | 절충점 |
|----------|----------|-----------|
| **MCP(Copilot Chat)** | 자연어, 탐색 | 더 큰 토큰 오버헤드 |
| **CLI(터미널)** | scripting, 정밀성, 속도 | shell 접근 필요 |

둘 다 같은 데이터베이스에서 작동합니다. 대화형 작업에는 MCP를, 빠른 명령에는 CLI를
사용하세요. 전체 절충점은 [MCP 서버](/integrations/mcp-server)를 참고하세요.

## 문제 해결

### 도구가 표시되지 않음

1. VS Code 1.96 이상인지 확인합니다.
2. `mcp.json` 문법이 유효한 JSON인지 확인합니다.
3. VS Code window를 다시 로드합니다.
4. Output panel에서 MCP 오류를 확인합니다.

### "beads-mcp not found"

```bash
# 설치 확인
which beads-mcp
pip show beads-mcp

# uv는 ~/.local/bin에 설치하므로 PATH 포함 여부 확인
export PATH="$HOME/.local/bin:$PATH"

# pip로 설치했다면 위치 찾기
pip show beads-mcp | grep Location

# 필요한 경우 다시 설치
uv tool install beads-mcp --force
```

### 데이터베이스를 찾을 수 없음

```bash
bd init --quiet
```

### 변경 사항이 유지되지 않음

세션 종료 시 터미널에서 Dolt 원격으로 push하세요.

```bash
bd dolt push
```

### 조직 정책이 MCP를 차단함

Copilot Business/Enterprise에서는 조직이 `MCP servers in Copilot` 정책을 활성화해야
합니다. 설정이 올바른데도 MCP 도구가 표시되지 않으면 관리자에게 문의하세요.

## 자주 묻는 질문

### Beads를 클론해야 하나요?

**아니요.** Beads는 시스템 전체 CLI 도구입니다. 한 번 설치하고 어디서나 사용하세요.
프로젝트의 `.beads/` 디렉터리에는 이슈 데이터베이스만 들어 있습니다.

### Git hook은 어떤 역할을 하나요?

Git hook은 선택 사항입니다. export와 legacy fallback 검사를 갱신하며 이슈 동기화에는
`bd dolt push` / `bd dolt pull`을 사용합니다. 소스 코드를 수정하지 않으며 `bd init
--skip-hooks`로 건너뛸 수 있습니다.

### Copilot 없이 Beads를 사용할 수 있나요?

예. 같은 데이터베이스를 터미널, [Claude Code](/integrations/claude-code),
[Cursor](/integrations/cursor), [Aider](/integrations/aider), MCP 또는 shell에 접근할
수 있는 모든 편집기에서 사용할 수 있습니다.

### 다른 편집기의 Copilot에서도 작동하나요?

이 페이지는 VS Code를 다룹니다. JetBrains IDE에서는 해당 IDE의 MCP 지원 여부를
확인하세요. 설정 위치가 다릅니다. Neovim에서는 CLI를 직접 사용하고, 터미널에서는
[Copilot CLI](/integrations/copilot-cli)를 참고하세요.

## 관련 문서

- [MCP 서버](/integrations/mcp-server) - 상세 MCP 설정
- [Copilot CLI](/integrations/copilot-cli) - 터미널 기반 Copilot 통합
- [빠른 시작](/getting-started/quickstart) - `bd` 명령 기본 사항
- [설치](/getting-started/installation) - 전체 설치 가이드
- [에이전트 지침](https://github.com/gastownhall/beads/blob/main/AGENT_INSTRUCTIONS.md) - 전체 에이전트 워크플로 참조
