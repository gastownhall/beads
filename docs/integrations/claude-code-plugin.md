---
title: Beads Claude Code 플러그인
description: /beads slash 명령, 내장 skill, 세션 수명 주기 hook을 제공하는 Beads Claude Code 플러그인 설치
---

코딩 워크플로를 위한 AI 감독형 이슈 추적기입니다. slash 명령, 내장 skill, 수명 주기
hook으로 작업을 관리하고 새 작업을 발견하며 컨텍스트를 유지합니다.

## Beads란?

Beads(`bd`)는 AI 감독형 코딩 워크플로를 위해 특별히 설계된 이슈 추적기입니다. AI
에이전트와 개발자에게 다음 기능을 제공합니다.

- 간단한 CLI로 작업 추적
- 개발 중 관련 작업 발견 및 연결
- 코딩 세션 간 컨텍스트 유지
- 분산 워크플로를 위해 Dolt 원격을 통한 이슈 동기화

## 설치

### 사전 요구 사항

1. Beads CLI를 설치합니다.
```bash
curl -sSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```


### 플러그인 설치

Beads 플러그인을 설치하는 방법은 두 가지입니다.

#### 옵션 1: GitHub에서 설치(권장)

```bash
# Claude Code에서 실행
/plugin marketplace add gastownhall/beads
/plugin install beads
```

#### 옵션 2: 로컬 개발

```bash
# 저장소 클론(shell 명령)
git clone https://github.com/gastownhall/beads
cd beads
```

그런 다음 Claude Code에서 다음을 실행하세요.

```
# 로컬 marketplace 추가(Claude Code 명령)
/plugin marketplace add ./beads

# 플러그인 설치
/plugin install beads
```

**참고:** 다른 저장소에서 플러그인을 설치하려면 먼저 터미널에서 해당 저장소
디렉터리로 `cd`한 뒤 Claude Code에서 `./beads` 또는 Beads 디렉터리의 상대 경로를
사용하세요.

### Claude Code 다시 시작

설치 후 Claude Code를 다시 시작하여 플러그인의 명령과 hook을 로드하세요.

## 빠른 시작

```bash
# 프로젝트에서 Beads 초기화
/beads:init

# 첫 이슈 생성
/beads:create "프로젝트 구조 설정" feature 1

# 작업할 준비가 된 항목 확인
/beads:ready

# 전체 워크플로 가이드 표시
/beads:workflow
```

## 사용 가능한 명령

### 버전 관리

- **`/beads:version`** - bd CLI와 플러그인 버전 확인

### 핵심 워크플로 명령

- **`/beads:ready`** - 차단 요소가 없어 작업할 준비가 된 작업 찾기
- **`/beads:create [title] [type] [priority]`** - 대화형으로 새 이슈 생성
- **`/beads:show [issue-id]`** - 이슈 상세 정보 표시
- **`/beads:update [issue-id] [status]`** - 이슈 상태 또는 기타 필드 업데이트
- **`/beads:close [issue-id] [reason]`** - 완료된 이슈 종료

### 프로젝트 관리

- **`/beads:init`** - 현재 프로젝트에서 Beads 초기화
- **`/beads:workflow`** - AI 감독형 이슈 워크플로 가이드 표시
- **`/beads:stats`** - 프로젝트 통계와 진행 상황 표시

### 에이전트

- **`@task-agent`** - 준비된 작업을 찾아 완료하는 자율 에이전트

## MCP 도구

플러그인에는 MCP 서버가 포함되지 않습니다. Claude Code가 직접 구동하는 `bd` CLI를
통해 작동하며 MCP 도구 schema보다 token overhead가 적습니다. MCP 전용 화면 등에서
MCP 도구도 사용하려면 플러그인과 함께 독립 실행형 `beads-mcp` 서버를 설정하세요.
설치 옵션과 전체 도구 catalog는 [MCP 서버](/integrations/mcp-server)를 참고하세요.

## 워크플로

Beads 워크플로는 AI 에이전트를 위해 설계되었지만 사람에게도 잘 맞습니다.

1. **준비된 작업 찾기**: `/beads:ready`
2. **작업 claim**: `/beads:update <id> in_progress`
3. **작업 수행**: 구현, 테스트, 문서화
4. **새 작업 발견**: 작업 중 발견한 bug/TODO의 이슈 생성
5. **완료**: `/beads:close <id> "완료: <summary>"`
6. **반복**: 새로 차단이 해제된 작업 확인

