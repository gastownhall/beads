---
title: IDE 설정
description: Claude Code, Cursor, Gemini, Copilot 및 기타 코딩 에이전트용 bd setup 레시피, hook, 지침 파일 설정
---

Beads를 최적으로 통합하도록 IDE 또는 코딩 에이전트를 설정하세요.

Last reviewed: 2026-07-10

Freshness source: `cmd/bd/setup*.go` 및 `internal/recipes/`.

## `bd setup` 작동 방식

`bd setup` 명령은 **레시피 기반 아키텍처**를 사용합니다. 레시피는 Beads 워크플로
지침을 작성할 위치를 정의합니다. 내장 레시피는 널리 쓰이는 도구를 지원하며, 다른
도구에는 커스텀 레시피를 추가할 수 있습니다(아래 커스텀 레시피 참고). 각 통합은
서로 보완하므로 여러 개를 한 번에 설치할 수 있습니다.

```bash
bd setup --list             # 사용 가능한 모든 레시피 표시
bd setup claude             # 통합 설치(claude, cursor, gemini 등)
bd setup claude --check     # 설치 확인
bd setup claude --remove    # 제거
```

| 레시피 | 작성되는 파일 | 상세 정보 |
|--------|---------------|---------|
| `claude` | `.claude/settings.json`(`--global` 사용 시 `~/.claude/settings.json`) 및 `CLAUDE.md` 섹션 | [Claude Code](/integrations/claude-code) |
| `cursor` | `.cursor/rules/beads.mdc` | [Cursor](/integrations/cursor) |
| `gemini` | `~/.gemini/settings.json`(`--project` 사용 시 `.gemini/settings.json`) 및 `GEMINI.md` 섹션 | [Gemini CLI](/integrations/gemini) |
| `copilot` | `.copilot-plugin/plugin.json` 및 `.github/copilot-instructions.md` | [Copilot CLI](/integrations/copilot-cli) |
| `codex` | `.agents/skills/beads/`, `AGENTS.md` 섹션, `.codex/` hook | [Codex](/integrations/codex) |
| `factory` | `AGENTS.md` 섹션 | [Factory.ai Droid](/integrations/factory) |
| `mux` | `AGENTS.md` 섹션(`--project`/`--global` 사용 시 `.mux/` 계층 추가) | [Mux](/integrations/mux) |
| `opencode` | `AGENTS.md` 섹션 | [OpenCode](/integrations/opencode) |
| `aider` | `.aider.conf.yml` + `.aider/BEADS.md` + `.aider/README.md` | [Aider](/integrations/aider) |
| `junie` | `.junie/guidelines.md` + `.junie/mcp/mcp.json` | [Junie](/integrations/junie) |
| `windsurf` | `.windsurf/rules/beads.md` | [Windsurf](/integrations/windsurf) |
| `cody` | `.cody/rules/beads.md` | [Cody](/integrations/cody) |
| `kilocode` | `.kilocode/rules/beads.md` | [Kilo Code](/integrations/kilocode) |

`bd prime`은 운영 워크플로 명령의 단일 원본입니다. 각 통합의 지침 파일은
`bd prime`을 가리키거나(hook 지원 에이전트), 전체 명령 참조를 담습니다(AGENTS 우선
에이전트).

<Note>
모든 팀원과 AI 도구가 같은 지침을 받도록 해당 지침 파일(`AGENTS.md`, `CLAUDE.md`,
`GEMINI.md`)을 Git에 commit하세요.
</Note>

### 템플릿 프로필

각 통합은 도구의 지침 파일(`AGENTS.md`, `CLAUDE.md`, `GEMINI.md` 또는
`.github/copilot-instructions.md`)에 들어갈 내용의 양을 제어하는 두 **프로필** 중
하나를 작성합니다.

