---
title: 레이블
description: 횡단 관심사, 필터링 및 이슈의 운영 상태 캐싱을 위한 유연한 태그 지정
---

레이블은 구조화된 필드(상태, 우선순위, 유형)를 넘어 이슈를 유연하고 다차원적으로 분류합니다. 스키마를 변경하지 않고 횡단 관심사, 기술 메타데이터, 컨텍스트 태그에 레이블을 사용하세요.

## 설계 철학

**레이블과 구조화된 필드를 사용하는 시점:**

- **구조화된 필드**(상태, 우선순위, 유형) → 핵심 워크플로 상태
  - 상태: 워크플로에서 이슈의 위치(`open`, `in_progress`, `blocked`, `closed`)
  - 우선순위: 긴급도(0~4)
  - 유형: 작업 종류(`bug`, `feature`, `task`, `epic`, `chore`)

- **레이블** → 그 밖의 모든 항목
  - 기술 메타데이터(`backend`, `frontend`, `api`, `database`)
  - 도메인/범위(`auth`, `payments`, `search`, `analytics`)
  - 작업량 추정(`small`, `medium`, `large`)
  - 품질 Gate(`needs-review`, `needs-tests`, `breaking-change`)
  - 팀/소유권(`team-infra`, `team-product`)
  - 릴리스 추적(`v1.0`, `v2.0`, `backport-candidate`)

## 빠른 시작

```bash
# 이슈 생성 시 레이블 추가
bd create "인증 버그 수정" -t bug -p 1 -l auth,backend,urgent

# 기존 이슈에 레이블 추가
bd label add bd-42 security
bd label add bd-42 breaking-change

# 여러 레이블을 한 번에 추가(쉼표로 구분, 쉼표 주변에 공백 없음)
bd label add bd-42 security,breaking-change

# 이슈 레이블 나열
bd label list bd-42

# 레이블 제거
bd label remove bd-42 urgent

# 여러 레이블을 한 번에 제거
bd label remove bd-42 urgent,needs-review

# 사용 중인 모든 레이블 나열
bd label list-all

# 레이블로 필터링(AND - 모두 있어야 함)
bd list --label backend,auth

# 레이블로 필터링(OR - 하나 이상 있어야 함)
bd list --label-any frontend,backend

# 필터 조합
bd list --status open --priority 1 --label security
```

## 일반적인 레이블 패턴

### 1. 기술 구성 요소 레이블

시스템의 어느 부분인지 식별합니다.
```bash
backend
frontend
api
database
infrastructure
cli
ui
mobile
```

**예시:**
```bash
bd create "GraphQL 엔드포인트 추가" -t feature -p 2 -l backend,api
bd create "로그인 양식 업데이트" -t task -p 2 -l frontend,auth,ui
```

### 2. 도메인/기능 영역

비즈니스 도메인별로 그룹화합니다.
```bash
auth
payments
search
analytics
billing
notifications
reporting
admin
```

**예시:**
```bash
bd list --label payments --status open  # 열린 결제 이슈 모두
bd list --label-any auth,security       # 보안 관련 작업
```

### 3. 규모/작업량 추정

빠른 작업량 지표:
```bash
small     # 1일 미만
medium    # 1~3일
large     # 3일 초과
```

**예시:**
```bash
# 작고 빠르게 끝낼 작업 찾기
bd ready --json | jq '.[] | select(.labels[] == "small")'
```

### 4. 품질 Gate

닫기 전에 필요한 항목을 추적합니다.
```bash
needs-review
needs-tests
needs-docs
breaking-change
```

**예시:**
```bash
bd label add bd-42 needs-review
bd list --label needs-review --status in_progress
```

### 5. 릴리스 관리

대상 릴리스를 추적합니다.
```bash
v1.0
v2.0
backport-candidate
release-blocker
```

**예시:**
```bash
bd list --label v1.0 --status open    # v1.0에 남은 항목은?
bd label add bd-42 release-blocker
```

### 6. 팀/소유권

소유권 또는 관심사를 표시합니다.
```bash
team-infra
team-product
team-mobile
needs-triage
help-wanted
```