## 이슈 유형

- **`bug`** - 수정해야 하는 문제
- **`feature`** - 새 기능
- **`task`** - 작업 항목(테스트, 문서, refactoring)
- **`epic`** - 여러 이슈로 구성된 큰 기능
- **`chore`** - 유지보수 작업(의존성, tooling)

## 우선순위 수준

- **`0`** - 긴급(보안, 데이터 손실, build 실패)
- **`1`** - 높음(주요 기능, 중요한 버그)
- **`2`** - 중간(있으면 좋은 기능, 사소한 버그)
- **`3`** - 낮음(마무리, 최적화)
- **`4`** - backlog(향후 아이디어)

## 의존성 유형

- **`blocks`** - 강한 의존성(이슈 X가 이슈 Y의 시작을 차단)
- **`related`** - 약한 관계(이슈가 서로 연결됨)
- **`parent-child`** - epic/subtask 관계
- **`discovered-from`** - 작업 중 발견한 이슈 추적

`blocks` 의존성만 준비된 작업 큐에 영향을 줍니다.

## 설정

### 자동 승인 설정

이 설정은 플러그인과 함께 독립 실행형 [beads-mcp
서버](/integrations/mcp-server)를 설정한 경우에 적용됩니다. 기본적으로 Claude Code는
MCP 서버가 명령을 실행하려 할 때마다 확인을 요청합니다. 보안 기능이지만 활발한 개발
중에는 워크플로를 방해할 수 있습니다.

**사용 가능한 옵션:**

#### 1. 모든 Beads 도구 자동 승인(신뢰할 수 있는 프로젝트에 권장)

Claude Code `settings.json`에 추가하세요.

```json
{
  "enabledMcpjsonServers": ["beads"]
}
```

모든 Beads 명령을 prompt 없이 자동 승인합니다.

#### 2. 프로젝트 MCP 서버 자동 승인

Claude Code `settings.json`에 추가하세요.

```json
{
  "enableAllProjectMcpServers": true
}
```

프로젝트의 `.mcp.json` 파일에 정의된 모든 MCP 서버를 자동 승인합니다. MCP 요구 사항이
서로 다른 여러 프로젝트에서 작업할 때 유용합니다.

#### 3. 수동 승인(기본값)

설정할 필요가 없습니다. Claude Code가 MCP 도구를 호출할 때마다 승인을 요청합니다.

**보안 절충점:**

- **수동 승인(기본값)**: 가장 안전하지만 워크플로를 자주 중단합니다.
- **서버 수준 자동 승인**: 신뢰할 수 있는 프로젝트에는 편리하지만 확인 없이 모든 Beads 작업을 허용합니다.
- **프로젝트 수준 자동 승인**: 프로젝트별 신뢰 수준이 다른 다중 프로젝트 워크플로에 균형 잡힌 선택입니다.

**제한:** Claude Code는 현재 도구별 승인 세분화를 지원하지 않습니다. `bd ready`, `bd
show` 같은 읽기 작업만 자동 승인하면서 `bd create`, `bd update` 같은 변경 작업에
확인을 요구할 수 없습니다. 서버 수준에서 전부 허용하거나 전부 확인해야 합니다.

**권장 설정:**

Beads를 자주 사용하는 신뢰할 수 있는 프로젝트에서 활발히 개발하는 경우:

```json
{
  "enabledMcpjsonServers": ["beads"]
}
```