| 프로필 | 사용 도구 | 내용 |
|---------|---------|---------|
| `full` | Factory, Mux, OpenCode | 전체 명령 참조, 이슈 유형, 우선순위, 워크플로 |
| `minimal` | Claude Code, GitHub Copilot CLI, Gemini CLI | `bd prime` 포인터와 빠른 참조만 포함(약 60% 작음) |

hook 지원 에이전트는 세션 시작 시 `bd prime`이 전체 컨텍스트를 주입하므로 `minimal`
프로필을 사용합니다. AGENTS 우선 에이전트는 지침 파일이 주요 통합 위치이므로 `full`
프로필을 사용합니다. Codex는 대신 skill 기반입니다. `.agents/skills/beads/SKILL.md`를
사용하고, 관리형 `AGENTS.md` 지침이 skill 사용 시점을 Codex에 알려 줍니다.

**프로필 우선순위:** 파일에 이미 `full` 프로필 섹션이 있고 `minimal` 프로필 도구가
symlink 등을 통해 같은 파일에 설치되면, 정보 손실을 막기 위해 `full` 프로필을
보존합니다.

### 정책 프로필

템플릿 프로필은 설치되는 텍스트의 양을 제어합니다. 정책 프로필은 handoff 때
에이전트에 허용되는 작업을 제어합니다.

| 정책 | 기본 범위 | commit/push 지침 |
|--------|---------------|----------------------|
| `conservative` | 독립 프로젝트, 알 수 없는 프로젝트, 일회성 지원 | 작업 추적에는 `bd`를 사용한 뒤 변경 파일, 검증, 제안 명령을 보고합니다. 사용자나 orchestrator의 명시적 승인 없이 commit, push, Dolt 원격 동기화를 실행하지 않습니다. |
| `minimal` | `bd prime`이 상세 워크플로를 제공하는 hook 우선 통합 | Git 권한은 conservative와 같고, 설치 파일은 짧게 유지하며 `bd prime`을 가리킵니다. |
| `team-maintainer` | 세션 종료를 에이전트에 명시적으로 위임한 저장소 | 에이전트는 일상 작업의 일부로 Beads 종료, 품질 검사, commit, `bd dolt push`, `git push`를 수행할 수 있습니다. 현재의 "do not commit" 또는 "do not push" 지침은 여전히 프로필보다 우선합니다. |

생성된 Beads 섹션과 `bd prime`의 기본 Git 권한은 conservative입니다. `agent.profile`
설정 key 또는 `BD_AGENT_PROFILE` 환경 변수로 프로필을 명시적으로 설정하세요. 값은
`conservative`, `minimal`, `team-maintainer`이며, 환경 변수가 우선하고 알 수 없는
값은 `conservative`로 대체됩니다.

```bash
bd config set agent.profile team-maintainer
# 또는 단일 세션/프로세스의 경우
BD_AGENT_PROFILE=team-maintainer bd prime
```

`bd prime`은 명시적 설정을 브랜치별 Git 권한 검사(stealth 모드, Git 원격 없음, 임시
브랜치, `no-push`) 위에 적용합니다. 이러한 강제 제약이 여전히 우선하며,
`team-maintainer`도 명시적인 "do not commit" 또는 "do not push" 지침보다
하위입니다. Beads는 원격이 있다는 이유만으로 team-maintainer 권한을 추론하지
않습니다. 이 설정으로 지정하거나, 설정에 접근할 수 없는 도구에서는 최상위 프로젝트
지침으로 지정해야 합니다.

### 관리형 섹션

`bd setup factory`, `bd setup mux`, `bd setup opencode`는 `BEGIN/END BEADS
INTEGRATION` HTML 주석 marker로 감싼 Beads 섹션을 `AGENTS.md`에 추가합니다. 시작
marker에는 버전, 프로필, 해시 metadata가 들어 있으므로(예:
`<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:19cc25d9 -->`) `--check`가
`missing`, `stale`, `current` 상태를 보고할 수 있습니다. metadata가 없는 legacy
marker는 다음 설치 또는 업데이트 때 자동 업그레이드됩니다. 설정을 다시 실행하면 기존
섹션을 제자리에서 멱등적으로 업데이트하며, `--remove`는 관리형 섹션만 삭제하고
`AGENTS.md`의 나머지 내용은 그대로 둡니다.

