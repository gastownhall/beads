---
title: GitHub Copilot CLI 통합 설계
description: 플러그인 매니페스트와 저장소 지침을 사용하는 Copilot CLI 통합의 설계 근거 및 설정
---

이 문서는 Beads의 GitHub Copilot CLI 통합 설계 결정을 설명합니다.

**VS Code 및 MCP**는 [GitHub Copilot](/integrations/github-copilot)을 참고하세요.

## 통합 방식

**권장: Copilot CLI 플러그인 및 저장소 지침** - Beads는 Copilot CLI의 네이티브 플러그인
매니페스트와 저장소 지침을 함께 사용합니다.

- `.copilot-plugin/plugin.json`이 `bd prime` 훅을 네이티브로 등록합니다.
- `.github/copilot-instructions.md`가 저장소별 워크플로 지침을 제공합니다.
- `--json` 플래그가 있는 직접 CLI 명령이 주요 운영 인터페이스로 유지됩니다.

**대안: VS Code MCP** - 편집기의 Copilot Chat에 사용합니다.

- MCP를 통한 네이티브 도구 호출
- 도구 스키마로 인한 더 큰 컨텍스트 오버헤드
- 터미널 우선 워크플로 대신 편집기 네이티브 도구 접근을 원할 때 사용

## 커스텀 설정 코드 대신 플러그인 및 지침을 사용하는 이유

**플러그인 매니페스트가 이미 원하는 동작을 모델링합니다.**

1. **훅은 도구의 네이티브 형식에 포함**
   - Copilot CLI가 플러그인 매니페스트를 직접 이해합니다.
   - `SessionStart`와 `PreCompact`를 커스텀 Go 논리 대신 데이터로 선언할 수 있습니다.
   - Beads 코어를 더 작고 유지보수하기 쉽게 유지합니다.

2. **지침을 명시적이고 검토 가능한 상태로 유지**
   - 저장소 지침은 계속 `.github/copilot-instructions.md`에 있습니다.
   - 팀은 다른 프로젝트 문서처럼 지침을 검토할 수 있습니다.
   - 훅 동작과 사람이 읽는 지침이 분리됩니다.

3. **낮은 유지보수 부담**
   - 코어에 Copilot 전용 설치/검사/제거 구현이 없습니다.
   - Copilot 전용 doctor 점검도 없습니다.
   - 레시피는 네이티브 플러그인 파일과 지침 파일만 작성합니다.

## 터미널 작업에서 MCP보다 Copilot CLI를 사용하는 이유

큰 컨텍스트 창에서도 **컨텍스트 효율은 중요합니다.**

1. **토큰에 따라 계산 비용 증가** - 추론할 때마다 컨텍스트의 모든 토큰을 처리합니다.
2. **컨텍스트에 따라 지연 시간 증가** - 작은 프롬프트가 CLI 응답성을 높입니다.
3. **에너지 소비** - 간결한 프롬프트가 긴 세션에서 더 지속 가능합니다.
4. **주의 품질** - 일반적으로 모델은 더 작고 관련성 높은 컨텍스트에서 잘 작동합니다.

**수치 비교:**

- MCP 도구 스키마는 컨텍스트에 1만~5만 토큰을 추가할 수 있습니다.
- `bd prime`은 약 1~2천 토큰의 워크플로 컨텍스트를 추가합니다.
- 오버헤드가 약 10배 줄어듭니다.

## 설치

```bash
# Copilot CLI 플러그인 매니페스트 및 저장소 지침 설치
bd setup copilot

# 설치 상태 확인
bd setup copilot --check

# 통합 제거
bd setup copilot --remove
```

**설치 항목:**
- `.copilot-plugin/plugin.json`
  - `SessionStart` hook: Copilot CLI가 세션을 시작할 때 `bd prime` 실행
  - `PreCompact` hook: 컨텍스트 압축 전에 `bd prime` 실행
- `.github/copilot-instructions.md`
  - Copilot CLI용 저장소 워크플로 지침

## 관련 파일

- `plugins/beads/.copilot-plugin/plugin.json` - 공유 플러그인 패키지의 소스 플러그인 매니페스트
- `plugins/beads/copilot_manifest.go` - `bd setup copilot`이 사용하는 내장 매니페스트 소스
- `internal/recipes/recipes.go` - 가벼운 `copilot` 레시피 정의
- `internal/recipes/template.go` - `bd setup`이 사용하는 정적 Copilot 지침 템플릿
- [GitHub Copilot 통합](/integrations/github-copilot) - VS Code MCP 통합

## 참고 자료

- [GitHub Copilot CLI 문서](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli)
- [GitHub Copilot CLI에 저장소 커스텀 지침 추가](https://docs.github.com/en/copilot/how-tos/copilot-cli/add-custom-instructions)
