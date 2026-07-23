---
title: Azure DevOps(ADO) 통합 설정
description: Beads 이슈와 Azure DevOps 작업 항목을 양방향 동기화하는 bd ado sync 설정 참조
---

Last reviewed: 2026-05-08

Freshness source: `cmd/bd/ado*.go` 및 `internal/ado/`.

이 가이드는 Beads 이슈를 Azure DevOps 작업 항목과 동기화하는 `bd ado sync` 명령의
모든 설정 옵션을 다룹니다.

## 빠른 시작

```bash
# 필수 설정 지정
bd config set ado.pat "your-personal-access-token"
bd config set ado.org "your-organization"
bd config set ado.project "your-project"

# 또는 환경 변수 사용
export AZURE_DEVOPS_PAT="your-personal-access-token"
export AZURE_DEVOPS_ORG="your-organization"
export AZURE_DEVOPS_PROJECT="your-project"

# 동기화(양방향)
bd ado sync

# pull만 수행(ADO에서 import)
bd ado sync --pull-only

# push만 수행(ADO로 export)
bd ado sync --push-only

# 변경하지 않고 미리 보기
bd ado sync --dry-run
```

## 연결 설정

| 설정 key | 환경 변수 | 필수 | 설명 |
|---|---|---|---|
| `ado.pat` | `AZURE_DEVOPS_PAT` | 예 | Personal Access Token |
| `ado.org` | `AZURE_DEVOPS_ORG` | 조건부¹ | 조직 이름(예: `myorg`) |
| `ado.url` | `AZURE_DEVOPS_URL` | 조건부¹ | 커스텀 base URL(on-prem ADO 서버) |
| `ado.project` | `AZURE_DEVOPS_PROJECT` | 조건부² | 단일 프로젝트 이름 |
| `ado.projects` | `AZURE_DEVOPS_PROJECTS` | 조건부² | 쉼표로 구분한 프로젝트 이름 |

¹ `ado.org` 또는 `ado.url` 중 하나를 설정해야 합니다. on-premises Azure DevOps 서버에는 `ado.url`을 사용하세요.

² `ado.project` 또는 `ado.projects`로 프로젝트를 하나 이상 설정해야 합니다.

**설정과 환경 변수의 우선순위:** `bd config set`으로 지정한 설정 key가 환경 변수보다 우선합니다.

### on-premises ADO 서버

Azure DevOps 서버(on-prem)에는 `ado.org` 대신 `ado.url`을 사용하세요.

```bash
bd config set ado.url "https://tfs.company.com/DefaultCollection"
bd config set ado.project "MyProject"
```

### 다중 프로젝트 동기화

명령 하나로 여러 프로젝트를 동기화합니다.

```bash
bd config set ado.projects "ProjectA,ProjectB,ProjectC"
```

첫 프로젝트를 URL 구성의 주 대상으로 사용합니다. WIQL query는 다중 프로젝트 지원을
위해 `TeamProject IN (...)`을 사용합니다.

## 필터 설정

필터는 동기화 작업에 포함할 ADO 작업 항목을 제어합니다.

| 설정 key | CLI flag | 설명 | 예제 |
|---|---|---|---|
| `ado.filter.area_path` | `--area-path` | 영역 경로(UNDER 사용) | `Project\Team` |
| `ado.filter.iteration_path` | `--iteration-path` | sprint/iteration 경로 | `Project\Sprint 1` |
| `ado.filter.types` | `--types` | 작업 항목 유형(쉼표로 구분) | `Bug,Task,User Story` |
| `ado.filter.states` | `--states` | ADO 상태(쉼표로 구분) | `New,Active,Resolved` |

CLI flag는 해당 동기화 실행에서 설정 값을 override합니다.

**WIQL query 예제**(필터에서 생성):

```sql
SELECT [System.Id] FROM WorkItems WHERE
  [System.TeamProject] = 'MyProject'
  AND [System.IsDeleted] = false
  AND [System.AreaPath] UNDER 'Project\Team'
  AND [System.WorkItemType] IN ('Bug', 'Task')
  AND [System.State] IN ('New', 'Active')
  ORDER BY [System.ChangedDate] ASC
```

## 기본 mapping

### 우선순위 mapping

우선순위 mapping은 양방향이지만 **P3/P4에서는 손실이 발생**합니다.