`bd setup codex`는 자체 marker 쌍(`BEGIN/END BEADS CODEX SETUP`)을 사용합니다.
같은 `AGENTS.md`에 `bd setup factory` 또는 `bd setup mux`와 함께 실행하면 관리형
섹션 두 개가 나란히 남습니다. 각 레시피의 `--check`는 자체 섹션만 검사하고,
`--remove`도 자체 섹션만 제거합니다.

하나의 `AGENTS.md`는 Factory Droid, Mux, OpenCode, Cursor, Zed, Jules 등
AGENTS.md를 인식하는 여러 도구에서 작동합니다. 팀에서 여러 AI 도구를 섞어 쓴다면
`bd setup factory`가 좋은 출발점입니다.

## Claude Code

Claude Code에 권장하는 방법은 다음과 같습니다.

```bash
bd setup claude            # 프로젝트 설치: .claude/settings.json
bd setup claude --global   # 전역 설치: ~/.claude/settings.json
```

다음 항목을 설치합니다.

- **SessionStart hook** - 워크플로 컨텍스트를 Claude Code가 기대하는 JSON envelope로 감싸는 `bd prime --hook-json`을 실행합니다. SessionStart는 세션 시작, 재개, 초기화 및 컨텍스트 압축 후에 실행되므로 별도의 압축 hook이 필요하지 않습니다.
- **`CLAUDE.md`의 minimal Beads 섹션** - `bd prime`을 가리키며, 안전한 업데이트와 `--check` 최신 상태 감지를 위해 hash/version marker로 관리됩니다.

[Beads Claude Code 플러그인](/integrations/claude-code-plugin)이 설치되어 있으면
플러그인이 hook을 관리하므로 `bd setup claude`는 hook 작성을 건너뜁니다. 따라서
세션마다 `bd prime`이 두 번 실행되지 않습니다.

**작동 방식:**

1. SessionStart hook이 `bd prime --hook-json`을 자동 실행합니다.
2. `bd prime`이 약 1~2천 token의 워크플로 컨텍스트를 주입합니다.
3. `bd` CLI 명령을 직접 사용합니다.
4. Git hook은 export와 legacy fallback을 갱신하고, Dolt 원격은 동기화를 처리합니다.

**플래그:**

| 플래그 | 설명 |
|------|-------------|
| `--check` | hook과 관리형 `CLAUDE.md` Beads 섹션을 모두 확인 |
| `--remove` | Beads hook과 관리형 `CLAUDE.md` Beads 섹션 제거 |
| `--global` | 프로젝트 대신 `~/.claude/settings.json`에 설치 |
| `--stealth` | `bd prime --stealth --hook-json` 사용(flush만 수행, Git 작업 없음). Git 작업이 실패할 수 있는 CI/CD에 유용 |

hook이 적용되도록 설치 후 Claude Code를 다시 시작하세요.

**설치 확인:**
```bash
bd setup claude --check
```

### 수동 설정

수동 설정을 원한다면 Claude Code 설정에 다음 hook을 추가하세요.

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

## Cursor IDE

```bash
bd setup cursor            # 항상 적용되는 규칙 파일
```

이 명령은 Cursor가 매 턴 다시 포함하는 Beads 인식 규칙을 담은
`.cursor/rules/beads.mdc`를 생성합니다.

**확인:**
```bash
bd setup cursor --check
```

자세한 내용은 [Cursor](/integrations/cursor)를 참고하세요.

## Gemini CLI

```bash
bd setup gemini            # ~/.gemini/settings.json의 전역 hook
bd setup gemini --project  # .gemini/settings.json의 프로젝트 hook
```

