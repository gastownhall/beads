---
title: "bd repo"
description: "다중 저장소 hydration을 위한 여러 저장소 지원을 구성하고 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc repo`에서 생성되었습니다.

다중 저장소 hydration을 위한 여러 저장소 지원을 구성하고 관리합니다.

다중 저장소 지원을 사용하면 여러 beads 저장소의 이슈를 단일 데이터베이스로
hydrate하여 저장소 간 이슈 추적을 통합할 수 있습니다.

구성은 .beads/config.yaml의 'repos' 섹션에 저장됩니다:

  repos:
    primary: "."
    additional:
      - ~/beads-planning
      - ~/work-repo

예시:
  bd repo add ~/beads-planning       # 계획 저장소 추가
  bd repo add ../other-repo          # 상대 경로 저장소 추가
  bd repo list                       # 구성된 모든 저장소 표시
  bd repo remove ~/beads-planning    # 경로로 제거
  bd repo sync                       # 구성된 모든 저장소에서 동기화

```
bd repo [flags]
```

## bd repo add

config.yaml의 repos.additional 목록에 저장소 경로를 추가합니다.

경로는 .beads 폴더가 있는 디렉터리를 가리켜야 합니다.
절대 경로나 상대 경로를 사용할 수 있습니다(입력한 그대로 저장됨).

버전 관리되며 이 저장소의 모든 클론에서 공유되는 .beads/config.yaml을 수정합니다.

```
bd repo add <path> [flags]
```

**플래그:**

```
      --json   JSON 출력
```

## bd repo list

.beads/config.yaml에 구성된 모든 저장소를 나열합니다.

기본 저장소(항상 ".")와 hydration용으로 구성된 추가 저장소를 표시합니다.

```
bd repo list [flags]
```

**플래그:**

```
      --json   JSON 출력
```

## bd repo remove

config.yaml의 repos.additional 목록에서 저장소 경로를 제거합니다.

경로는 추가한 값과 정확히 일치해야 합니다(예: "~/foo"를 추가했다면
"/home/user/foo"가 아니라 "~/foo"를 제거해야 함).

이 명령은 제거된 저장소에서 가져와 이전에 hydrate한 이슈도 데이터베이스에서 제거합니다.

```
bd repo remove <path> [flags]
```

**플래그:**

```
      --json   JSON 출력
```

## bd repo sync

구성된 모든 추가 저장소에서 이슈를 동기화합니다.

각 추가 저장소에서 issues.jsonl을 읽어 원래 접두사와 source_repo 설정을 유지한 채
기본 데이터베이스로 가져옵니다. mtime 캐시를 사용해 JSONL이 변경되지 않은 저장소를
건너뜁니다.

원격이 구성되어 있으면 Dolt push/pull도 트리거합니다.

```
bd repo sync [flags]
```

**플래그:**

```
      --json      JSON 출력
      --verbose   상세한 동기화 진행 상황 표시
```
