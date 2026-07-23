---
title: "bd create"
description: "새 이슈를 생성합니다(Markdown/그래프 JSON에서 일괄 생성 가능)"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc create`에서 생성되었습니다.

새 이슈를 생성합니다(Markdown/그래프 JSON에서 일괄 생성 가능)

```
bd create [title] [flags]
```

**별칭:** new

**플래그:**

```
      --acceptance string       인수 기준
      --append-notes string     기존 메모에 추가(줄바꿈 구분자 포함)
  -a, --assignee string         담당자
      --body-file string        파일에서 설명 읽기(stdin은 - 사용)
      --context string          이슈의 추가 컨텍스트
      --defer string            지정 날짜까지 연기(그때까지 bd ready에서 이슈 숨김). --due와 같은 형식
      --deps strings            'type:id' 또는 'id' 형식의 의존성(예: 'discovered-from:bd-20,blocks:bd-15' 또는 'bd-20')
  -d, --description string      이슈 설명
      --design string           설계 메모
      --design-file string      파일에서 설계 읽기(stdin은 - 사용)
      --dry-run                 실제 생성하지 않고 생성될 항목 미리 보기
      --due string              마감 날짜/시간. 형식: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15
      --ephemeral               임시로 생성(단기 유지, TTL 압축 대상)
  -e, --estimate int            예상 시간(분)(예: 1시간은 60)
      --event-actor string      이 이벤트를 발생시킨 엔터티 URI(--type=event 필요)
      --event-category string   이벤트 범주(예: patrol.muted, agent.started)(--type=event 필요)
      --event-payload string    이벤트별 JSON 데이터(--type=event 필요)
      --event-target string     영향을 받은 엔터티 URI 또는 bead ID(--type=event 필요)
      --external-ref string     외부 참조(예: 'gh-9', 'jira-ABC', Linear URL)
  -f, --file string             Markdown 파일에서 여러 이슈 생성
      --force                   접두사가 데이터베이스 접두사와 일치하지 않아도 강제로 생성
      --graph string            JSON 계획 파일에서 의존성이 있는 이슈 그래프 생성
      --id string               명시적 이슈 ID(예: 파티셔닝용 'bd-42')
  -l, --labels strings          레이블(쉼표로 구분)
      --metadata string         사용자 정의 메타데이터 설정(JSON 문자열 또는 읽을 @file.json)
      --mol-type string         molecule 유형: swarm(다중 에이전트), patrol(반복 작업), work(기본값)
      --no-history              GC 대상이 되지 않게 하면서 Dolt 커밋 이력 건너뛰기(영구 에이전트 bead용)
      --no-inherit-labels       상위 이슈에서 레이블을 상속하지 않음
      --notes string            추가 메모
      --parent string           계층형 하위 이슈의 상위 이슈 ID(예: 'bd-a3f8e9')
  -p, --priority string         우선순위(0-4 또는 P0-P4, 0이 가장 높음)(기본값 "2")
      --repo string             이슈의 대상 저장소(자동 라우팅 재정의)
      --silent                  이슈 ID만 출력(스크립팅용)
      --skills string           이 이슈에 필요한 skill
      --spec-id string          사양 문서에 연결
      --stdin                   stdin에서 설명 읽기(--body-file -의 별칭)
      --title string            이슈 제목(위치 인수 대신 사용)
  -t, --type string             이슈 유형(bug|feature|task|epic|chore|decision), 사용자 정의 유형에는 types.custom 구성 필요, 별칭: enhancement/feat→feature, dec/adr→decision(기본값 "task")
      --validate                설명에 이슈 유형별 필수 섹션이 있는지 검증
      --waits-for string        기다릴 스폰 이슈 ID(fanout gate용 waits-for 의존성 생성)
      --waits-for-gate string   gate 유형: all-children(모두 기다림) 또는 any-children(첫 항목 기다림)(기본값 "all-children")
      --wisp-type string        TTL 기반 압축용 wisp 유형: heartbeat, ping, patrol, gc_report, recovery, error, escalation
```