**예시:**
```bash
bd list --assignee alice --label team-infra
bd create "캐시 메모리 누수" -t bug -p 1 -l team-infra,help-wanted
```

### 7. 특수 마커

프로세스 또는 워크플로 플래그:
```bash
auto-generated     # 자동화로 생성
discovered-from    # 다른 작업 중 발견(의존성 유형이기도 함)
technical-debt
good-first-issue
duplicate
wontfix
```

**예시:**
```bash
bd create "TODO: 파서 리팩터링" -t chore -p 3 -l technical-debt,auto-generated
```

## 레이블로 필터링

### AND 필터링(--label)
지정한 레이블이 모두 있어야 합니다.

```bash
# backend와 urgent가 모두 있는 이슈
bd list --label backend,urgent

# 검토와 테스트가 모두 필요한 열린 버그
bd list --status open --type bug --label needs-review,needs-tests
```

### OR 필터링(--label-any)
지정한 레이블 중 하나 이상이 있어야 합니다.

```bash
# frontend 또는 backend 이슈
bd list --label-any frontend,backend

# 보안 또는 인증 관련
bd list --label-any security,auth
```

### AND/OR 조합
복잡한 쿼리에서는 두 필터를 함께 사용합니다.

```bash
# urgent 또는 blocker인 backend 이슈
bd list --label backend --label-any urgent,release-blocker

# 구성 요소와 관계없이 검토와 테스트가 모두 필요한 frontend 작업
bd list --label needs-review,needs-tests --label-any frontend,ui,mobile
```

## 워크플로 예시

### 분류 워크플로
```bash
# 분류되지 않은 이슈 생성
bd create "로그인 시 충돌" -t bug -p 1 -l needs-triage

# 분류 중 컨텍스트 추가
bd label add bd-42 auth
bd label add bd-42 backend
bd label add bd-42 urgent
bd label remove bd-42 needs-triage

# 분류되지 않은 이슈 찾기
bd list --label needs-triage
```

### 품질 Gate 워크플로
```bash
# 작업 시작
bd update bd-42 --claim

# 품질 요구 사항 표시
bd label add bd-42 needs-tests
bd label add bd-42 needs-docs

# 닫기 전에 확인
bd label list bd-42
# ... 테스트와 문서 작성 ...
bd label remove bd-42 needs-tests
bd label remove bd-42 needs-docs

# Gate가 충족되면 닫기
bd close bd-42
```

### 릴리스 계획
```bash
# v1.0 대상 이슈에 태그 지정
bd label add bd-42 v1.0
bd label add bd-43 v1.0
bd label add bd-44 v1.0

# v1.0 진행 상황 추적
bd list --label v1.0 --status closed    # 완료
bd list --label v1.0 --status open      # 남음
bd stats  # 전체 진행 상황

# 중요 항목 표시
bd label add bd-45 v1.0
bd label add bd-45 release-blocker
```

### 구성 요소 기반 작업 분배
```bash
# 백엔드 팀이 작업 가져오기
bd ready --json | jq '.[] | select(.labels[]? == "backend")'

# 프런트엔드 팀이 작은 작업 찾기
bd list --status open --label frontend,small

# 새 기여자용 help-wanted 항목 찾기
bd list --label help-wanted,good-first-issue
```

## 레이블 관리

### 레이블 나열
```bash
# 특정 이슈의 레이블
bd label list bd-42

# 데이터베이스의 모든 레이블과 사용 횟수
bd label list-all

# 스크립트용 JSON 출력
bd label list-all --json
```

출력:
```json
[
  {"label": "auth", "count": 5},
  {"label": "backend", "count": 12},
  {"label": "frontend", "count": 8}
]
```

### 일괄 작업

생성 중 레이블을 일괄 추가합니다.
```bash
bd create "이슈" -l label1,label2,label3
```

여러 이슈에 레이블을 추가하는 스크립트:
```bash
# 모든 in_progress 이슈에 "needs-review" 추가
bd list --status in_progress --json | jq -r '.[].id' | while read id; do
  bd label add "$id" needs-review
done
```

