---
title: Codex
description: Beads skill, 관리형 AGENTS.md 섹션, 압축 후에도 작동하는 native hook으로 Codex용 Beads 설정
---

`beads` skill, 관리형 `AGENTS.md` 지침, native Codex hook을 통해 Codex에서 Beads를 사용합니다.

```bash
bd setup codex
bd setup codex --check
```

프로젝트 설정은 다음 항목을 작성합니다.

- Beads skill용 `.agents/skills/beads/`
- 관리형 Beads 섹션이 있는 `AGENTS.md`
- `[features].hooks = true`가 있는 `.codex/config.toml`
- Beads hook fallback이 있는 `.codex/hooks.json`

`--skip-agents` 또는 `--stealth`를 사용하지 않으면 `bd init`이 기본적으로 이
프로젝트 설정을 실행합니다. 전역 설정은 `bd setup codex --global`을 사용하며,
`$CODEX_HOME`이 설정되어 있으면 그 아래에, 아니면 `~/.codex`에 작성합니다.

Codex 0.129.0 이상은 `/hooks`, compact 수명 주기 hook, hook이 제공하는 developer
컨텍스트를 지원합니다. Beads는 이 수명 주기를 사용하여 세션 시작 시 `bd prime`을
주입하고 압축 후 컨텍스트를 복구합니다. `/hooks`로 설치된 handler를 검사하거나
활성화 상태를 전환하세요.

## hook 수명 주기

- `SessionStart`(`startup|resume|clear`)는 전체 `bd prime` 출력을 주입합니다.
- `PreCompact`(`manual|auto`)는 `bd prime --memories-only`를 확인하고 Beads 컨텍스트를 사용할 수 없으면 경고합니다.
- `PostCompact`(`manual|auto`)는 세션에 Beads 갱신이 필요함을 기록합니다.
- `UserPromptSubmit`은 압축 후 전체 `bd prime`을 한 번 주입한 뒤 갱신 marker를 지웁니다.

Codex는 compact hook의 일반 stdout을 무시하므로 `PreCompact`만으로는 컨텍스트를
주입하지 않습니다. post-compact marker와 첫 prompt 갱신을 결합한 방식이 신뢰할 수
있는 복구 경로입니다.

갱신 marker는 Codex `session_id`와 워크스페이스 경로를 key로 사용하는 사용자
cache/temp 디렉터리에 저장됩니다. 추적되는 파일이나 Beads 데이터베이스에는 쓰지
않습니다.

Beads Codex 플러그인은 hook을 `plugins/beads/.codex-plugin/hooks/hooks.json`에
저장하고 `plugins/beads/.codex-plugin/plugin.json`에서 `"hooks":
"./.codex-plugin/hooks/hooks.json"`으로 선언합니다. 플러그인이 없으면 `bd setup
codex`가 같은 hook 설정을 `.codex/hooks.json`에 설치하고 `[features].hooks =
true`를 활성화합니다.

## 수동 fallback

`bd setup codex`를 실행하지 않고 `.codex/hooks.json`을 직접 관리한다면 동일한
구조는 다음과 같습니다.

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear",
        "hooks": [{ "type": "command", "command": "bd codex-hook SessionStart", "statusMessage": "Beads 컨텍스트 로드 중" }]
      }
    ],
    "PreCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PreCompact", "statusMessage": "Beads 컨텍스트 확인 중" }]
      }
    ],
    "PostCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PostCompact", "statusMessage": "Beads 컨텍스트 갱신 예약 중" }]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [{ "type": "command", "command": "bd codex-hook UserPromptSubmit", "statusMessage": "Beads 컨텍스트 갱신 중" }]
      }
    ]
  }
}
```

그런 다음 `.codex/config.toml`에서 다음을 활성화했는지 확인하세요.

```toml
[features]
hooks = true
```
