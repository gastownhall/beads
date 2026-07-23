---
title: "bd linear"
description: "beads와 Linear 간에 이슈를 동기화합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc linear`에서 생성되었습니다.

beads와 Linear 간에 이슈를 동기화합니다.

구성:
  bd config set linear.api_key "YOUR_API_KEY"
  bd config set linear.team_id "TEAM_ID"
  bd config set linear.team_ids "TEAM_ID1,TEAM_ID2"  # 여러 팀(쉼표로 구분)
  bd config set linear.project_id "PROJECT_ID"  # 선택 사항: 이 프로젝트만 동기화

환경 변수(구성 대신 사용):
  LINEAR_API_KEY  - Linear API 키(개별 개발자용)
  LINEAR_TEAM_ID  - Linear 팀 ID(UUID, 단일)
  LINEAR_TEAM_IDS - Linear 팀 ID(쉼표로 구분한 UUID)

OAuth(CI 워커/자동 동기화용):
  LINEAR_OAUTH_CLIENT_ID     - OAuth 앱 클라이언트 ID
  LINEAR_OAUTH_CLIENT_SECRET - OAuth 앱 클라이언트 시크릿

  두 OAuth 환경 변수를 모두 설정하면 API 키 대신 OAuth client_credentials 흐름을
  사용합니다. 이를 통해 CI 워커가 사용자를 가장하지 않고 애플리케이션
  (actor=application)으로 인증할 수 있습니다.
  우선순위: OAuth &gt; LINEAR_API_KEY &gt; 구성 파일.

데이터 매핑(선택 사항, 적절한 기본값 제공):
  우선순위 매핑(Linear 0-4를 Beads 0-4로):
    bd config set linear.priority_map.0 4    # 우선순위 없음 -&gt; 백로그
    bd config set linear.priority_map.1 0    # 긴급 -&gt; 치명적
    bd config set linear.priority_map.2 1    # 높음 -&gt; 높음
    bd config set linear.priority_map.3 2    # 중간 -&gt; 중간
    bd config set linear.priority_map.4 3    # 낮음 -&gt; 낮음

  상태 매핑(Linear 상태 유형을 Beads 상태로):
    bd config set linear.state_map.backlog open
    bd config set linear.state_map.unstarted open
    bd config set linear.state_map.started in_progress
    bd config set linear.state_map.completed closed
    bd config set linear.state_map.canceled closed
    bd config set linear.state_map.my_custom_state in_progress  # 사용자 정의 상태 이름

  레이블과 이슈 유형 간 매핑:
    bd config set linear.label_type_map.bug bug
    bd config set linear.label_type_map.feature feature
    bd config set linear.label_type_map.epic epic

  관계 유형 매핑(Linear 관계를 Beads 의존성으로):
    bd config set linear.relation_map.blocks blocks
    bd config set linear.relation_map.blockedBy blocks
    bd config set linear.relation_map.duplicate duplicates
    bd config set linear.relation_map.related related

  ID 생성(선택 사항, bd/Jira 해시 모드와 일치하는 해시 ID):
    bd config set linear.id_mode "hash"      # hash(기본값)
    bd config set linear.hash_length "6"     # 해시 길이 3-8(기본값: 6)

예시:
  bd linear sync --pull         # Linear에서 이슈 가져오기
  bd linear sync --push         # Linear로 이슈 내보내기
  bd linear sync                # 양방향 동기화(pull 후 push)
  bd linear sync --dry-run      # 변경 없이 동기화 미리 보기
  bd create "로그인 수정" --external-ref https://linear.app/team/issue/TEAM-123
                              # 로컬 이슈를 기존 Linear 이슈에 연결
  bd linear status              # 동기화 상태 표시

```
bd linear [flags]
```

## bd linear pull

Linear에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd linear sync --pull --issues &lt;refs&gt;

```
bd linear pull [refs...] [flags]
```

**플래그:**

```
      --dry-run     변경하지 않고 pull 미리 보기
      --relations   pull할 때 Linear 관계를 bd 의존성으로 가져오기
```

## bd linear push

하나 이상의 beads 이슈를 Linear로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd linear sync --push --issues &lt;ids&gt;