여러 이슈에서 레이블을 제거합니다.
```bash
# 닫힌 이슈에서 "urgent" 제거
bd list --status closed --label urgent --json | jq -r '.[].id' | while read id; do
  bd label remove "$id" urgent
done
```

## Git 워크플로와 통합

레이블은 Dolt 데이터베이스에 저장되며 모든 이슈 데이터와 함께 자동으로 동기화됩니다.

```bash
# 변경
bd create "버그 수정" -l backend,urgent
bd label add bd-42 needs-review

# 변경 사항은 Dolt 기록에 자동 커밋
# 준비되면 원격과 동기화:
bd dolt push

# 변경 사항을 풀한 후:
bd dolt pull
bd list --label backend  # 레이블을 포함한 최신 데이터
```

## Markdown 가져오기/내보내기

Markdown에서 가져올 때 레이블이 보존됩니다.

```markdown
# 인증 버그 수정

### 유형
bug

### 우선순위
1

### 레이블
auth, backend, urgent, needs-review

### 설명
최근 배포 후 사용자가 로그인할 수 없습니다.
```

```bash
bd create -f issue.md
# 네 레이블이 모두 있는 이슈 생성
```

## 모범 사례

### 1. 규칙을 일찍 수립
팀의 레이블 분류 체계를 문서화합니다.
```bash
# 프로젝트 README 또는 CONTRIBUTING.md에 추가
- 소문자와 하이픈 구분 사용(예: `good-first-issue`)
- 팀 레이블에 접두사 사용(예: `team-infra`, `team-product`)
- 일관된 규모 레이블 사용(`small`, `medium`, `large`)
```

### 2. 레이블을 과도하게 사용하지 않기
레이블은 유연하지만 너무 많으면 혼란을 일으킬 수 있습니다. 다음을 권장합니다.
- 핵심 기술 레이블 5~10개(`backend`, `frontend`, `api` 등)
- 프로젝트당 도메인 레이블 3~5개
- 표준 프로세스 레이블(`needs-review`, `needs-tests`)
- 필요한 릴리스 레이블

### 3. 사용하지 않는 레이블 정리
주기적으로 검토합니다.
```bash
bd label list-all
# 이슈에서 오래된 레이블 제거
```

### 4. 검색이 아니라 필터링에 레이블 사용
레이블은 자유 텍스트 검색이 아니라 분류에 사용합니다.
- ✅ 좋음: `backend`, `auth`, `urgent`
- ❌ 나쁨: `fix-the-login-bug`, `john-asked-for-this`

### 5. 의존성과 조합
레이블 + 의존성 = 강력한 구성:
```bash
# 레이블이 있는 하위 작업을 포함한 Epic
bd create "인증 시스템 재작성" -t epic -p 1 -l auth,v2.0
bd create "JWT 구현" -t task -p 1 -l auth,backend --deps parent-child:bd-42
bd create "로그인 UI 업데이트" -t task -p 1 -l auth,frontend --deps parent-child:bd-42

# 모든 v2.0 인증 작업 찾기
bd list --label auth,v2.0
```

## AI 에이전트 사용법

레이블은 복잡한 워크플로를 관리하는 AI 에이전트에 특히 유용합니다.

```bash
# 발견한 작업에 자동 레이블 지정
bd create "auth.go에서 TODO 발견" -t task -p 2 -l auto-generated,technical-debt

# 에이전트 검토용 필터링
bd list --label needs-review --status in_progress --json

# 자동화 메타데이터 추적
bd label add bd-42 ai-generated
bd label add bd-42 needs-human-review
```

에이전트 워크플로 예시:
```bash
# 에이전트가 리팩터링 중 이슈 발견
bd create "validateToken 함수 추출" -t chore -p 2 \
  -l technical-debt,backend,auth,small \
  --deps discovered-from:bd-10

# 에이전트가 검토할 작업 표시
bd update bd-42 --claim
# ... 에이전트 작업 ...
bd label add bd-42 needs-review
bd label add bd-42 ai-generated

# 사람이 검토하고 승인
bd label remove bd-42 needs-review
bd label add bd-42 approved
bd close bd-42
```

## 상태 캐시로서의 레이블

