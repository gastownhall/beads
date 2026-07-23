---
title: "bd jira"
description: "beads와 Jira 간에 이슈를 동기화합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc jira`에서 생성되었습니다.

beads와 Jira 간에 이슈를 동기화합니다.

구성:
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set jira.projects "PROJ1,PROJ2"   # 여러 프로젝트
  bd config set jira.api_token "YOUR_TOKEN"
  bd config set jira.username "your_email@company.com"  # Jira Cloud용
  bd config set jira.push_prefix "hippo"       # hippo-* 이슈만 Jira로 push
  bd config set jira.push_prefix "proj1,proj2" # 여러 접두사(쉼표로 구분)

환경 변수(구성 대신 사용):
  JIRA_API_TOKEN  - Jira API 토큰
  JIRA_USERNAME   - Jira 사용자 이름/이메일
  JIRA_PROJECTS   - 쉼표로 구분한 프로젝트 키

예시:
  bd jira sync --pull         # Jira에서 이슈 가져오기
  bd jira sync --push         # Jira로 이슈 내보내기
  bd jira sync                # 양방향 동기화(pull 후 push)
  bd jira sync --dry-run      # 변경 없이 동기화 미리 보기
  bd jira status              # 동기화 상태 표시

```
bd jira [flags]
```

## bd jira pull

Jira에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd jira sync --pull --issues &lt;refs&gt;

```
bd jira pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

## bd jira push

하나 이상의 beads 이슈를 Jira로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd jira sync --push --issues &lt;ids&gt;

```
bd jira push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd jira status

다음을 포함한 현재 Jira 동기화 상태를 표시합니다:
  - 마지막 동기화 타임스탬프
  - 구성 상태
  - Jira 링크가 있는 이슈 수
  - push 대기 중인 이슈(external_ref 없음)

```
bd jira status [flags]
```

## bd jira sync

beads와 Jira 간에 이슈를 동기화합니다.

모드:
  --pull         Jira에서 beads로 이슈 가져오기
  --push         beads에서 Jira로 이슈 내보내기
  (플래그 없음)  양방향 동기화: 충돌을 해결하며 pull 후 push

충돌 해결:
  기본적으로 더 최신 타임스탬프가 우선합니다. 다음으로 재정의할 수 있습니다:
  --prefer-local   항상 로컬 beads 버전 우선
  --prefer-jira    항상 Jira 버전 우선

예시:
  bd jira sync --pull                # Jira에서 가져오기
  bd jira sync --push --create-only  # 새 이슈만 push
  bd jira sync --dry-run             # 변경 없이 미리 보기
  bd jira sync --prefer-local        # 양방향, 로컬 우선

```
bd jira sync [flags]
```

**플래그:**

```
      --create-only       새 이슈만 생성하고 기존 이슈는 업데이트하지 않음
      --dry-run           변경 없이 동기화 미리 보기
      --issues string     선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string     이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-jira       충돌 시 Jira 버전 우선
      --prefer-local      충돌 시 로컬 버전 우선
      --project strings   동기화할 프로젝트 키(구성된 project/projects 재정의)
      --pull              Jira에서 이슈 pull
      --push              Jira로 이슈 push
      --state string      동기화할 이슈 상태: open, closed, all(기본값 "all")
```