```
bd linear push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd linear status

다음을 포함한 현재 Linear 동기화 상태를 표시합니다:
  - 마지막 동기화 타임스탬프
  - 구성 상태
  - Linear 링크가 있는 이슈 수
  - push 대기 중인 이슈(external_ref 없음)

```
bd linear status [flags]
```

## bd linear sync

beads와 Linear 간에 이슈를 동기화합니다.

모드:
  --pull              Linear에서 beads로 이슈 가져오기
  --push              beads에서 Linear로 이슈 내보내기
  --pull-if-stale     데이터가 오래된 경우에만 pull(최신이면 건너뜀)
  (플래그 없음)       양방향 동기화: 충돌을 해결하며 pull 후 push

오래됨 판단(--pull-if-stale):
  --threshold 20m     pull하기 전에 데이터가 경과해야 하는 시간(기본값 20m)
  5분 디바운스로 에이전트 루프를 방지합니다. 5분 이내에 pull이 완료되었다면
  임곗값과 관계없이 데이터를 항상 최신으로 간주합니다.

팀 선택:
  --team ID1,ID2  이 동기화에 구성된 팀 ID 재정의
  linear.team_ids로 여러 팀을 구성할 수 있습니다(쉼표로 구분).
  이전 버전과의 호환성을 위해 linear.team_id로 대체됩니다.
  여러 팀이 구성된 경우 push에 명시적인 --team이 필요합니다.

pull 옵션:
  --milestones       Linear 프로젝트 마일스톤을 로컬 epic 상위 이슈로 재구성

유형 필터링(--push 전용):
  --type task,feature       해당 유형의 이슈만 동기화
  --exclude-type wisp       해당 유형의 이슈 제외
  --include-ephemeral       임시 이슈(wisp 등) 포함. 기본값은 제외
  --parent TICKET           이 티켓과 하위 항목만 push
  --relations               pull 시 Linear 관계를 bd 의존성으로 가져오기

충돌 해결:
  기본적으로 더 최신 타임스탬프가 우선합니다. 다음으로 재정의할 수 있습니다:
  --prefer-local    항상 로컬 beads 버전 우선
  --prefer-linear   항상 Linear 버전 우선

예시:
  bd linear sync --pull                         # Linear에서 가져오기
  bd linear sync --pull-if-stale                # 데이터가 오래된 경우에만 pull
  bd linear sync --pull-if-stale --threshold 5m # 5분보다 오래된 경우 pull
  bd linear sync --pull --relations             # Linear 차단 관계를 bd 의존성으로 가져오기
  bd linear sync --push --create-only           # 새 이슈만 push
  bd linear sync --push --type=task,feature     # task와 feature만 push
  bd linear sync --push --exclude-type=wisp     # wisp를 제외하고 모두 push
  bd linear sync --push --parent=bd-abc123      # 하나의 티켓 트리 push
  bd linear sync --dry-run                      # 변경 없이 미리 보기
  bd linear sync --prefer-local                 # 양방향, 로컬 우선

```
bd linear sync [flags]
```

**플래그:**

```
      --create-only            새 이슈만 생성하고 기존 이슈는 업데이트하지 않음
      --dry-run                변경 없이 동기화 미리 보기
      --exclude-type strings   해당 유형의 이슈 제외(반복 가능)
      --include-ephemeral      Linear로 push할 때 임시 이슈(wisp 등) 포함
      --issues string          선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --milestones             pull할 때 Linear 프로젝트 마일스톤을 로컬 epic 상위 이슈로 재구성
      --no-wait                다른 동기화가 실행 중이면 기다리지 않고 즉시 실패
      --parent string          이 beads 티켓과 하위 항목으로 push 제한
      --prefer-linear          충돌 시 Linear 버전 우선
      --prefer-local           충돌 시 로컬 버전 우선
      --pull                   Linear에서 이슈 pull
      --pull-if-stale          Linear 데이터가 오래된 경우에만 pull(최신이면 건너뜀)
      --push                   Linear로 이슈 push
      --relations              pull할 때 Linear 관계를 bd 의존성으로 가져오기
      --state string           동기화할 이슈 상태: open, closed, all(기본값 "all")
      --team strings           동기화할 팀 ID(구성된 team_id/team_ids 재정의)
      --threshold duration     --pull-if-stale의 오래됨 임곗값(기본값 20m)(기본값 20m0s)
      --type strings           해당 유형의 이슈만 동기화(반복 가능)
      --update-refs            Linear 이슈 생성 후 external_ref 업데이트(기본값 true)
```

## bd linear teams

Linear API 키로 접근 가능한 모든 팀을 나열합니다.

이를 사용해 구성에 필요한 팀 ID(UUID)를 찾습니다.

예시:
  bd linear teams
  bd config set linear.team_id "12345678-1234-1234-1234-123456789abc"

```
bd linear teams [flags]
```
