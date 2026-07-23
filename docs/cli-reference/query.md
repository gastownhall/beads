---
title: "bd query"
description: "복합 필터, 불리언 연산자, 날짜 상대 표현식을 지원하는 간단한 쿼리 언어로 이슈를 조회합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc query`에서 생성되었습니다.

복합 필터, 불리언 연산자, 날짜 상대 표현식을 지원하는 간단한 쿼리 언어로
이슈를 조회합니다.

이 쿼리 언어를 사용하면 여러 플래그나 jq 파이프가 필요할 복잡한 필터링을
수행할 수 있습니다.

구문:
  field=value       같음 비교
  field!=value      같지 않음 비교
  field&gt;value       초과
  field&gt;=value      이상
  field&lt;value       미만
  field&lt;=value      이하

불리언 연산자(대소문자 구분 안 함):
  expr AND expr     두 조건이 모두 일치해야 함
  expr OR expr      한 조건이라도 일치 가능
  NOT expr          조건 부정
  (expr)            괄호로 그룹화

지원 필드:
  status            저장된 상태(open, in_progress, blocked, deferred, closed). 참고: 의존성으로 차단된 이슈는 "open"을 유지하며, 찾으려면 'bd blocked' 사용
  priority          우선순위 수준(0-4)
  type              이슈 유형(bug, feature, task, epic, chore, decision)
  assignee          담당 사용자(미지정은 "none" 사용)
  owner             이슈 소유자
  label             이슈 레이블(레이블 없음은 "none" 사용)
  title             제목에서 검색(포함)
  description       설명에서 검색(포함, 비어 있음은 "none")
  notes             메모에서 검색(포함)
  created           생성 날짜/시간
  updated           마지막 업데이트 날짜/시간
  started           이슈가 처음 in_progress로 전환된 날짜/시간
  closed            닫은 날짜/시간
  id                이슈 ID(와일드카드 지원: bd-*)
  spec              Spec ID(와일드카드 지원)
  pinned            불리언(true/false)
  ephemeral         불리언(true/false)
  template          불리언(true/false)
  parent            상위 이슈 ID
  mol_type          molecule 유형(swarm, patrol, work)

날짜 값:
  상대 기간: 7d(7일 전), 24h(24시간 전), 2w(2주 전)
  절대 날짜: 2025-01-15, 2025-01-15T10:00:00Z
  자연어: tomorrow, "next monday", "in 3 days"

예시:
  bd query "status=open AND priority&gt;1"
  bd query "status=open AND priority&lt;=2 AND updated&gt;7d"
  bd query "(status=open OR status=blocked) AND priority&lt;2"
  bd query "type=bug AND label=urgent"
  bd query "NOT status=closed"
  bd query "assignee=none AND type=task"
  bd query "created&gt;30d AND status!=closed"
  bd query "label=frontend OR label=backend"
  bd query "title=authentication AND priority=0"

```
bd query [expression] [flags]
```

**플래그:**

```
  -a, --all           닫힌 이슈 포함(기본값: 닫힌 이슈 제외)
  -n, --limit int     결과 수 제한(기본값: 50, 0 = 무제한)(기본값 50)
      --long          각 이슈의 상세한 여러 줄 출력 표시
      --offset int    처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --parse-only    쿼리만 구문 분석하고 AST 표시(디버깅용)
  -r, --reverse       정렬 순서 반전
      --sort string   필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
```
