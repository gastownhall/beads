---
title: "bd gitlab"
description: "beads와 GitLab 간 이슈 동기화 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc gitlab`에서 생성되었습니다.

beads와 GitLab 간 이슈 동기화 명령입니다.

구성은 'bd config' 또는 환경 변수로 설정할 수 있습니다:
  gitlab.url / GITLAB_URL                         - GitLab 인스턴스 URL
  gitlab.token / GITLAB_TOKEN                     - 개인용 액세스 토큰
  gitlab.project_id / GITLAB_PROJECT_ID           - 프로젝트 ID 또는 경로
  gitlab.group_id / GITLAB_GROUP_ID               - 그룹 수준 동기화용 그룹 ID
  gitlab.default_project_id / GITLAB_DEFAULT_PROJECT_ID - 그룹 모드에서 이슈를 생성할 프로젝트

```
bd gitlab [flags]
```

## bd gitlab projects

구성된 토큰으로 접근 가능한 GitLab 프로젝트를 나열합니다.

```
bd gitlab projects [flags]
```

## bd gitlab pull

GitLab에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd gitlab sync --pull-only --issues &lt;refs&gt;

```
bd gitlab pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

## bd gitlab push

하나 이상의 beads 이슈를 GitLab으로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd gitlab sync --push-only --issues &lt;ids&gt;

```
bd gitlab push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd gitlab status

현재 GitLab 구성과 동기화 상태를 표시합니다.

```
bd gitlab status [flags]
```

## bd gitlab sync

beads와 GitLab 간에 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- GitLab의 새 이슈 또는 업데이트된 이슈를 beads로 pull
- 로컬 beads 이슈를 GitLab으로 push

방향을 제한하려면 --pull-only 또는 --push-only를 사용하세요.

```
bd gitlab sync [flags]
```

**플래그:**

```
      --assignee string       담당자 사용자 이름으로 필터링
      --dry-run               변경하지 않고 동기화될 항목 표시
      --exclude-type string   동기화에서 해당 이슈 유형 제외(쉼표로 구분)
      --issues string         선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --label string          레이블로 필터링(쉼표로 구분, AND 논리)
      --milestone string      마일스톤 제목으로 필터링
      --no-ephemeral          push에서 임시/wisp 이슈 제외(기본값: true)(기본값 true)
      --parent string         이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-gitlab         충돌 시 GitLab 버전 사용
      --prefer-local          충돌 시 로컬 beads 버전 유지
      --prefer-newer          충돌 시 최신 버전 사용(기본값)
      --project string        이 프로젝트 ID의 이슈로 필터링(그룹 모드)
      --pull-only             GitLab에서 이슈만 pull
      --push-only             GitLab으로 이슈만 push
      --type string           해당 이슈 유형만 동기화(쉼표로 구분, 예: 'epic,feature,task')
```
