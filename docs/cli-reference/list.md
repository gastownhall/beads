---
title: "bd list"
description: "이슈를 나열합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc list`에서 생성되었습니다.

이슈를 나열합니다

```
bd list [flags]
```

**플래그:**

```
      --all                          닫힌 이슈를 포함해 모든 이슈 표시(기본 필터 재정의)
  -a, --assignee string              담당자로 필터링
      --closed-after string          지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string         지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string         지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string        지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --defer-after string           지정 날짜 이후 연기된 이슈 필터링(상대값 지원: +6h, tomorrow)
      --defer-before string          지정 날짜 이전 연기된 이슈 필터링(상대값 지원: +6h, tomorrow)
      --deferred                     defer_until이 설정된 이슈만 표시
      --desc-contains string         설명 부분 문자열로 필터링(대소문자 구분 안 함)
      --due-after string             지정 날짜 이후 마감인 이슈 필터링(상대값 지원: +6h, tomorrow)
      --due-before string            지정 날짜 이전 마감인 이슈 필터링(상대값 지원: +6h, tomorrow)
      --empty-description            설명이 비어 있거나 없는 이슈 필터링
      --exclude-label strings        지정 레이블 중 하나라도 있는 이슈 제외
      --exclude-type strings         결과에서 이슈 유형 제외(쉼표로 구분하거나 반복 가능, 예: --exclude-type=convoy,epic)
      --flat                         트리 형식을 비활성화하고 기존 평면 목록 출력 사용
      --format string                출력 형식: 'digraph'(golang.org/x/tools/cmd/digraph용), 'dot'(Graphviz) 또는 Go 템플릿
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
      --id string                    특정 이슈 ID로 필터링(쉼표로 구분, 예: bd-1,bd-5,bd-10)
      --include-gates                출력에 gate 이슈 포함(일반적으로 숨김)
      --include-infra                출력에 인프라 beads(agent/role/message) 포함
      --include-templates            출력에 템플릿 molecule 포함
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
      --label-pattern string         레이블 glob 패턴으로 필터링(예: 'tech-*'는 tech-debt, tech-legacy와 일치)
      --label-regex string           레이블 정규식 패턴으로 필터링(예: 'tech-(debt|legacy)')
  -n, --limit int                    결과 수 제한(기본값 50, 무제한은 0 사용)(기본값 50)
      --long                         각 이슈의 상세한 여러 줄 출력 표시
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --mol-type string              molecule 유형으로 필터링: swarm, patrol 또는 work
      --no-assignee                  담당자가 없는 이슈 필터링
      --no-labels                    레이블이 없는 이슈 필터링
      --no-pager                     페이저 출력 비활성화
      --no-parent                    하위 이슈 제외(최상위 이슈만 표시)
      --no-pinned                    고정된 이슈 제외
      --notes-contains string        메모 부분 문자열로 필터링(대소문자 구분 안 함)
      --offset int                   처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --overdue                      due_at이 과거인 이슈만 표시(닫힌 이슈 제외)
      --parent string                상위 이슈 ID로 필터링(지정한 이슈의 하위 이슈 표시)
      --pinned                       고정된 이슈만 표시
      --pretty                       상태/우선순위 기호가 있는 트리 형식으로 이슈 표시
  -p, --priority string              우선순위(0-4 또는 P0-P4, 0이 가장 높음)
      --priority-max string          최대 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --priority-min string          최소 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --ready                        준비된 이슈만 표시(활성 차단 요소 없음, bd ready와 같은 의미)
  -r, --reverse                      정렬 순서 반전
      --skip-labels                  레이블 로드를 건너뜁니다. 실제 레이블과 관계없이 출력의 labels 필드가 비어 있습니다. 호출자가 레이블 데이터에 의존하지 않을 때만 사용하세요. --label, --label-any, --label-pattern, --label-regex, --exclude-label, --no-labels와 함께 사용할 수 없습니다.
      --sort string                  필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
      --spec string                  spec_id 접두사로 필터링
  -s, --status string                저장된 상태로 필터링(open, in_progress, blocked, deferred, closed). 여러 값은 쉼표로 구분: --status open,in_progress. 참고: -s/--status를 반복하면 이전 값을 알림 없이 덮어씁니다. 여러 상태를 필터링할 때는 항상 쉼표 구분 형식을 사용하세요.
      --title string                 제목 텍스트로 필터링(대소문자 구분 없는 부분 문자열 일치)
      --title-contains string        제목 부분 문자열로 필터링(대소문자 구분 안 함)
      --tree                         계층형 트리 형식(기본값: true, 비활성화하려면 --flat 사용)(기본값 true)
  -t, --type string                  유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate, convoy). 별칭: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision
      --updated-after string         지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string        지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
  -w, --watch                        변경을 감시하고 표시 자동 업데이트(--pretty 포함)
      --wisp-type string             wisp 유형으로 필터링: heartbeat, ping, patrol, gc_report, recovery, error, escalation
```
