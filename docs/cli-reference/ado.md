---
title: "bd ado"
description: "beads와 Azure DevOps 간 이슈 동기화 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc ado`에서 생성되었습니다.

beads와 Azure DevOps 간 이슈 동기화 명령입니다.

구성은 'bd config' 또는 환경 변수로 설정할 수 있습니다:
  ado.org / AZURE_DEVOPS_ORG              - 조직 이름
  ado.project / AZURE_DEVOPS_PROJECT      - 프로젝트 이름(단일)
  ado.projects / AZURE_DEVOPS_PROJECTS    - 프로젝트 이름(쉼표로 구분)
  ado.pat / AZURE_DEVOPS_PAT              - 개인용 액세스 토큰
  ado.url / AZURE_DEVOPS_URL              - 사용자 정의 기본 URL(온프레미스)

```
bd ado [flags]
```

## bd ado projects

구성된 토큰으로 접근 가능한 Azure DevOps 프로젝트를 나열합니다.

```
bd ado projects [flags]
```

## bd ado pull

Azure DevOps에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd ado sync --pull-only --issues &lt;refs&gt;

```
bd ado pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

## bd ado push

하나 이상의 beads 이슈를 Azure DevOps로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd ado sync --push-only --issues &lt;ids&gt;

```
bd ado push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd ado status

현재 Azure DevOps 구성과 동기화 상태를 표시합니다.

```
bd ado status [flags]
```

## bd ado sync

beads와 Azure DevOps 간에 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- Azure DevOps의 새 작업 항목 또는 업데이트된 작업 항목을 beads로 pull
- 로컬 beads 이슈를 Azure DevOps로 push

방향을 제한하려면 --pull-only 또는 --push-only를 사용하세요.

필터(--area-path, --iteration-path, --types, --states)는 동기화할 작업 항목을 제한합니다.
pull 시 WIQL 쿼리를 제한합니다. push 시 --types와 --states가 ADO로 push하기 전에
로컬 beads를 필터링합니다. 새 ADO 작업 항목 생성을 건너뛰고 기존 연결 항목만
업데이트하려면 push와 --no-create를 함께 사용하세요. 필터는 구성에 영구 저장할 수도 있습니다:
  ado.filter.area_path, ado.filter.iteration_path,
  ado.filter.types, ado.filter.states
둘 다 설정된 경우 CLI 플래그가 구성 값을 재정의합니다.

```
bd ado sync [flags]
```

**플래그:**

```
      --area-path string        ADO 영역 경로로 필터링(예: "Project\Team")
      --bootstrap-match         최초 동기화용 휴리스틱 일치 활성화
      --dry-run                 변경하지 않고 동기화될 항목 표시
      --issues string           선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --iteration-path string   ADO 반복 경로로 필터링(예: "Project\Sprint 1")
      --no-create               어느 방향에서도 새 항목을 생성하지 않음(pull 또는 push)
      --parent string           이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-ado              충돌 시 Azure DevOps 버전 사용
      --prefer-local            충돌 시 로컬 beads 버전 유지
      --prefer-newer            충돌 시 최신 버전 사용(기본값)
      --project strings         동기화할 프로젝트 이름(구성된 project/projects 재정의)
      --pull-only               Azure DevOps에서 이슈만 pull
      --push-only               Azure DevOps로 이슈만 push
      --reconcile               삭제된 항목의 조정 검사 강제
      --states string           쉼표로 구분한 ADO 상태로 필터링(예: "New,Active,Resolved")
      --types string            쉼표로 구분한 작업 항목 유형으로 필터링(예: "Bug,Task,User Story")
```
