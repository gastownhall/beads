---
title: 자주 묻는 질문(FAQ)
description: beads와 효과적인 사용법에 관한 일반적인 질문
---

## 일반

### beads란 무엇인가요?

Beads(`bd`)는 AI 코딩 에이전트용으로 설계된 가벼운 Dolt 기반 이슈 트래커입니다. 머신 간 동기화가 내장된 의존성 인식 작업 관리를 제공하므로 에이전트와 사람이 같은 작업 그래프에서 협업할 수 있습니다. 전체 모델은 [핵심 개념](/core-concepts/index)을 참조하세요.

### GitHub Issues나 Jira 대신 beads를 사용하는 이유는 무엇인가요?

GitHub Issues와 `gh` CLI를 함께 사용하면 일부 기능을 비슷하게 구현할 수 있지만, 호스팅 트래커는 AI 에이전트에 필요한 것을 근본적으로 재현할 수 없습니다.

| 기능 | beads | GitHub Issues |
|---|---|---|
| 유형이 지정된 의존성 | 핵심 유형(`blocks`, `related`, `parent-child`, `discovered-from`)과 워크플로 및 지식 그래프 에지 | "blocks/blocked by" 링크만 제공, 의미 체계 강제 없음, 에이전트 작업 발견용 `discovered-from` 없음 |
| 준비된 작업 감지 | `bd ready`가 오프라인에서 밀리초 내에 전이적 차단 계산 | 내장 "ready" 개념 없음, 사용자 지정 GraphQL과 동기화 서비스 필요 |
| 오프라인 우선 작업 메모리 | 오프라인 작동, 이슈가 로컬 버전 관리 데이터베이스에 저장, 해시 ID가 병합 충돌 방지 | 클라우드 우선, 네트워크와 인증 필요, 브랜치 범위 작업 상태 없음 |
| 충돌과 중복 | 자동 충돌 해결, 의존성 통합과 참조 재작성으로 중복 병합 | 수동 중복 닫기, 안전한 일괄 병합과 상호 참조 업데이트 없음 |
| 로컬 SQL 데이터베이스 | 네이티브 버전 관리 기능이 있는 로컬 Dolt 데이터베이스에 전체 SQL 쿼리 | 로컬 데이터베이스 없음, 외부에 데이터를 미러링해야 함 |
| 에이전트 네이티브 API | 모든 명령에 일관된 `--json`, 워크스페이스 감지 기능이 있는 전용 MCP 서버 | JSON/텍스트 출력 혼용, 에이전트 중심 MCP 계층 없음 |