`bd prime --hook-json`을 실행하는 SessionStart hook과 `GEMINI.md`의 minimal Beads
섹션을 설치합니다. Gemini는 hook stdout이 유효한 JSON이어야 하며, `--hook-json`은
Markdown을 필요한 envelope로 감쌉니다. `--stealth`는 Claude Code와 같은 방식으로
작동하고, `--check`와 `--remove`는 hook과 관리형 `GEMINI.md` 섹션을 모두
처리합니다.

**확인:**
```bash
bd setup gemini --check
```

자세한 내용은 [Gemini CLI](/integrations/gemini)를 참고하세요.

## Aider

```bash
# Aider 통합 설정
bd setup aider
```

다음 세 파일을 작성합니다.

| 파일 | 용도 |
|------|---------|
| `.aider.conf.yml` | Aider가 지침 파일을 읽도록 지정 |
| `.aider/BEADS.md` | AI용 워크플로 지침 |
| `.aider/README.md` | 사람용 빠른 참조 |

Aider는 human-in-the-loop 방식입니다. AI가 `bd` 명령을 **제안**하고 사용자가
`/run`으로 실행합니다. 워크플로는 [Aider](/integrations/aider)를 참고하세요.

**확인:**
```bash
bd setup aider --check
```

## AGENTS.md 도구: Factory, Mux, OpenCode, Codex

```bash
bd setup factory    # Factory.ai Droid — AGENTS.md 섹션
bd setup mux        # Mux — AGENTS.md 섹션(+ --project/--global 계층)
bd setup opencode   # OpenCode — AGENTS.md 섹션
bd setup codex      # Codex — Beads skill + AGENTS.md 지침 + native hook
```

이 명령은 `AGENTS.md`의 관리형 섹션을 생성하거나 업데이트합니다(위의 관리형 섹션
참고). `--skip-agents` 또는 `--stealth`를 사용하지 않으면 `bd init`이 프로젝트
Codex 설정을 자동 실행합니다. 워크트리, 공유 또는 `BEADS_DIR` 설정에서는 `bd
where`로 해석된 워크스페이스를 확인하세요. 이 통합에는 로컬 `./.beads`가 필요하지
않습니다. 도구가 이미 실행 중이면 설정 후 다시 시작하세요.

자세한 내용: [Factory.ai Droid](/integrations/factory), [Mux](/integrations/mux),
[OpenCode](/integrations/opencode), [Codex](/integrations/codex).

## GitHub Copilot

**Copilot CLI:**

```bash
bd setup copilot
```

native Copilot CLI 플러그인 manifest(`bd prime` hook을 등록하는
`.copilot-plugin/plugin.json`)와 저장소 지침(`.github/copilot-instructions.md`)을
설치합니다. [Copilot CLI](/integrations/copilot-cli)를 참고하세요.

**GitHub Copilot을 사용하는 VS Code**에서는 MCP 서버를 사용하세요.

```bash
# MCP 서버 설치
uv tool install beads-mcp
```

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

Beads를 초기화하고 VS Code를 다시 로드하세요.

```bash
bd init --quiet
```

자세한 설정은 [GitHub Copilot 통합](/integrations/github-copilot)을 참고하세요.

## `bd prime`을 사용한 컨텍스트 주입

모든 통합은 `bd prime`으로 컨텍스트를 주입합니다.

```bash
bd prime
```

다음 항목을 포함한 간결한 워크플로 참조(약 1~2천 token)를 출력합니다.

- 사용 가능한 명령
- 현재 프로젝트 상태
- 워크플로 패턴
- 모범 사례
- `bd remember`의 영구 메모리

`bd prime`은 상단 부근에 메모리를 출력하고 잘림 경고로 시작합니다. host가 전체 hook
출력을 파일에 저장하고 미리 보기만 보여 준다면 계속하기 전에 에이전트가 전체 파일을
읽게 하세요.