자세한 내용은 [Claude Code 설정 문서](https://docs.claude.com/en/docs/claude-code/settings)를 참고하세요.


## 예제

### 기본 작업 관리

```bash
# 높은 우선순위의 bug 생성
/beads:create "인증 수정" bug 1

# 준비된 작업 확인
/beads:ready

# bd-10 작업 시작
/beads:update bd-10 in_progress

# 작업 완료
/beads:close bd-10 "인증 token 검증 수정"
```

### 개발 중 작업 발견

```bash
# bd-10 작업 중 관련 버그 발견
/beads:create "API에 rate limiting 추가" feature 2

# 현재 작업에 연결
bd dep add bd-11 bd-10 --type discovered-from

# 원래 작업 종료
/beads:close bd-10 "완료, rate limiting용 bd-11 발견"
```

### 작업 에이전트 사용

```bash
# 에이전트가 준비된 작업을 찾아 완료하게 함
@task-agent

# 에이전트의 작업:
# 1. `ready` 도구로 준비된 작업 찾기
# 2. 상태를 업데이트하여 작업 claim
# 3. 작업 실행
# 4. 발견한 내용의 이슈 생성
# 5. 완료 시 종료
# 6. 반복
```

## Dolt를 사용한 자동 동기화

Beads는 모든 쓰기 작업 후 변경 사항을 Dolt 기록에 자동 commit합니다. 이를 통해
원활하게 협업할 수 있습니다.

```bash
# 변경
bd create "기능 추가" -p 1

# 변경 사항은 Dolt 기록에 자동 commit됨
# 준비되면 원격과 동기화
bd dolt push

# 협업자의 변경 사항 pull
bd dolt pull
bd ready  # 최신 데이터로 작업할 준비가 된 이슈 표시
```

## 업데이트

Beads 플러그인에는 업데이트가 필요할 수 있는 구성 요소가 세 개 있습니다.

### 1. 플러그인 업데이트

플러그인 업데이트를 확인하세요.
```bash
/plugin update beads
```

Claude Code가 GitHub에서 최신 버전을 pull합니다. 업데이트 후 **Claude Code를 다시
시작**하여 플러그인 변경 사항을 적용하세요.

### 2. bd CLI 업데이트

플러그인을 사용하려면 `bd` CLI가 설치되어 있어야 합니다. 별도로 업데이트하세요.

```bash
# 빠른 업데이트
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash

# 또는 Go 사용(server 모드 전용)
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# 또는 Go 사용(embedded 지원)
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### 3. 버전 호환성

MCP 서버는 시작 시 bd CLI 버전을 **자동으로 확인**하며 버전이 너무 오래되면 명확한
오류와 함께 실패합니다.

버전 호환성을 수동으로 확인하세요.
```bash
/beads:version
```

다음 항목을 표시합니다.

- bd CLI 버전
- 플러그인 버전
- MCP 서버 상태
- 버전이 일치하지 않을 때 호환성 경고

**권장 업데이트 워크플로:**

1. 버전 확인: `/beads:version`
2. 필요한 경우 bd CLI 업데이트(위 내용 참고)
3. 플러그인 업데이트: `/plugin update beads`
4. Claude Code 다시 시작
5. 확인: `/beads:version`

### 버전 번호

Beads는 semantic versioning을 따릅니다. 플러그인 버전은 bd CLI 버전을 추적합니다.
major 버전 상승에는 breaking change가 포함될 수 있으므로 release note는
CHANGELOG.md를 확인하세요.

## 문제 해결

### 플러그인이 표시되지 않음

1. 설치 확인: `/plugin list`
2. Claude Code 다시 시작
3. `bd`가 PATH에 있는지 확인: `which bd`
4. uv 설치 확인: `which uv`

### MCP 서버가 연결되지 않음

1. MCP 서버 목록 확인: `/mcp`
2. 플러그인 표시가 있는 `beads` 서버 찾기
3. MCP 서버를 다시 로드하도록 Claude Code 다시 시작
4. log에서 오류 확인

### 명령이 작동하지 않음

1. Beads를 초기화한 프로젝트인지 확인: `/beads:init`
2. 데이터베이스 존재 여부 확인: `ls -la .beads/`
3. slash 명령 대신 MCP 도구에 직접 접근
4. Beads CLI 작동 확인: `bd --help`

### MCP 도구 오류

1. `bd` 실행 파일 위치 확인: `BEADS_PATH` 환경 변수
2. 터미널에서 `bd` 작동 확인: `bd stats`
3. Claude Code의 MCP 서버 log 검토
4. 다시 초기화: `/beads:init`

## 더 알아보기

- **GitHub**: https://github.com/gastownhall/beads
- **문서**: 저장소의 README.md 참고
- **예제**: 통합 패턴은 `examples/` 디렉터리 확인
- **MCP 서버**: 서버 상세 정보는 `integrations/beads-mcp/` 참고

## 기여

버그를 발견했거나 기능 아이디어가 있나요? Beads 저장소에 이슈를 생성하세요.

## 라이선스

MIT License입니다. 저장소의 LICENSE 파일을 참고하세요.