레이블은 빠른 쿼리를 위해 운영 상태를 캐시할 수 있으므로 beads가 불변 기록(이벤트)과 현재 상태(레이블)를 모두 추적하는 패턴을 사용할 수 있습니다.

### 패턴

**규칙:** `<dimension>:<value>`

예시:
- `patrol:muted` / `patrol:active` - 순찰 억제 상태
- `mode:degraded` / `mode:normal` - 운영 모드
- `status:idle` / `status:working` - 작업자 상태
- `health:healthy` / `health:failing` - 구성 요소 상태

**구현:**
1. 이벤트 bead 생성(전체 컨텍스트, 불변 기록)
2. 역할 bead의 레이블 업데이트(현재 상태 캐시)

```bash
# 이벤트: 발생한 일과 이유의 전체 기록
bd create "순찰 음소거: 디버깅 중 사용자 요청" -t event \
  -l event-type:patrol-muted,actor:observer,reason:user-request

# 상태: 현재 상태를 반영하도록 역할 bead의 레이블 업데이트
bd label remove beads/observer patrol:active
bd label add beads/observer patrol:muted
```

**핵심 원칙:** 이벤트가 원본입니다. 레이블은 빠른 쿼리를 위한 캐시입니다.

### 이 패턴을 사용하는 이유

**이벤트 스캔 없이 빠른 쿼리:**
```bash
# 상태 레이블 없이: 현재 순찰 상태를 찾기 위해 모든 이벤트 스캔
bd list --type event | grep "patrol" | tail -1  # 느리고 취약함

# 상태 레이블 사용: 직접 쿼리
bd show beads/observer | grep "patrol:"  # 즉시
```

**기록 보존:**
```bash
# 순찰을 언제, 왜, 누가 음소거했는가?
bd list --label event-type:patrol-muted --type event
```

**상태 복구:**
```bash
# 레이블이 손상되면 이벤트에서 다시 빌드
bd list --type event --label event-type:patrol-muted | tail -1
# 그런 다음 레이블 재적용
```

### 일반적인 상태 차원

| 차원 | 값 | 사용 사례 |
|-----------|--------|----------|
| `patrol:` | `active`, `muted` | 순찰 주기 억제 |
| `mode:` | `normal`, `degraded`, `maintenance` | 운영 모드 |
| `status:` | `idle`, `working`, `blocked` | 작업자 활동 |
| `health:` | `healthy`, `warning`, `failing` | 구성 요소 상태 |
| `lock:` | `unlocked`, `locked` | 배타적 접근 제어 |

### 상태 전환

상태 레이블을 변경하기 전에 항상 이벤트를 생성합니다.

```bash
# 감사 추적과 함께 상태를 전환하는 함수
transition_state() {
  local role="$1"
  local dimension="$2"
  local old_value="$3"
  local new_value="$4"
  local reason="$5"

  # 전환 기록
  bd create "상태 변경: $dimension $old_value → $new_value" -t event \
    -l "event-type:state-change,dimension:$dimension,from:$old_value,to:$new_value"

  # 캐시 업데이트
  bd label remove "$role" "$dimension:$old_value"
  bd label add "$role" "$dimension:$new_value"
}

# 사용법
transition_state beads/observer patrol active muted "사용자 디버깅 세션"
```

### 상태 쿼리

```bash
# 역할의 현재 상태
bd label list beads/observer | grep ":"

# 특정 상태의 모든 역할
bd list --label patrol:muted

# 예상 상태가 아닌 역할
bd list --label-any mode:degraded,health:failing

# 상태 변경 기록
bd list --type event --label event-type:state-change
```

### 모범 사례

1. **네임스페이스가 지정된 차원 사용** - 모호하면 역할 유형을 접두사로 사용합니다.
2. **값 집합을 작게 유지** - 차원당 값 2~4개를 사용합니다.
3. **유효한 값 문서화** - 역할 문서에 허용되는 값을 나열합니다.
4. **항상 이벤트를 먼저 생성** - 기록 없이 레이블을 업데이트하지 않습니다.
5. **레이블을 임시로 취급** - 손상되면 이벤트에서 다시 빌드합니다.

### 향후 도우미