hook 컨텍스트에서 `bd prime --hook-json`은 출력을 SessionStart JSON
envelope(Claude Code, Gemini CLI, Codex)로 감쌉니다. 메모리 전용 hook에는 다음을
사용하세요.

```bash
bd prime --memories-only
```

**컨텍스트 효율이 중요한 이유:**

- token 수에 따라 계산 비용이 증가합니다.
- 컨텍스트 크기에 따라 지연 시간이 늘어납니다.
- 모델은 작고 집중된 컨텍스트에 더 잘 주의를 기울입니다.

## MCP 서버(대안)

MCP 전용 환경(Claude Desktop, shell 접근 불가)에서는 다음과 같이 설치하세요.

```bash
# MCP 서버 설치
pip install beads-mcp
```

Claude Desktop 설정에 추가하세요.
```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**절충점:**

- MCP 전용 환경에서 작동합니다.
- 컨텍스트 overhead가 큽니다(도구 schema에 1만~5만 token).
- MCP protocol로 인한 지연 시간이 추가됩니다.

자세한 설정은 [MCP 서버](/integrations/mcp-server)를 참고하세요.

## 커스텀 레시피

내장 레시피가 없는 편집기나 도구에는 다음을 사용하세요.

```bash
bd setup --add myeditor .myeditor/rules.md   # 커스텀 레시피 저장
bd setup myeditor                            # 설치
bd setup myeditor --check                    # 확인
bd setup myeditor --remove                   # 제거
```

커스텀 레시피는 `.beads/recipes.toml`에 저장됩니다. 추가하려면 활성 Beads
워크스페이스가 필요합니다.

```toml
[recipes.myeditor]
name = "myeditor"
path = ".myeditor/rules.md"
type = "file"
```

레시피를 저장하지 않고 한 번만 설치하려면 템플릿을 원하는 경로에 작성하거나 먼저
검사하세요.

```bash
bd setup -o .my-custom-location/beads.md
bd setup --print
```

**레시피 유형:**

| 유형 | 설명 | 사용 도구 |
|------|-------------|---------|
| `file` | 단일 파일에 템플릿 작성 | windsurf, cody, kilocode |
| `hooks` | JSON 설정을 수정하여 hook 추가 | claude, gemini |
| `section` | 기존 파일에 marker가 있는 섹션 주입 | factory, codex, mux, opencode |
| `multifile` | 여러 파일 작성 | aider, copilot, junie |

`--add`로 추가한 커스텀 레시피의 유형은 항상 `file`입니다.

## Git 훅

export 갱신과 legacy fallback 동작을 위해 Git hook이 설치되었는지 확인하세요.

```bash
bd hooks install
```

다음 hook을 설치합니다.

- **pre-commit** - commit 전에 연결된 hook 실행
- **post-merge** - pull/merge 후 연결된 hook 실행
- **pre-push** - push 전에 연결된 hook 실행
- **post-checkout** - 브랜치 checkout 후 연결된 hook 실행
- **prepare-commit-msg** - 포렌식용 에이전트 identity trailer 추가

**hook 상태 확인:**
```bash
bd hooks list   # 설치됨, 오래됨, 누락 상태 표시
bd info         # hook이 오래된 경우 경고 표시
```

## 설정 확인

전체 상태 검사를 실행하세요.

```bash
# 버전 확인
bd version

# 프로젝트 상태 확인(통합 상태 포함)
bd doctor

# Git hook 확인
bd hooks list

# 편집기 통합 확인
bd setup claude --check   # 또는 cursor, gemini, aider 등
```

**문제 해결:**

- *hook이 작동하지 않나요?* 설치 후 AI 도구를 다시 시작한 다음 `bd setup claude --check`(또는 해당 도구의 레시피)를 다시 실행하고 `bd doctor` 출력에서 통합 상태를 확인하세요.
- *컨텍스트가 표시되지 않나요?* `bd prime`이 단독으로 작동하는지 확인하세요. 실패하면 먼저 근본적인 Beads 문제를 해결하세요.
