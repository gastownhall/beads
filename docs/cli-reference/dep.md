---
title: "bd dep"
description: "이슈 간 의존성을 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc dep`에서 생성되었습니다.

이슈 간 의존성을 관리합니다.

이슈 ID와 --blocks 플래그로 호출하면 차단 의존성을 생성합니다:
  bd dep &lt;blocker-id&gt; --blocks &lt;blocked-id&gt;

다음과 같습니다:
  bd dep add &lt;blocked-id&gt; &lt;blocker-id&gt;

예시:
  bd dep bd-xyz --blocks bd-abc    # bd-xyz가 bd-abc를 차단
  bd dep add bd-abc bd-xyz         # 위와 동일(bd-abc가 bd-xyz에 의존)

```
bd dep [issue-id] [flags]
```

**플래그:**

```
  -b, --blocks string    이 이슈가 차단하는 이슈 ID(단축 명령: bd dep add <blocked> <blocker>)
      --no-cycle-check   속도를 위해 간선별 순환 검사 건너뛰기(일괄 연결), 일괄 --file 추가는 커밋 전 마지막 전체 그래프 검사를 계속 실행
```

## bd dep add

두 이슈 간에 의존성을 추가합니다.

depends-on-id는 다음과 같이 제공할 수 있습니다:
  - 위치 인수: bd dep add issue-123 issue-456
  - 플래그: bd dep add issue-123 --blocked-by issue-456
  - 플래그: bd dep add issue-123 --depends-on issue-456

--blocked-by와 --depends-on 플래그는 별칭이며 둘 다 "issue-123이 지정 이슈에
의존함(차단됨)"을 의미합니다.

depends-on-id로 다음을 사용할 수 있습니다:
  - 로컬 이슈 ID(예: bd-xyz)
  - 외부 참조: external:&lt;project&gt;:&lt;capability&gt;

일괄 연결에는 --file로 줄바꿈 구분 JSON을 전달하세요. 각 줄은 "from"과 "to" 필드가
있는 객체여야 하며 "type"을 포함할 수 있습니다. "issue_id"와 "depends_on_id" 별칭도
허용됩니다. stdin에서 읽으려면 --file -를 사용하세요.

외부 참조는 그대로 저장되고 쿼리 시 external_projects 구성을 사용해 해석됩니다.
대상 프로젝트에서 기능이 "shipped"될 때까지 이슈를 차단합니다.

예시:
  bd dep add bd-42 bd-41                              # 위치 인수
  bd dep add bd-42 --blocked-by bd-41                 # 플래그 구문(같은 효과)
  bd dep add bd-42 --depends-on bd-41                 # 별칭(같은 효과)
  bd dep add gt-xyz external:beads:mol-run-assignee   # 프로젝트 간 의존성
  bd dep add bd-42 bd-41 --no-cycle-check             # 순환 검사 건너뛰기(일괄 연결)
  bd dep add --file deps.jsonl                        # 일괄 JSONL: &#123;"from":"bd-42","to":"bd-41"&#125;

```
bd dep add [issue-id] [depends-on-id] [flags]
```

**플래그:**

```
      --blocked-by string   첫 이슈를 차단하는 이슈 ID(위치 인수 대신 사용)
      --depends-on string   첫 이슈가 의존하는 이슈 ID(--blocked-by의 별칭)
      --file string         JSONL 파일에서 의존성 간선 읽기, stdin은 '-'
      --no-cycle-check      속도를 위해 간선별 순환 검사 건너뛰기(일괄 연결), 일괄 --file 추가는 커밋 전 마지막 전체 그래프 검사를 계속 실행
  -t, --type string         의존성 유형(blocks|tracks|related|parent-child|discovered-from|until|caused-by|validates|relates-to|supersedes)(기본값 "blocks")
```

## bd dep cycles

의존성 순환을 감지합니다

```
bd dep cycles [flags]
```

## bd dep list

선택적 유형 필터링을 사용해 하나 이상 이슈의 의존 항목 또는 종속 항목을 나열합니다.

기본적으로 의존 항목(이슈가 의존하는 대상)을 표시합니다. --direction으로 제어합니다:
  - down: 의존 항목(이 이슈가 의존하는 대상) 표시 - 기본값
  - up:   종속 항목(이 이슈에 의존하는 대상) 표시

일괄 dep 목록에 여러 ID를 제공할 수 있습니다. --json 사용 시 출력은 요청한 모든
이슈의 의존성 레코드로 구성된 평면 배열입니다.

의존성 유형(예: tracks, blocks, parent-child)으로 필터링하려면 --type을 사용하세요.

예시:
  bd dep list gt-abc                     # gt-abc가 의존하는 대상 표시
  bd dep list gt-abc gt-def              # 일괄: 두 이슈의 의존성
  bd dep list gt-abc --direction=up      # gt-abc에 의존하는 대상 표시
  bd dep list gt-abc --direction=up -t tracks  # gt-abc를 추적하는 대상 표시(convoy 추적)

```
bd dep list [issue-id...] [flags]
```

**플래그:**

```
      --direction string   방향: 'down'(의존 항목), 'up'(종속 항목)(기본값 "down")
  -t, --type string        의존성 유형으로 필터링(예: tracks, blocks, parent-child)
```

## bd dep relate

두 이슈 간 느슨한 '함께 보기' 관계를 생성합니다.

relates_to 링크는 양방향이며 두 이슈가 서로 참조합니다.
차단이나 계층 없이 지식 그래프를 연결할 수 있습니다.

예시:
  bd relate bd-abc bd-xyz    # 관련 이슈 두 개 연결
  bd relate bd-123 bd-456    # 함께 보기 연결 생성

```
bd dep relate <id1> <id2> [flags]
```

## bd dep remove

의존성을 제거합니다

```
bd dep remove [issue-id] [depends-on-id] [flags]
```

**별칭:** rm

## bd dep tree

지정한 이슈를 루트로 하는 의존성 트리를 표시합니다.

기본적으로 의존 항목(이 이슈를 차단하는 대상)을 표시합니다. --direction으로 제어합니다:
  - down: 의존 항목(이 이슈를 차단하는 대상) 표시 - 기본값
  - up:   종속 항목(이 이슈가 차단하는 대상) 표시
  - both: 양방향 전체 그래프 표시

예시:
  bd dep tree gt-0iqq                    # gt-0iqq를 차단하는 대상 표시
  bd dep tree gt-0iqq --direction=up     # gt-0iqq가 차단하는 대상 표시
  bd dep tree gt-0iqq --status=open      # 열린 이슈만 표시
  bd dep tree gt-0iqq --depth=3          # 깊이를 3단계로 제한

```
bd dep tree [issue-id] [flags]
```

**플래그:**

```
      --direction string   트리 방향: 'down'(의존 항목), 'up'(종속 항목) 또는 'both'
      --format string      출력 형식: Mermaid.js 순서도용 'mermaid'
  -d, --max-depth int      표시할 최대 트리 깊이(안전 제한)(기본값 50)
      --reverse            종속 트리 표시(사용 중단: --direction=up 사용)
      --show-all-paths     노드로 향하는 모든 경로 표시(다이아몬드 의존성 중복 제거 안 함)
      --status string      이 상태의 이슈만 표시하도록 필터링(open, in_progress, blocked, deferred, closed)
```

## bd dep unrelate

두 이슈 간 relates_to 관계를 제거합니다.

양방향 링크를 제거합니다.

예시:
  bd unrelate bd-abc bd-xyz

```
bd dep unrelate <id1> <id2> [flags]
```