이 패턴은 다음 도우미 명령을 제안합니다(bd-7l67 참조).
```bash
# 현재 상태 쿼리
bd state beads/observer patrol     # → "muted"

# 자동 이벤트 생성과 함께 전환
bd set-state beads/observer patrol=active --reason "디버깅 완료"
```

도우미가 생길 때까지 위의 수동 패턴을 사용하세요.

## 고급 패턴

### 구성 요소 매트릭스
여러 차원에서 이슈를 추적합니다.
```bash
# 백엔드 + 인증 + 높은 우선순위
bd list --label backend,auth --priority 1

# 규모가 작은 모든 프런트엔드 작업
bd list --label-any frontend,ui --label small

# 모든 구성 요소의 치명적 이슈
bd list --priority 0 --label-any backend,frontend,infrastructure
```

### 스프린트 계획
```bash
# 스프린트 대상 이슈에 레이블 지정
for id in bd-42 bd-43 bd-44 bd-45; do
  bd label add "$id" sprint-12
done

# 스프린트 진행 상황 추적
bd list --label sprint-12 --status closed    # 속도
bd list --label sprint-12 --status open      # 남음
bd stats | grep "In Progress"                # 현재 WIP
```

### 기술 부채 추적
```bash
# 부채 표시
bd create "레거시 파서 리팩터링" -t chore -p 3 -l technical-debt,large

# 처리할 부채 찾기
bd list --label technical-debt --label small
bd list --label technical-debt --priority 1  # 높은 우선순위 부채
```

### 호환성을 깨는 변경 조율
```bash
# 호환성을 깨는 변경 식별
bd label add bd-42 breaking-change
bd label add bd-42 v2.0

# 다음 메이저 릴리스의 호환성을 깨는 변경 모두 찾기
bd list --label breaking-change,v2.0

# 문서화 여부 확인
bd list --label breaking-change --label needs-docs
```

## 운영 상태 패턴(캐시로서의 레이블)

오케스트레이션 시스템에서 레이블은 "역할 bead"(에이전트 또는 시스템 구성 요소를 나타내는 이슈)의 현재 운영 상태를 캐시할 수 있습니다. 이벤트 기록을 스캔하지 않고 빠르게 상태를 쿼리할 수 있습니다.

### 규칙: `<dimension>:<value>`

차원 접두사와 값 접미사를 콜론으로 구분한 레이블을 사용합니다.

```
patrol:muted      patrol:active
mode:degraded     mode:normal
status:idle       status:working
health:healthy    health:failing
```

### 패턴

1. 전체 컨텍스트가 있는 **이벤트 bead 생성**(불변, 감사 추적)
2. 현재 상태를 반영하도록 **역할 bead의 레이블 업데이트**(빠른 조회)

```bash
# 1. 이벤트 기록(원본)
bd create "agent-abc 순찰 음소거" -t event \
  --parent agent-abc \
  -d "이유: 멈춘 작업자 조사. 예상 시간: 30분"

# 2. 캐시된 상태 레이블 업데이트
bd label remove agent-abc patrol:active
bd label add agent-abc patrol:muted
```

### 이 패턴을 사용하는 이유

**이벤트가 원본이고 레이블은 캐시입니다.**

| 접근 방식 | 이벤트만 사용 | 레이블을 캐시로 사용 |
|----------|-------------|-----------------|
| 현재 상태 쿼리 | 모든 이벤트를 스캔해 최신 항목 찾기 | `bd list --label patrol:muted` |
| 상태 기록 쿼리 | 자연스러움(모든 이벤트 존재) | 이벤트 쿼리 |
| 감사 추적 | 완전함 | 완전함(이벤트가 계속 존재) |
| 성능 | O(n) 이벤트 | O(1) 레이블 조회 |

이 패턴은 이벤트를 통한 전체 기록과 레이블을 통한 빠른 쿼리를 모두 제공합니다.

### 예시: 에이전트 역할 상태

