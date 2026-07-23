---
title: Aider
description: AI가 bd 명령을 제안하고 사용자가 /run으로 승인하는 Aider의 human-in-the-loop 워크플로로 Beads 설정
---

Aider에서 Beads를 사용하는 방법입니다.

[Aider](https://aider.chat/)는 human-in-the-loop AI pair programming 도구입니다.
[Claude Code](/integrations/claude-code) 같은 자율 에이전트와 달리 shell 명령을 직접
실행하지 않습니다. Beads 통합은 이 설계에 맞춰 AI가 `bd` 명령을 **제안**하고
사용자가 Aider의 `/run` 명령으로 각 항목을 확인합니다.

## 설정

### 사전 요구 사항

- 프로젝트에 Beads 설치 및 초기화([설치](/getting-started/installation) 참고). 아직 `.beads/` 디렉터리가 없다면 `bd init` 실행
- Aider 설치: `pip install aider-chat` 또는 `pipx install aider-chat`

### 빠른 설정

```bash
bd setup aider
```

다음 항목을 생성합니다.

- `.aider.conf.yml` — Aider가 Beads 지침을 로드하도록 지정
- `.aider/BEADS.md` — AI가 읽는 워크플로 지침
- `.aider/README.md` — 사람을 위한 빠른 참조

### 설정 확인

```bash
bd setup aider --check
```

### 통합 제거

```bash
bd setup aider --remove
```

`.aider.conf.yml`, `.aider/BEADS.md`, `.aider/README.md`를 제거합니다.

## 설정 내용

생성된 `.aider.conf.yml`은 Beads 지침을 Aider의 읽기 전용 컨텍스트에 로드합니다.

```yaml
# Aider용 Beads 이슈 추적 통합
# 'bd setup aider'가 자동 생성

# AI용 Beads 워크플로 지침 로드
# 효율을 위해 이 파일은 읽기 전용으로 표시되고 cache됨
read:
  - .aider/BEADS.md
```

`.aider/BEADS.md`에는 AI가 따르는 워크플로 규칙이 있습니다. 모든 작업을 `bd`에서
추적하고(Markdown TODO는 사용하지 않음), 작업을 찾을 때 `bd ready`, 새 이슈에는
`bd create`, 세션 종료 시 `bd dolt push`를 제안합니다. 항상 사용자가 `/run`으로
실행할 명령을 *제안*합니다. 프로젝트별 지침을 추가하도록 편집할 수 있지만 `bd setup
aider`를 다시 실행하면 재생성됩니다.

## 워크플로

### 세션 시작

```bash
# Aider는 .aider.conf.yml을 통해 이슈에 접근
aider

# 또는 컨텍스트를 수동 주입
bd prime | aider --message-file -
```

### Aider 내부

Aider 자체 명령은 `/`로 시작합니다(`/run`, `/add`, `/help`). 나머지는 AI에 보내는
메시지입니다. AI가 `bd` 명령을 제안하면 승인한 항목을 `/run`으로 실행합니다.

```text
사용자: 작업할 준비가 된 이슈는 무엇인가요?

Aider: 사용 가능한 작업을 확인하겠습니다. 다음을 실행하세요.
/run bd ready

사용자: bd-42를 작업합시다.

Aider: claim하려면 다음을 실행하세요.
/run bd update bd-42 --claim
```

세션 도중 AI에 전체 `bd` 컨텍스트를 제공하려면 `/run bd prime`을 실행하세요. AI가
출력을 읽고 전체 워크플로 가이드를 가져옵니다.

### 작업 중

Aider와 함께 `bd` 명령을 사용하세요.

```bash
# 다른 터미널 또는 Aider 종료 후
bd create "작업 중 버그 발견" --deps discovered-from:bd-42 --json
bd update bd-42 --claim
bd ready

# 이미 생성한 이슈를 발견된 작업으로 연결
bd dep add bd-77 bd-42 --type discovered-from
```

### 세션 종료

```bash
bd dolt push
```

## 모범 사례

1. **이슈를 표시된 상태로 유지** - `bd prime`으로 이슈 컨텍스트를 주입합니다.
2. **정기적으로 push** - 중요한 변경 후 `bd dolt push`를 실행합니다.
3. **discovered-from 사용** - 작업 중 발견한 이슈를 추적합니다.
4. **컨텍스트 문서화** - 이슈에 설명을 포함합니다.
5. **Aider는 commit, bd는 동기화** - Aider가 코드 변경 사항을 자동 commit하고 이슈 데이터는 `bd dolt push`로 별도로 이동합니다.

## 워크플로 예제

```bash
# 1. 준비된 작업 확인
bd ready

# 2. 이슈 컨텍스트와 함께 Aider 시작
aider --message "bd-42 작업 중: 인증 버그 수정"

# 3. Aider에서 작업

# 4. 발견한 이슈 생성
bd create "관련 버그 발견" --deps discovered-from:bd-42 --json

# 5. 완료 후 push
bd close bd-42 --reason "수정 완료"
bd dolt push
```

## 문제 해결

### 설정이 로드되지 않음

```bash
# 설정 존재 여부 확인
cat .aider.conf.yml

# 재생성
bd setup aider
```

Aider는 시작할 때 `.aider.conf.yml`을 읽으므로 재생성 후 Aider를 다시 시작하세요.
`/exit` 후 `aider`를 실행합니다.

### 이슈가 표시되지 않음

```bash
# bd prime으로 이슈 컨텍스트 주입
bd prime | aider --message-file -

# 또는 데이터베이스 상태 확인
bd doctor
```

## 관련 문서

- [Claude Code](/integrations/claude-code)
- [IDE 설정](/getting-started/ide-setup)
- [빠른 시작](/getting-started/quickstart)
- [Aider 문서](https://aider.chat/docs/)
- [AGENTS.md](https://github.com/gastownhall/beads/blob/main/AGENTS.md) - 전체 `bd` 에이전트 워크플로 가이드
