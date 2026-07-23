---
title: 소개
description: 컨텍스트 손실에도 작업을 이어가는 AI 코딩 에이전트를 위해 만든, 의존성을 인식하는 Dolt 기반 이슈 추적기
---

**Beads**(`bd`)는 AI가 감독하는 코딩 워크플로를 위해 설계된 Dolt 기반 이슈 추적기입니다.

이 문서는 Beads 1.1.0 릴리스를 대상으로 합니다. [v1.1.0 릴리스 노트](https://github.com/gastownhall/beads/releases/tag/v1.1.0)를 참고하세요.

## Beads를 사용하는 이유

기존 이슈 추적기(Jira, GitHub Issues)는 AI 에이전트를 위해 설계되지 않았습니다. Beads는 처음부터 다음 목적에 맞춰 만들었습니다.

- **AI 네이티브 워크플로** - 해시 기반 ID가 여러 에이전트의 동시 작업에서 충돌을 방지합니다.
- **Dolt 기반 저장소** - 이슈를 버전 관리되는 SQL 데이터베이스에 저장하여 Dolt 네이티브 복제로 협업할 수 있습니다.
- **의존성을 인식하는 실행** - `bd ready`는 차단되지 않은 작업만 보여 줍니다.
- **Formula 시스템** - 반복 가능한 워크플로를 선언형 템플릿으로 정의합니다.
- **다중 에이전트 조정** - 복잡한 워크플로에 라우팅, Gate, Molecule을 제공합니다.

## 빠른 시작

```bash
# Homebrew로 설치(macOS/Linux)
brew install beads

# 또는 빠른 설치(macOS/Linux/FreeBSD)
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash

# 프로젝트에서 초기화
cd your-project
bd init --quiet

# 첫 이슈 생성
bd create "데이터베이스 설정" -p 1 -t task

# 준비된 작업 확인
bd ready
```

## 핵심 개념

전체 모델은 [Beads 작동 방식](/core-concepts/index) 한 페이지에서 확인할 수 있습니다.

| 개념 | 설명 |
|---------|-------------|
| [**Beads(이슈)**](/core-concepts/issues) | 우선순위, 유형, 레이블, 의존성이 있는 작업 항목 |
| [**의존성**](/core-concepts/dependencies) | `blocks`, `parent-child`, `discovered-from`, `related` |
| [**동기화**](/core-concepts/sync-concepts) | 실행할 서버 없이 Git 원격을 통한 Dolt push/pull |
| [**Formula**](/workflows/formulas) | 선언형 워크플로 템플릿(TOML 또는 JSON) |
| [**Molecule**](/workflows/molecules) | Formula에서 인스턴스화한 작업 그래프 |
| [**Gate**](/workflows/gates) | 비동기 조정 기본 요소(사람, 타이머, GitHub) |

## AI 에이전트용 기능

Beads는 AI 코딩 에이전트에 최적화되어 있습니다.

```bash
# 프로그래밍 방식으로 접근할 때는 항상 --json 사용
bd list --json
bd show bd-42 --json

# 구현 중 발견한 작업 추적
bd create "인증 버그 발견" --description="상세 내용..." \
  --deps discovered-from:bd-100 --json

# 세션 종료 시 변경 사항 push
bd dolt push
```

자세한 에이전트 지침은 [Claude Code 통합](/integrations/claude-code)을 참고하세요.

## 아키텍처

```
Dolt DB(embedded 모드에서는 .beads/embeddeddolt/,
        server 모드에서는 .beads/dolt/; gitignore 대상)
    ↕ dolt commit
로컬 Dolt 기록
    ↕ dolt push/pull
원격 Dolt 저장소(여러 머신에서 공유)
```

Dolt의 버전 관리 데이터베이스와 내장 복제 기능으로 자동 동기화가 이루어집니다.

## 다음 단계

- [설치](/getting-started/installation) - `bd`를 설치합니다.
- [빠른 시작](/getting-started/quickstart) - 첫 이슈를 만듭니다.
- [Beads 작동 방식](/core-concepts/index) - 개념 모델을 한 페이지에서 봅니다.
- [CLI 참조](/cli-reference/index) - 사용 가능한 모든 명령을 확인합니다.
- [워크플로](/workflows/index) - Formula, Molecule, Gate를 알아봅니다.