각 도구의 사용 시점: GitHub Issues와 Jira는 저장소 간 대시보드와 통합이 있는 웹 UI에서 작업하는 사람 중심 팀에 뛰어납니다. Beads는 그래프 의미 체계와 결정적 쿼리가 있는 오프라인 버전 관리 작업 메모리가 필요한 AI 에이전트에 뛰어납니다. 둘은 공존할 수도 있습니다. beads는 GitHub, Jira, Linear와 양방향으로 동기화됩니다([마이그레이션](#migration) 참조).

### beads는 Taskwarrior와 어떻게 다른가요?

Taskwarrior는 개인 작업 관리에 뛰어나지만 beads는 AI 에이전트용으로 만들어졌습니다.

- **에이전트 의미 체계**: `discovered-from` 의존성 유형, 큐 관리용 `bd ready`
- **JSON 우선 설계**: 모든 명령에 `--json` 출력 제공
- **내장 동기화**: 네이티브 푸시/풀이 있는 버전 관리 저장소, 별도 동기화 서버 불필요
- **셀 수준 병합**: 동시 변경 사항을 필드 수준에서 자동 병합
- **SQL 데이터베이스**: Dolt 데이터베이스에 전체 SQL 쿼리

### AI 에이전트 없이 beads를 사용할 수 있나요?

물론입니다. Beads는 사람에게도 좋은 CLI 이슈 트래커이며 `bd ready`는 의존성을 관리하는 누구에게나 유용합니다. "Taskwarrior와 git의 만남"이라고 생각하세요.

### "beads"는 무엇을 뜻하나요?

특정한 약자는 아닙니다. 줄에 꿴 구슬처럼 연결된 작업 항목을 나타내는 비유입니다.

### beads는 프로덕션에 사용할 준비가 되었나요?

Beads는 AI 지원 개발의 프로덕션 환경에서 사용하는 1.x 제품입니다. 생성, 업데이트, 의존성, 준비된 작업, Dolt 기반 동기화 등 핵심 기능이 안정적이며 릴리스는 유의적 버전 관리를 따릅니다. 데이터는 이식 가능합니다. `bd export`는 사람이 읽을 수 있는 JSONL을 생성하고 `bd backup`은 Dolt 네이티브 백업을 푸시합니다. 중요한 작업을 보관하는 모든 트래커와 마찬가지로 일반적인 백업 관리(Dolt 원격 또는 `bd backup` 대상)를 유지하세요.

## 아키텍처

### 일반 SQLite나 플랫 파일 대신 Dolt를 사용하는 이유는 무엇인가요?

Dolt는 데이터베이스 수준에서 git 의미 체계를 제공하는 버전 관리 SQL 데이터베이스입니다.

- **버전 관리 SQL**: 브랜치, diff, 병합이 내장된 전체 SQL 쿼리
- **셀 수준 병합**: 동시 변경 사항을 필드 수준에서 자동 병합
- **별도 동기화 형식 없음**: `bd dolt push` / `bd dolt pull`이 기록을 네이티브로 이동
- **다중 기록자**: 서버 모드에서 동시 에이전트 지원
- **이식 가능**: `bd export`가 마이그레이션과 상호 운용용 JSONL 생성

자세한 분석은 [Dolt 아키텍처](/architecture/dolt)를 참조하세요.

### 순차 ID 대신 해시 기반 ID를 사용하는 이유는 무엇인가요?

순차 ID(`#1`, `#2`)는 두 에이전트 또는 두 브랜치가 동시에 이슈를 생성하는 순간 충돌합니다. 양쪽 모두 같은 다음 번호를 만들고 병합하면 하나의 ID에 서로 다른 이슈 두 개가 생깁니다. `bd-a1b2` 같은 해시 ID는 조율 없이 전역적으로 고유합니다.

```bash
# 브랜치 A
bd create "OAuth 추가"   # bd-a1b2

# 브랜치 B
bd create "Stripe 추가"  # bd-f14c — 충돌 없음

git merge feature-auth  # 깔끔한 병합, 서로 다른 ID
```

ID는 3자로 시작하고 데이터베이스가 커짐에 따라 자동으로 최대 8자까지 늘어나 충돌 확률을 고정 임곗값 아래로 유지합니다. [해시 ID](/core-concepts/hash-ids)와 [적응형 ID](/core-concepts/adaptive-ids)를 참조하세요. 수치가 필요하면 [충돌 계산](https://github.com/gastownhall/beads/blob/main/engdocs/COLLISION_MATH.md)을 참조하세요.

### 계층적 자식 ID란 무엇인가요?

계층적 ID(`bd-a3f8e9.1`, `bd-a3f8e9.2`)는 epic과 하위 작업에 사람이 읽기 쉬운 구조를 제공합니다.

```bash
bd create "인증 시스템" -t epic            # bd-a3f8e9
bd create "로그인 UI" --parent bd-a3f8e9   # bd-a3f8e9.1
bd create "검증" --parent bd-a3f8e9        # bd-a3f8e9.2
```

부모 해시는 epic 간 네임스페이스를 고유하게 유지하고 자식 번호는 사람이 이해하기 쉬우며 최대 3단계 중첩을 지원합니다. 작업 분할 구조에는 계층적 ID를 사용하고 횡단 관계에는 대신 `bd dep add`를 사용하세요. [해시 ID](/core-concepts/hash-ids)를 참조하세요.

### 임베디드 모드와 서버 모드 중 어떤 모드를 실행 중인가요?

기본 `bd init`은 **임베디드 모드**를 사용합니다. Dolt가 `bd` 프로세스 내부에서 실행되고 데이터는 `.beads/embeddeddolt/`에 있으며 관리할 서버, 포트, PID 파일이 없습니다. 개인 작업, CI/CD, 단일 에이전트 설정에 적합합니다.

**서버 모드**(`bd init --server`)는 실행 중인 `dolt sql-server`에 연결하고 데이터를 `.beads/dolt/`에 저장합니다. 한 머신의 여러 에이전트처럼 여러 프로세스가 같은 데이터베이스에 동시에 쓰기 접근해야 할 때 전환하세요. 모드 간 설정과 마이그레이션은 [Dolt 아키텍처](/architecture/dolt)를 참조하세요.

## 사용법

### bd init을 직접 실행해야 하나요, 에이전트가 실행하게 해야 하나요?

둘 다 가능합니다. 올바른 플래그를 사용하세요.

```bash
bd init          # 사람: 대화형, git 훅 설치 여부 질문
bd init --quiet  # 에이전트: 비대화형, 훅 자동 설치
```

기존 프로젝트에서는 클론한 뒤 `bd init`을 실행하세요. Dolt 데이터베이스를 만들고 구성된 원격에서 풀합니다. 새 프로젝트에서는 `bd init`을 실행한 뒤 `.beads/` 디렉터리를 커밋하세요.

### 머신 간에 이슈를 어떻게 동기화하나요?

```bash
bd dolt push    # 변경 사항을 Dolt 원격에 푸시
bd dolt pull    # Dolt 원격에서 변경 사항 풀
```

git origin이 있으면 `bd init`이 Dolt 원격으로 자동 구성합니다. 명시적 원격에는 `bd init --remote <url>`을 사용하세요. [동기화 설정](/getting-started/sync-setup)을 참조하세요.

### 내보내기/가져오기를 수동으로 실행해야 하나요?

아니요. 모든 쓰기는 Dolt 데이터베이스로 직접 이동해 Dolt 기록에 자동 커밋되며 `bd dolt push` / `bd dolt pull`이 동기화를 처리합니다. `bd export`는 이식과 교환을 위한 기능입니다. `.beads/issues.jsonl`은 수동 내보내기일 뿐 데이터베이스가 아닙니다. 백업에는 `bd backup init <path>` / `bd backup sync` / `bd backup restore`를 사용하세요. 전체 모델과 피해야 할 동기화 패턴은 [동기화 개념](/core-concepts/sync-concepts)을 참조하세요.

### 동료가 변경 사항을 푸시한 뒤 데이터베이스가 오래된 것 같으면 어떻게 하나요?

```bash
bd dolt pull    # Dolt 원격에서 업데이트를 페치하고 병합
bd ready        # 최신 데이터 표시
```

페더레이션 설정에서는 `bd federation sync`가 구성된 모든 피어와 동기화합니다. [페더레이션](/multi-agent/federation)을 참조하세요.

### 병합 충돌을 어떻게 처리하나요?

Dolt는 셀 수준에서 병합하므로 대부분의 동시 변경 사항이 자동으로 해결됩니다. ID는 같지만 필드가 다른 이슈를 가져오는 것은 충돌이 아니라 업데이트입니다. 해시 ID는 안정적이므로 같은 ID는 같은 이슈를 뜻합니다. `bd dolt pull`이 충돌을 보고한다면 다음을 실행합니다.

```bash
bd doctor --fix
bd dolt push
```

[병합 충돌 런북](/recovery/merge-conflicts)을 참조하세요.

### 여러 프로젝트의 이슈를 추적할 수 있나요?

예. 각 프로젝트가 완전히 격리됩니다.

```bash
cd ~/project1 && bd init --prefix proj1
cd ~/project2 && bd init --prefix proj2
```

각 프로젝트에는 자체 `.beads/` 디렉터리와 데이터베이스가 생기며 `bd`는 git처럼 현재 디렉터리에서 위로 올라가며 올바른 디렉터리를 자동으로 찾습니다. 프로젝트 간 작업을 연결하려면 다른 저장소를 데이터베이스에 hydrate하고(`bd repo add`, 이어서 `bd repo sync`) 일반 의존성을 추가하거나, `external:<project>:<capability>` 대상으로 다른 프로젝트의 기능에 의존하세요. [저장소 간 라우팅](/multi-agent/routing)을 참조하세요.

서버 모드에서는 기본적으로 프로젝트마다 자체 Dolt 서버를 실행합니다. 프로젝트가 많은 머신에서는 `~/.beads/shared-server/`에서 모든 프로젝트를 제공하는 단일 공유 서버(`bd init --shared-server` 또는 `export BEADS_DOLT_SHARED_SERVER=1`)를 선택할 수 있습니다. [Dolt 아키텍처](/architecture/dolt)를 참조하세요.

### 여러 에이전트가 같은 저장소에서 작업할 수 있나요?

예. 바로 그것을 위해 beads가 설계되었습니다. 해시 ID는 충돌을 방지하고 할당은 누가 무엇을 작업하는지 추적합니다.

```bash
bd ready --assignee agent-name       # 에이전트의 준비된 작업 쿼리
bd update bd-a1b2 --claim            # 이슈를 원자적으로 클레임(assignee + in_progress)
bd create "발견한 이슈" --deps discovered-from:bd-a1b2   # 발견한 작업 추적
```

오케스트레이션된 워크플로에서는 보통 오케스트레이터가 작업을 할당합니다(`bd assign`). 작업을 직접 선택하는 에이전트는 원자적 `--claim`을 사용해야 합니다. 한 머신의 여러 동시 프로세스에는 서버 모드를 사용하세요. 분산 설정에서는 Dolt의 셀 수준 병합과 [페더레이션](/multi-agent/federation)을 통해 에이전트가 독립적으로 작업하고 개발자처럼 병합할 수 있습니다. [에이전트 조정](/multi-agent/coordination)을 참조하세요.

### beads는 오프라인에서 작동하나요?

예. beads는 오프라인 우선입니다. 모든 쿼리는 로컬 Dolt 데이터베이스를 대상으로 실행되고 어떤 명령에도 네트워크가 필요하지 않으며 온라인일 때 `bd dolt push` / `bd dolt pull`로 동기화합니다. 비행기, 불안정한 연결, 에어 갭 환경, 개인 정보에 민감한 프로젝트에 적합합니다.

### CI/CD에서 beads를 어떻게 사용하나요?

명령을 실행하기만 하면 됩니다. 임베디드 모드가 기본값이므로 서버가 필요 없습니다.

```bash
bd list --json
bd ready --json
```

## 워크플로

### Formula란 무엇인가요?

TOML 또는 JSON으로 작성한 선언적 워크플로 템플릿입니다. `bd cook`은 Formula를 proto(템플릿 epic)로 컴파일하고 `bd mol pour`는 proto를 실제로 추적되는 bead의 Molecule로 인스턴스화합니다. [Formulas](/workflows/formulas)를 참조하세요.

### Gate란 무엇인가요?

조건이 해소될 때까지 워크플로 단계를 차단하는 비동기 조정 기본 요소입니다.

- **사람 Gate**는 승인을 기다립니다.
- **타이머 Gate**는 일정 시간을 기다립니다.
- **GitHub Gate**는 CI 실행 또는 PR 이벤트를 기다립니다.

[Gates](/workflows/gates)를 참조하세요.

### Molecule과 Wisp의 차이는 무엇인가요?

둘 다 실제 bead로 구성된 인스턴스화된 워크플로입니다. **Molecule**(`bd mol pour`)은 지속적이며 기록의 일부로서 다른 bead처럼 동기화됩니다. **Wisp**(`bd mol wisp`)는 임시이며 페더레이션 푸시에서 제외되도록 표시되고 닫힌 뒤 전체 삭제(`bd purge`, `bd mol wisp gc`)할 수 있습니다. 나중에 참조할 가치가 있는 작업에는 Molecule을, 릴리스 체크리스트와 상태 순찰 같은 운영 루프에는 Wisp를 사용하세요. [Molecules](/workflows/molecules)와 [Wisps](/workflows/wisps)를 참조하세요.

## 통합

### CLI와 MCP 중 무엇을 사용해야 하나요?

셸을 사용할 수 있다면(Claude Code, Cursor 등) **CLI + 훅을 사용**하세요.

- 더 낮은 컨텍스트 오버헤드(전체 MCP 도구 스키마는 수만 토큰인 데 비해 수천 토큰 수준)
- 더 빠른 실행
- 편집기 전반에서 범용적

CLI를 사용할 수 없다면(예: Claude Desktop) **MCP를 사용**하세요. [MCP 서버](/integrations/mcp-server)를 참조하세요.

### 편집기와 어떻게 통합하나요?

```bash
bd setup claude   # Claude Code
bd setup cursor   # Cursor
bd setup aider    # Aider
```

`bd setup`은 copilot, gemini, factory, codex, mux, opencode, junie, windsurf, cody, kilocode도 지원합니다. [IDE 설정](/getting-started/ide-setup)과 [통합 색인](/integrations/index)을 참조하세요.

### beads가 GitHub Issues에서 가져올 수 있나요?

예. `bd github sync --pull-only`는 이슈를 일괄 가져오며(`bd github pull <refs>`는 특정 이슈를 체리픽), `bd github sync`는 beads와 GitHub를 양방향으로 동기화합니다. [bd github 참조](/cli-reference/github)를 확인하세요.

<a id="migration"></a>

## 마이그레이션

### GitHub Issues, Jira 또는 Linear에서 어떻게 마이그레이션하나요?

Beads에는 세 서비스 모두에 대한 양방향 동기화가 내장되어 있습니다. `bd github`, `bd jira`, `bd linear`은 각각 일괄 이동용 `sync`와 ID별 특정 이슈용 `pull`/`push`를 제공합니다(GitLab, Azure DevOps, Notion은 `bd gitlab`, `bd ado`, `bd notion`으로 지원). [CLI 참조](/cli-reference/index)에 따라 `bd config set`으로 자격 증명을 구성한 뒤 풀 방향으로 동기화를 실행하세요. `bd github sync --pull-only`, `bd jira sync --pull` 또는 `bd linear sync --pull`입니다.

다른 트래커의 경우 보통 CSV 또는 JSON으로 내보내 beads의 JSONL 형식으로 변환한 다음 `bd import <file>`을 실행합니다. 스크립팅 패턴은 [예시](https://github.com/gastownhall/beads/tree/main/examples)를 참조하세요.

### beads에서 다시 내보낼 수 있나요?

GitHub, Jira, Linear은 같은 통합을 푸시 방향으로 사용합니다. 전체에는 `bd github sync --push-only`, `bd jira sync --push` 또는 `bd linear sync --push`를, 특정 bead에는 `bd <tracker> push <ids>`를 사용하세요. 다른 시스템에는 `bd export -o issues.jsonl`로 JSONL을 생성해 스크립트로 변환한 뒤 대상 시스템 API에 전달할 수 있습니다.

## 성능

### beads는 규모 확장을 어떻게 처리하나요?

Dolt는 SQL 데이터베이스이며 일반적인 프로젝트에 누적되는 것보다 훨씬 많은 이슈를 충분히 처리합니다. 이슈 수천 개 규모에서도 명령이 빠르게 유지됩니다. 이슈가 10만 개 이상인 매우 큰 프로젝트는 구성 요소별 여러 데이터베이스로 분할하는 방안을 고려하세요.

### 데이터베이스가 너무 커지면 어떻게 하나요?

`bd gc`는 전체 수명 주기를 실행합니다. 오래된 닫힌 이슈를 삭제하고 이전 Dolt 커밋을 스쿼시하며 Dolt 가비지 컬렉션을 실행해 디스크 공간을 회수합니다.

```bash
bd gc --dry-run          # 모든 단계 미리 보기
bd gc                    # 90일 이상 전에 닫힌 이슈 삭제, 압축, GC
bd gc --older-than 30    # 더 적극적인 감쇠 기간
```

오래된 닫힌 이슈를 삭제하는 대신 의미론적으로 요약하려면 `bd admin compact`를 참조하세요. 또는 프로젝트를 분할합니다.

```bash
cd ~/project/frontend && bd init --prefix fe
cd ~/project/backend && bd init --prefix be
```

## 사용 사례

### 코드가 아닌 프로젝트에도 beads를 사용할 수 있나요?

물론입니다. beads는 이슈 트래커입니다. 글쓰기 프로젝트(장을 이슈로, 개요를 의존성으로), 연구(논문, 실험), 집안 프로젝트(차단 작업이 있는 수리) 등 의존성이 있는 모든 워크플로에 사용할 수 있으며 에이전트 친화적 설계는 모든 AI 지원 워크플로에 적합합니다.

## 기술

### beads에는 어떤 의존성이 있나요?

Beads는 런타임 의존성이 없는 단일 정적 바이너리이며 Dolt 엔진이 프로세스 내부에 임베드되어 있습니다. PostgreSQL, Redis, Docker, node_modules가 필요 없습니다. 독립 실행형 `dolt` CLI는 서버 모드를 실행할 때만 필요하고 git은 프로젝트 코드 버전 관리에만 필요합니다. [설치](/getting-started/installation)를 참조하세요.

### 데이터베이스를 직접 쿼리하거나 확장할 수 있나요?

예. 세 가지 방법이 있습니다. 내장 쿼리 언어(복합 필터, 불리언 연산자, 날짜 표현식)용 `bd query`, 기반 데이터베이스에 원시 SQL을 실행하는 `bd sql`, 통합 구축을 위한 모든 명령의 `--json` 출력입니다.

### beads는 Windows를 지원하나요?

예. MSYS나 MinGW 없이 Windows를 네이티브로 지원합니다. PowerShell 스크립트가 미리 빌드된 릴리스를 설치하며 모든 기능이 Windows 경로에서 작동합니다. [설치](/getting-started/installation#windows-11)를 참조하세요.

### git 워크트리와 함께 beads를 사용할 수 있나요?

예. beads는 특별한 설정 없이 일반 git 워크트리에서 작동합니다. 저장소의 모든 워크트리는 같은 `.beads` 워크스페이스를 공유하며 `bd`가 연결된 워크트리에서 자동으로 찾습니다.

이전 beads 버전이 제거된 동기화 브랜치 기능으로 `.git/` 아래에 숨겨진 워크트리를 남겼다면 정리하세요.

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

자세한 내용과 레거시 정리는 [워크트리](/reference/worktrees)를 참조하세요.

## 문제 해결

### Dolt 서버가 시작되지 않는 이유는 무엇인가요?

서버 모드에만 해당합니다(임베디드 모드에는 서버가 없음).

```bash
bd doctor                        # 상태 확인
cat .beads/dolt-server.log       # 서버 로그 확인(서버 모드)
bd dolt stop && bd dolt start    # 서버 다시 시작
```

[문제 해결](/reference/troubleshooting)을 참조하세요.

### 변경 사항이 동기화되지 않는 이유는 무엇인가요?

```bash
bd dolt push     # Dolt 원격에 푸시
bd hooks list    # git 훅 설치 확인
bd doctor        # 더 깊은 문제 확인
```

[동기화 실패 런북](/recovery/sync-failures)을 참조하세요.

### 데이터베이스 손상과 ID 충돌의 차이는 무엇인가요?

서로 다른 두 가지 무결성 문제입니다.

**논리적 일관성** — 서로 다른 이슈에 같은 ID 할당, 잘못된 접두사 버그, 브랜치 분기입니다. 해시 기반 ID는 설계상 충돌을 제거하고 `bd doctor --fix`는 논리적 불일치를 복구합니다.

**물리적 손상** — 디스크 장애, 전원 손실 또는 여러 프로세스가 임베디드 데이터베이스에 동시에 쓰는 상황입니다. [데이터베이스 손상 런북](/recovery/database-corruption)부터 시작하세요(`.beads/`를 백업한 뒤 `bd doctor --fix`). 데이터베이스를 복구할 수 없고 Dolt 원격이 있다면 `.beads/`를 백업하고 해당 모드의 데이터 디렉터리(임베디드 모드는 `.beads/embeddeddolt/`, 서버 모드는 `.beads/dolt/`)를 삭제한 다음 다시 초기화하고 풀하세요.

```bash
cp -r .beads .beads.backup
rm -rf .beads/embeddeddolt   # 서버 모드에서는 .beads/dolt
bd init
bd dolt pull
```

다중 기록자 시나리오에서는 파일에서 경쟁하지 않고 동시 접근이 서버를 통하도록 서버 모드를 사용하세요.

### 버그를 어떻게 신고하나요?

1. 기존 이슈 확인: https://github.com/gastownhall/beads/issues
2. `bd version`, `bd info --json`, 재현 단계 포함
3. 제출 위치: https://github.com/gastownhall/beads/issues/new

## 도움받기

### 어디서 더 도움을 받을 수 있나요?

- **문서**: [빠른 시작](/getting-started/quickstart), [고급 기능](/reference/advanced), [README](https://github.com/gastownhall/beads/blob/main/README.md)
- **문제 해결**: [문제 해결 가이드](/reference/troubleshooting)와 [복구 런북](/recovery/index)
- **예시**: [examples/](https://github.com/gastownhall/beads/tree/main/examples)
- **GitHub Issues**: [버그 신고 또는 기능 요청](https://github.com/gastownhall/beads/issues)
- **GitHub Discussions**: [질문하기](https://github.com/gastownhall/beads/discussions)

### 어떻게 기여할 수 있나요?

기여를 환영합니다. 지침, 테스트 방법, 개발 워크플로는 [CONTRIBUTING.md](https://github.com/gastownhall/beads/blob/main/CONTRIBUTING.md)를 참조하세요.

### 로드맵은 어디에 있나요?

로드맵은 beads 자체에 있습니다.

```bash
bd list --priority-max 1 --json   # 모든 P0 및 P1 이슈
```

또는 GitHub Issues에서 기능 요청과 계획된 개선 사항을 확인하세요.
