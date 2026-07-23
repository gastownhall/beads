---
title: Claude Code
description: MCP 대신 CLI를 사용하고 컨텍스트를 준비하는 SessionStart hook으로 Claude Code에 Beads 연결
---

Claude Code에서 Beads를 사용하는 방법입니다.

## 설정

### 빠른 설정

```bash
bd setup claude
```

다음 항목을 설치합니다.

- **SessionStart hook** - 세션 시작 시 `bd prime --hook-json`을 실행합니다. SessionStart는 컨텍스트 압축 후에도 실행되므로 같은 hook이 컨텍스트를 자동 갱신합니다.
- **CLAUDE.md 포인터** - 프로젝트 `CLAUDE.md`의 minimal Beads 섹션입니다. `CLAUDE.md`가 symlink이면 건너뜁니다.

기본적으로 hook은 프로젝트의 `.claude/settings.json`에 작성됩니다. 변형은 다음과 같습니다.

```bash
bd setup claude --global   # 대신 ~/.claude/settings.json에 설치
bd setup claude --stealth  # Stealth 모드: flush만 수행, Git 작업 없음
bd setup claude --remove   # hook과 CLAUDE.md 섹션 제거
```

[Beads 플러그인](/integrations/claude-code-plugin)이 활성화되어 있으면 플러그인이
자체 hook을 제공하므로 `bd setup claude`는 hook 작성을 건너뜁니다. 중복되면 세션마다
`bd prime`이 두 번 실행됩니다.

### 수동 설정

`.claude/settings.json`(프로젝트) 또는 `~/.claude/settings.json`(전역)에 추가하세요.

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "bd prime --hook-json" }
        ]
      }
    ]
  }
}
```

`--hook-json` flag는 출력을 Claude Code가 기대하는 hook JSON envelope로 감쌉니다.
압축 후 SessionStart가 다시 실행되므로 `PreCompact` hook은 필요하지 않습니다.

### 설정 확인

```bash
bd setup claude --check
```

## 작동 방식

1. **세션 시작**: `bd prime`이 약 1~2천 token의 컨텍스트를 주입합니다.
2. **작업**: `bd` CLI 명령을 직접 사용합니다.
3. **세션 압축**: SessionStart가 다시 실행되고 `bd prime`이 워크플로 컨텍스트를 갱신합니다.
4. **세션 종료**: `bd dolt push`가 변경 사항을 동기화합니다.

### MCP 대신 CLI 및 hook을 사용하는 이유

컨텍스트 효율 때문입니다. MCP 도구 schema는 요청마다 1만~5만 token을 추가할 수
있지만 `bd prime`은 약 1~2천 token의 워크플로 컨텍스트를 추가합니다. overhead가
10~50배 적어 비용과 지연 시간이 줄고 모델의 주의력이 향상됩니다. shell에 접근할 수
있는 환경에서는 CLI 및 hook을 권장하며, Claude Desktop처럼 CLI를 사용할 수 없는
곳에서만 [MCP 서버](/integrations/mcp-server)를 사용하세요.

### Claude Skill을 사용하지 않는 이유

Beads는 Claude Skill(`.claude/skills/`)을 제공하거나 요구하지 않습니다. `bd prime`이
이미 워크플로 컨텍스트를 전달하며, 워크플로는 ready, create, update, close, sync라는
간단한 명령 집합에 맞습니다. Skill은 Claude 전용이므로 편집기에 독립적인 Beads의
방식과 맞지 않습니다. 같은 CLI가 Cursor, Windsurf 등 shell 지원 편집기에서 모두
작동합니다. Beads 위에 자체 Skill을 만들 수 있지만 필수는 아닙니다.

## 에이전트 필수 명령

### 이슈 생성

```bash
# 컨텍스트를 위해 항상 설명 포함
bd create "인증 버그 수정" \
  --description="비밀번호에 특수 문자가 있으면 로그인 실패" \
  -t bug -p 1 --json

# 발견한 이슈 연결
bd create "SQL injection 발견" \
  --description="query builder에서 사용자 입력이 sanitize되지 않음" \
  --deps discovered-from:bd-42 --json
```

### 이슈 작업

```bash
# 준비된 작업 찾기
bd ready --json

# 작업 시작
bd update bd-42 --claim --json

# 작업 완료
bd close bd-42 --reason "commit abc123에서 수정" --json
```

### 조회

```bash
# 열린 이슈 나열
bd list --status open --json

# 이슈 상세 정보 표시
bd show bd-42 --json

# 차단된 이슈 확인
bd blocked --json
```

### 동기화

```bash
# 세션 종료 시 항상 실행
bd dolt push
```

## 모범 사례

### 항상 `--json` 사용

```bash
bd list --json            # 프로그래밍 방식으로 parse
bd create "작업" --json  # 출력에서 이슈 ID 가져오기
bd show bd-42 --json      # 구조화된 데이터
```

### 항상 설명 포함

```bash
# 좋은 예
bd create "인증 버그 수정" \
  --description="비밀번호에 따옴표가 있으면 로그인 실패" \
  -t bug -p 1 --json

# 나쁜 예 - 향후 작업을 위한 컨텍스트 없음
bd create "인증 버그 수정" -t bug -p 1 --json
```

### 관련 작업 연결

```bash
# 작업 중 이슈를 발견한 경우
bd create "관련 버그 발견" \
  --deps discovered-from:bd-current --json
```

### 세션 종료 전 push

```bash
# 종료 전에 항상 실행
bd dolt push
```

## 플러그인(선택 사항)

slash 명령과 향상된 UX를 사용하려면 [Beads 플러그인](/integrations/claude-code-plugin)을 설치하세요.

```bash
# Claude Code에서 실행
/plugin marketplace add gastownhall/beads
/plugin install beads
# Claude Code 다시 시작
```

다음 slash 명령을 추가합니다.

- `/beads:ready` - 준비된 작업 표시
- `/beads:create` - 이슈 생성
- `/beads:show` - 이슈 표시
- `/beads:update` - 이슈 업데이트
- `/beads:close` - 이슈 종료

## 문제 해결

### 컨텍스트가 주입되지 않음

```bash
# hook 설정 확인
bd setup claude --check

# 수동으로 prime
bd prime
```

### 변경 사항이 동기화되지 않음

```bash
# 강제 push
bd dolt push

# 시스템 상태 확인
bd doctor
```

### 데이터베이스를 찾을 수 없음

```bash
# Beads 초기화
bd init --quiet
```

## 관련 문서

- [Beads Claude Code 플러그인](/integrations/claude-code-plugin) - slash 명령이 포함된 package형 플러그인
- [MCP 서버](/integrations/mcp-server) - MCP 전용 환경
- [IDE 설정](/getting-started/ide-setup) - 기타 편집기
