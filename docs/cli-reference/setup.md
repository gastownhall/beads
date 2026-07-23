---
title: "bd setup"
description: "AI 편집기와 코딩 도우미용 통합 파일을 설정합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc setup`에서 생성되었습니다.

AI 편집기와 코딩 도우미용 통합 파일을 설정합니다.

레시피는 beads 워크플로 지침이 작성될 위치를 정의합니다. 기본 제공 레시피에는
cursor, claude, copilot, gemini, aider, factory, codex, mux, opencode, junie, windsurf, cody, kilocode가 있습니다.

예시:
  bd setup cursor          # Cursor IDE 통합 설치
  bd setup codex           # Codex skill + AGENTS.md 지침 + 네이티브 훅 설치
  bd setup codex --global  # 전역 Codex skill + 지침 + 네이티브 훅 설치
  bd setup copilot         # Copilot CLI 플러그인 + 저장소 지침 설치
  bd setup mux --project   # Mux 워크스페이스 계층(.mux/AGENTS.md) 설치
  bd setup mux --global    # Mux 전역 계층(~/.mux/AGENTS.md) 설치
  bd setup mux --project --global  # 두 Mux 계층 모두 설치
  bd setup --list          # 사용 가능한 모든 레시피 표시
  bd setup --print         # 템플릿을 stdout에 출력
  bd setup -o rules.md     # 사용자 정의 경로에 템플릿 쓰기
  bd setup --add myeditor .myeditor/rules.md  # 사용자 정의 레시피 추가

설치 상태를 확인하려면 'bd setup &lt;recipe&gt; --check'를 사용하세요.
제거하려면 'bd setup &lt;recipe&gt; --remove'를 사용하세요.

```
bd setup [recipe] [flags]
```

**플래그:**

```
      --add string      지정한 이름으로 사용자 정의 레시피 추가
      --check           통합 설치 여부 확인
      --global          전역 설치(claude/codex/mux, ~/.claude/settings.json, $CODEX_HOME/AGENTS.md 또는 ~/.codex/AGENTS.md, ~/.mux/AGENTS.md에 쓰기)
      --list            사용 가능한 모든 레시피 나열
  -o, --output string   사용자 정의 경로에 템플릿 쓰기
      --print           템플릿을 stdout에 출력
      --project         이 프로젝트에만 설치(gemini/mux)
      --remove          통합 제거
      --stealth         스텔스 모드 사용(claude/gemini)
```