| Beads 우선순위 | ADO 우선순위 | 방향 | 참고 |
|---|---|---|---|
| 0(긴급) | 1 | 양방향 | |
| 1(높음) | 2 | 양방향 | |
| 2(중간) | 3 | 양방향 | 알 수 없는 값의 기본값 |
| 3(낮음) | 4 | Beads에서 ADO | |
| 4(backlog) | 4 | Beads에서 ADO | **손실 발생**: pull 시 P3가 됨 |

> **참고:** Beads P3와 P4는 모두 ADO 우선순위 4에 대응합니다. 빈 데이터베이스로 새로
> pull하면 ADO 4가 Beads P3로 돌아옵니다. P4 이슈는 전체 왕복 과정에서 원래
> 우선순위가 보존되지 않습니다.

Bug 유형 작업 항목에는 ADO가 Severity 필드도 요구합니다.

| Beads 우선순위 | ADO Severity |
|---|---|
| 0 | 1 - Critical |
| 1 | 2 - High |
| 2 | 3 - Medium |
| 3, 4 | 4 - Low |

### 상태 mapping

| Beads 상태 | 기본 ADO 상태 | 설정 key |
|---|---|---|
| `open` | `New` | `ado.state_map.open` |
| `in_progress` | `Active` | `ado.state_map.in_progress` |
| `blocked` | `Active` + `beads:blocked` tag | `ado.state_map.blocked` |
| `deferred` | `Removed` | `ado.state_map.deferred` |
| `closed` | `Closed` | `ado.state_map.closed` |

**차단 상태:** ADO에는 native 차단 상태가 없습니다. Beads는 차단 상태를 `Active`에
대응시키고 `beads:blocked` tag를 추가합니다. pull 시 `Active` 및 `beads:blocked`
tag가 `StatusBlocked`를 복원합니다.

process 템플릿에 맞게 기본값을 override하세요.

```bash
# 예제: Scrum 템플릿
bd config set ado.state_map.open "To Do"
bd config set ado.state_map.in_progress "In Progress"
bd config set ado.state_map.closed "Done"
```

### 유형 mapping

| Beads 유형 | 기본 ADO 유형 | 설정 key |
|---|---|---|
| `bug` | `Bug` | `ado.type_map.bug` |
| `feature` | `User Story` | `ado.type_map.feature` |
| `task` | `Task` | `ado.type_map.task` |
| `epic` | `Epic` | `ado.type_map.epic` |
| `chore` | `Task` | `ado.type_map.chore` |

역방향 mapping(ADO에서 Beads)도 다음 항목을 인식합니다.

- `Product Backlog Item`은 `feature`에 대응(Scrum 템플릿)
- `Issue`는 `task`에 대응

process 템플릿에 맞게 override하세요.

```bash
# 예제: Scrum 템플릿
bd config set ado.type_map.feature "Product Backlog Item"
```

## process 템플릿 설정

ADO는 작업 항목 유형과 상태 전환이 서로 다른 여러 process 템플릿을 지원합니다. 기본값은
**Agile** 템플릿을 가정합니다. 다른 템플릿에는 mapping을 override하세요.

### Agile(기본값)

설정할 필요가 없습니다. 기본 mapping이 바로 작동합니다.

상태 전환:
```
Bug:         New → Active → Resolved → Closed
Task:        New → Active → Closed
User Story:  New → Active → Resolved → Closed
Epic:        New → Active → Resolved → Closed
```

### Scrum

```bash
bd config set ado.type_map.feature "Product Backlog Item"
bd config set ado.state_map.open "New"
bd config set ado.state_map.in_progress "Committed"
bd config set ado.state_map.closed "Done"
```

상태 전환:
```
Product Backlog Item: New → Approved → Committed → Done
Task:                 To Do → In Progress → Done
Bug:                  New → Approved → Committed → Done
```

### CMMI

```bash
bd config set ado.type_map.feature "Requirement"
bd config set ado.state_map.open "Proposed"
bd config set ado.state_map.in_progress "Active"
bd config set ado.state_map.closed "Closed"
```

상태 전환:
```
Requirement: Proposed → Active → Resolved → Closed
Task:        Proposed → Active → Closed
Bug:         Proposed → Active → Resolved → Closed
```

### 상태 전환 처리

초기 상태가 아닌 작업 항목을 생성할 때(예: 종료된 이슈 push) Beads는 다음과 같이
처리합니다.

1. 초기 상태(예: `New`)로 항목을 생성합니다.
2. 중간 상태를 거쳐 대상 상태에 도달합니다.
3. 예: 종료된 Bug 생성은 `New → Active → Resolved → Closed` 순서로 진행됩니다.

