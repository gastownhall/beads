---
title: MCP 서버
description: bd CLI를 사용할 수 없는 Claude Desktop 같은 MCP 전용 환경에서 beads-mcp 서버 실행
---

MCP 전용 환경에서 Beads를 사용합니다.

## MCP 사용 시점

CLI를 사용할 수 없을 때 MCP 서버를 사용하세요.

- Claude Desktop(shell 접근 없음)
- shell 없는 Sourcegraph Amp
- 기타 MCP 전용 환경

shell을 사용할 수 있다면 컨텍스트 효율이 높은 **CLI 및 hook**을 권장합니다.

## 설치

### uv 사용(권장)

```bash
uv tool install beads-mcp
```

### pip 사용

```bash
pip install beads-mcp
```

## 설정

### Claude Desktop (macOS)

`~/Library/Application Support/Claude/claude_desktop_config.json`에 추가하세요.

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

### Claude Desktop (Windows)

`%APPDATA%\Claude\claude_desktop_config.json`에 추가하세요.

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

### Sourcegraph Amp

MCP 설정에 추가하세요.

```json
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

### VS Code / GitHub Copilot

프로젝트에 `.vscode/mcp.json`을 생성하세요.

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**모든 프로젝트에 적용:** VS Code 사용자 수준 MCP 설정에 추가하세요.

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

**참고:** MCP 지원이 활성화된 VS Code 1.96 이상이 필요합니다.

전체 설정 가이드는 [GitHub Copilot 통합](/integrations/github-copilot)을 참고하세요.

## 사용 가능한 도구

MCP 서버는 다음 도구를 노출합니다.

| 도구 | 설명 |
|------|-------------|
| `ready` | 준비된 작업 표시(열린 차단 요소 없음) |
| `list` | 필터를 적용하여 이슈 나열 |
| `show` | 이슈 상세 정보, 의존성, 종속 항목 표시 |
| `create` | 새 이슈 생성 |
| `claim` | 이슈를 원자적으로 claim |
| `update` | 이슈 업데이트 |
| `close` / `reopen` | 이슈 종료 또는 다시 열기 |
| `dep` | 의존성 관리 |
| `comment` / `comments` | 댓글 추가 또는 나열 |
| `note` | 이슈 notes에 추가 |
| `blocked` | 차단된 이슈와 차단 요소 표시 |
| `stats` / `context` | 데이터베이스 통계 및 워크스페이스 컨텍스트 |
| `admin` | 관리 작업 |
| `discover_tools` / `get_tool_info` | 도구 탐색 및 schema |

MCP 동기화 도구는 없습니다. 동기화는 CLI(`bd dolt push` / `bd dolt pull`)에서
수행합니다.

## 사용법

설정 후 자연어로 사용하세요.

```
로그인 버그 수정 이슈를 우선순위 1로 생성해 줘
```

MCP 서버가 적절한 `bd` 명령으로 변환합니다.

## 절충점

| 측면 | CLI 및 hook | MCP 서버 |
|--------|-------------|------------|
| 컨텍스트 오버헤드 | 약 1~2천 토큰 | 1만~5만 토큰 |
| 지연 시간 | 직접 호출 | MCP protocol |
| 설정 | hook 설정 | MCP 설정 |
| 가용성 | shell 필요 | MCP 환경 |

## 문제 해결

### 서버가 시작되지 않음

`beads-mcp`가 PATH에 있는지 확인하세요.

```bash
which beads-mcp
```

찾을 수 없다면 다음을 실행하세요.

```bash
# 다시 설치
pip uninstall beads-mcp
pip install beads-mcp
```

### 도구가 표시되지 않음

1. Claude Desktop을 다시 시작합니다.
2. MCP 설정 JSON 문법을 확인합니다.
3. 서버 경로를 확인합니다.

### 권한 오류

```bash
# 디렉터리 권한 확인
ls -la .beads/

# 필요한 경우 초기화
bd init --quiet
```

## 관련 문서

- [Claude Code](/integrations/claude-code) - CLI 통합
- [설치](/getting-started/installation) - 전체 설치 가이드
