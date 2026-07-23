---
title: "bd prime"
description: "필수 Beads 워크플로 컨텍스트를 AI에 최적화된 Markdown 형식으로 출력합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc prime`에서 생성되었습니다.

필수 Beads 워크플로 컨텍스트를 AI에 최적화된 Markdown 형식으로 출력합니다.

MCP 서버 활성화 여부를 자동 감지하고 출력을 조정합니다:
- MCP 모드: 간단한 워크플로 알림(약 50토큰)
- CLI 모드: 전체 명령 참조(약 1~2천 토큰)

컨텍스트 압축 후 에이전트가 bd 워크플로를 잊지 않도록 Claude Code, Gemini CLI,
Codex SessionStart 훅용으로 설계되었습니다.

구성 옵션:
- no-git-ops: true이면 스텔스 모드 출력(세션 종료 프로토콜에 git 명령 없음).
  설정 방법: bd config set no-git-ops true
  커밋 시점을 수동으로 제어할 때 유용합니다.

	워크플로 사용자 정의:
	- 로컬 클론 또는 해석된 워크스페이스에 .beads/PRIME.md 파일을 두어 기본 출력을 완전히 재정의합니다.
	- 사용자 정의할 기본 콘텐츠를 덤프하려면 --export를 사용합니다.
	- 영구 메모리만 주입해야 하는 훅 컨텍스트에는 --memories-only를 사용합니다.

```
bd prime [flags]
```

**플래그:**

```
      --export          기본 콘텐츠 출력(PRIME.md 재정의 무시)
      --full            전체 CLI 출력 강제(MCP 감지 무시)
      --hook-json       출력을 SessionStart 훅 JSON 봉투로 감싸기(Claude Code, Gemini CLI, Codex)
      --mcp             MCP 모드 강제(최소 출력)
      --memories-only   간결한 훅 컨텍스트에 영구 메모리만 출력
      --stealth         스텔스 모드(git 작업 없이 flush만 수행)
```