직접 전환에 실패하면(ADO가 400 반환) Beads가 작업 항목 유형과 process 템플릿의 알려진
전환 경로를 자동으로 따릅니다.

## 동기화 옵션

### 방향

| 플래그 | 설명 |
|---|---|
| 없음 | 양방향: pull 후 push |
| `--pull-only` | ADO에서만 import |
| `--push-only` | ADO로만 export |

### 충돌 해결

같은 이슈가 로컬과 ADO에서 모두 수정된 경우입니다.

| 플래그 | 설명 |
|---|---|
| `--prefer-newer` | 가장 최근에 업데이트된 버전이 우선(기본값) |
| `--prefer-local` | 로컬 Beads 버전이 항상 우선 |
| `--prefer-ado` | ADO 버전이 항상 우선 |

### 추가 플래그

| 플래그 | 설명 |
|---|---|
| `--dry-run` | 변경하지 않고 동기화 미리 보기 |
| `--no-create` | 기존 항목만 업데이트하고 새 항목은 생성하지 않음 |
| `--bootstrap-match` | 첫 동기화에서 heuristic 제목 일치 활성화 |
| `--reconcile` | 삭제된 항목의 조정 scan 강제 실행 |
| `--issues` | Bead ID 또는 ADO 작업 항목 ID로 특정 이슈 동기화 |
| `--states` | 작업 항목 상태로 필터링(쉼표로 구분) |
| `--types` | 작업 항목 유형으로 필터링(쉼표로 구분) |
| `--issues` | ID로 특정 Beads 동기화 |

## PAT 권한

Personal Access Token에 다음 scope가 필요합니다.

| scope | 접근 권한 | 필요한 작업 |
|---|---|---|
| Work Items | Read & Write | 작업 항목 생성 및 업데이트 |

다음 위치에서 PAT를 생성하세요: `https://dev.azure.com/{org}/_usersettings/tokens`

## 보존되는 metadata

Beads는 왕복 충실도를 위해 ADO별 metadata를 저장합니다.

| metadata key | 설명 |
|---|---|
| `ado.rev` | ADO revision 번호 |
| `ado.area_path` | 영역 경로 |
| `ado.iteration_path` | iteration/sprint 경로 |
| `ado.story_points` | story point 추정치 |
| `ado.remaining_work` | 남은 작업 시간 |
| `ado.severity` | Bug severity 값 |

## 설명 변환

- **push(Beads에서 ADO):** Markdown을 HTML로 변환
- **pull(ADO에서 Beads):** HTML을 Markdown으로 변환

## tag와 레이블

- ADO tag는 세미콜론으로 구분하고 Beads 레이블은 array를 사용합니다.
- 사용자 레이블은 ADO tag를 통해 왕복합니다.
- 내부 `beads:*` tag(예: `beads:blocked`)는 pull 시 필터링되므로 사용자 레이블로 표시되지 않습니다.

## API 제한

| 제한 | 값 |
|---|---|
| 최대 batch 크기 | GET 요청당 작업 항목 200개 |
| 최대 응답 크기 | 50MB |
| 요청 timeout | 30초 |
| 최대 재시도 | 3회(GET 및 WIQL만) |
| 재시도 backoff | jitter가 있는 지수형, `Retry-After` header 준수 |

## 문제 해결

### 일반적인 오류

**`ado.pat not configured: set via 'bd config set ado.pat <token>' or AZURE_DEVOPS_PAT env var`**
```bash
bd config set ado.pat "your-pat-here"
# 또는
export AZURE_DEVOPS_PAT="your-pat-here"
```

**"ado.organization not configured"**
```bash
bd config set ado.org "your-org"
# 또는 on-prem의 경우
bd config set ado.url "https://tfs.company.com/DefaultCollection"
```

**상태 전환 오류(400 Bad Request)**
일반적으로 process 템플릿이 직접 상태 변경을 지원하지 않는다는 의미입니다.
`ado.state_map.*` 설정이 실제 process 템플릿과 일치하는지 확인하세요.

**유형을 찾을 수 없음 오류**
`ado.type_map.*` 설정이 프로젝트에서 사용할 수 있는 작업 항목 유형과 일치하는지
확인하세요. 동기화할 유형을 제한하려면 `--types` 필터를 사용하세요.

### 디버깅

```bash
# 실행 결과 미리 보기
bd ado sync --dry-run

# 현재 설정 확인
bd config get ado.pat
bd config get ado.org
bd config get ado.project
```
