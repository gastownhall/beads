---
title: Junie
description: 지침 파일과 MCP 서버 설정으로 JetBrains AI 에이전트 Junie용 Beads 설정
---

Junie(JetBrains AI 에이전트)에서 Beads를 사용하는 방법입니다.

## 설정

### 빠른 설정

```bash
bd setup junie
```

다음 항목을 생성합니다.

- **`.junie/guidelines.md`** - Beads 워크플로용 에이전트 지침
- **`.junie/mcp/mcp.json`** - MCP 서버 설정

### 설정 확인

```bash
bd setup junie --check
```

## 작동 방식

1. **세션 시작**: Junie가 워크플로 컨텍스트를 위해 `.junie/guidelines.md`를 읽습니다.
2. **MCP 도구 사용 가능**: Junie가 Beads MCP 도구를 직접 사용할 수 있습니다.
3. **작업**: `bd` CLI 명령 또는 MCP 도구를 사용합니다.
4. **세션 종료**: `bd dolt push`를 실행하여 변경 사항을 Dolt 원격에 push합니다.

## 설정 파일

### 지침(`.junie/guidelines.md`)

Junie가 자동으로 읽는 워크플로 지침을 포함합니다.

- 핵심 워크플로 규칙
- 명령 참조
- 이슈 유형과 우선순위
- MCP 도구 문서

### MCP 설정(`.junie/mcp/mcp.json`)

<Warning>
`bd setup junie`는 현재 `bd` build에 존재하지 않는 `bd mcp`를 호출하는 MCP 설정을
작성하므로 해당 설정으로 서버를 시작할 수 없습니다. 레시피가 수정될 때까지 Junie가
대신 독립 실행형 `beads-mcp` 서버를 가리키게 하세요.
</Warning>

```json
{
  "mcpServers": {
    "beads": {
      "command": "uvx",
      "args": ["beads-mcp"]
    }
  }
}
```

서버의 도구 catalog와 다른 설치 옵션(pip/pipx)은 [MCP
서버](/integrations/mcp-server)를 참고하세요.

## CLI 명령

`bd` CLI를 직접 사용할 수도 있습니다.

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

## 문제 해결

### 지침이 로드되지 않음

```bash
# 설정 확인
bd setup junie --check

# 필요한 경우 다시 설치
bd setup junie
```

### MCP 도구를 사용할 수 없음

```bash
# MCP 설정이 있고 beads-mcp 서버를 가리키는지 확인
cat .junie/mcp/mcp.json

# 서버 package 설치 확인
pip show beads-mcp
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

## 통합 제거

```bash
bd setup junie --remove
```

다음 항목을 제거합니다.
- `.junie/guidelines.md`
- `.junie/mcp/mcp.json`
- 비어 있는 `.junie/mcp/` 및 `.junie/` 디렉터리

## 관련 문서

- [MCP 서버](/integrations/mcp-server) - MCP 서버 상세 정보
- [Claude Code](/integrations/claude-code) - 유사한 hook 기반 통합
- [IDE 설정](/getting-started/ide-setup) - 기타 편집기