```bash
# 에이전트용 역할 bead 생성
bd create "witness-alpha" -t role -l patrol:active,mode:normal,health:healthy

# 에이전트가 성능 저하 모드 진입
bd create "성능 저하: 높은 오류율" -t event --parent witness-alpha \
  -d "오류율이 5%를 초과했습니다. 폴링 빈도를 줄입니다."
bd label remove witness-alpha mode:normal
bd label add witness-alpha mode:degraded

# 현재 상태 쿼리
bd list --label mode:degraded --type role  # 성능이 저하된 모든 역할

# 에이전트 복구
bd create "복구됨: 오류율 정상" -t event --parent witness-alpha
bd label remove witness-alpha mode:degraded
bd label add witness-alpha mode:normal
```

### 일반적인 차원

| 차원 | 값 | 사용 사례 |
|-----------|--------|----------|
| `patrol` | `active`, `muted`, `suspended` | 에이전트 순찰 주기 |
| `mode` | `normal`, `degraded`, `maintenance` | 운영 모드 |
| `status` | `idle`, `working`, `blocked` | 작업 상태 |
| `health` | `healthy`, `warning`, `failing` | 상태 검사 |
| `sync` | `current`, `stale`, `syncing` | 동기화 상태 |

### 모범 사례

1. **항상 이벤트를 먼저 생성** - 레이블은 캐시이고 이벤트가 원본입니다.
2. **새 값을 추가하기 전에 이전 값 제거** - dimension:value1 + dimension:value2 충돌을 방지합니다.
3. **일관된 차원 이름 사용** - 팀 규칙을 일찍 수립합니다.
4. **차원을 직교하게 유지** - patrol과 mode는 독립된 관심사입니다.

### 상태 쿼리

```bash
# 음소거된 모든 순찰 찾기
bd list --label patrol:muted

# 정상 모드의 정상 에이전트 찾기
bd list --label health:healthy,mode:normal

# 정상 상태가 아닌 에이전트 찾기
bd list --label-any health:warning,health:failing

# 특정 역할의 상태 가져오기
bd label list witness-alpha
# Output: patrol:active, mode:normal, health:healthy
```

### 도우미 명령

편의를 위해 다음 도우미를 사용합니다.

```bash
# 특정 차원 쿼리
bd state witness-alpha patrol
# Output: active

# 모든 상태 차원 나열
bd state list witness-alpha
# Output:
#   patrol: active
#   mode: normal
#   health: healthy

# 상태 설정(이벤트 생성 + 레이블 원자적 업데이트)
bd set-state witness-alpha patrol=muted --reason "이슈 조사 중"
```

`set-state` 명령은 다음을 원자적으로 수행합니다.
1. 이유가 있는 이벤트 bead 생성(원본)
2. 이전 차원 레이블이 있으면 제거
3. 새 dimension:value 레이블 추가(캐시)

전체 명령 참조는 [bd set-state](/cli-reference/set-state)를 참조하세요.

## 문제 해결

### 목록에 레이블이 표시되지 않음
레이블은 명시적으로 가져와야 합니다. `bd list` 명령은 사람이 읽는 출력에서 이슈를 표시하지만 레이블은 표시하지 않습니다(JSON에만 표시).

```bash
# JSON에서 레이블 보기
bd list --json | jq '.[] | {id, labels}'

# 특정 이슈의 레이블 보기
bd show bd-42 --json | jq '.labels'
bd label list bd-42
```

### 레이블 필터링이 작동하지 않음
레이블 이름이 정확히 일치하는지 확인하세요(대소문자 구분).
```bash
# 다음은 서로 다른 레이블:
bd label add bd-42 Backend    # 대문자 B
bd list --label backend       # 일치하지 않음

# 정확한 이름을 보려면 모든 레이블 나열
bd label list-all
```

### 레이블 동기화
레이블은 Dolt 데이터베이스에 저장됩니다. 레이블이 동기화되지 않은 것 같으면 다음을 실행합니다.
```bash
# Dolt 원격에서 풀
bd dolt pull

# 또는 doctor를 실행해 진단
bd doctor
```

## 함께 보기

- [README.md](https://github.com/gastownhall/beads/blob/main/README.md) - 주요 문서
- [AGENTS.md](https://github.com/gastownhall/beads/blob/main/AGENTS.md) - AI 에이전트 통합 가이드
- [고급 기능](/reference/advanced) - 고급 기능 및 구성
