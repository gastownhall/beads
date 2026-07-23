---
title: "이슈와 의존성"
description: "이슈 모델: 필드, 유형, 우선순위 및 준비된 작업을 결정하는 의존성"
---

beads의 이슈 모델을 알아봅니다.

## 이슈 구조

모든 이슈에는 다음 항목이 있습니다.

```bash
bd show bd-42 --json
```

```json
{
  "id": "bd-42",
  "title": "인증 구현",
  "description": "JWT 기반 인증 추가",
  "type": "feature",
  "status": "open",
  "priority": 1,
  "labels": ["backend", "security"],
  "created_at": "2024-01-15T10:30:00Z",
  "updated_at": "2024-01-15T10:30:00Z"
}
```

## 이슈 유형

| 유형 | 사용 사례 |
|------|----------|
| `bug` | 수정해야 하는 고장 난 기능 |
| `feature` | 새로운 기능 |
| `task` | 작업 항목(테스트, 문서, 리팩터링) |
| `epic` | 하위 작업이 있는 큰 기능 |
| `chore` | 유지 관리(의존성, 도구) |

## 우선순위

| 우선순위 | 수준 | 예시 |
|----------|-------|----------|
| 0 | 치명적 | 보안, 데이터 손실, 빌드 실패 |
| 1 | 높음 | 주요 기능, 중요한 버그 |
| 2 | 중간 | 있으면 좋은 기능, 사소한 버그 |
| 3 | 낮음 | 다듬기, 최적화 |
| 4 | 백로그 | 향후 아이디어 |

## 이슈 생성

```bash
# 기본 이슈
bd create "로그인 버그 수정" -t bug -p 1

# 설명 포함
bd create "비밀번호 재설정 추가" \
  --description="사용자가 잊어버린 비밀번호를 이메일로 재설정할 수 있어야 함" \
  -t feature -p 2

# 레이블 포함
bd create "의존성 업데이트" -t chore -l "maintenance,security"

# 에이전트용 JSON 출력
bd create "작업" -t task --json
```

## 의존성

### 차단 의존성

`blocks` 관계는 준비 큐에 영향을 줍니다.

```bash
# 의존성 추가: bd-2는 bd-1에 의존
bd dep add bd-2 bd-1

# 의존성 보기
bd dep tree bd-2

# 차단된 이슈 보기
bd blocked

# 준비된 작업 보기(차단되지 않음)
bd ready
```

### 구조적 관계

다음 관계는 준비 큐에 영향을 주지 않습니다.

```bash
# 부모-자식(epic 하위 작업)
bd create "에픽" -t epic
bd create "하위 작업" --parent bd-42

# 발견 출처(작업 중 발견)
bd create "발견한 버그" --deps discovered-from:bd-42

# 관련(소프트 링크)
bd dep relate bd-1 bd-2
```

### 의존성 유형

| 유형 | 설명 | 준비 큐에 미치는 영향 |
|------|-------------|-------------------|
| `blocks` | 하드 의존성 | 있음 - 차단된 항목은 준비되지 않음 |
| `parent-child` | Epic/하위 작업 계층 | 없음 |
| `discovered-from` | 발견 출처 추적 | 없음 |
| `related` | 소프트 관계 | 없음 |

## 계층적 이슈

큰 기능에는 계층적 ID를 사용합니다.

```bash
# epic 생성
bd create "인증 시스템" -t epic -p 1
# 반환: bd-a3f8e9

# 자식 작업 자동 번호 지정
bd create "로그인 UI 설계" --parent bd-a3f8e9       # bd-a3f8e9.1
bd create "백엔드 검증" --parent bd-a3f8e9          # bd-a3f8e9.2

# 계층 보기
bd dep tree bd-a3f8e9
```

## 이슈 업데이트

```bash
# 상태 변경
bd update bd-42 --claim

# 우선순위 변경
bd update bd-42 --priority 0

# 레이블 추가
bd update bd-42 --add-label urgent

# 여러 항목 변경
bd update bd-42 --claim --priority 1 --add-label "in-review"
```

## 이슈 닫기

```bash
# 단순 닫기
bd close bd-42

# 이유 포함
bd close bd-42 --reason "PR #123에서 구현됨"

# JSON 출력
bd close bd-42 --json
```

## 검색 및 필터링

```bash
# 상태별
bd list --status open
bd list --status in_progress

# 우선순위별
bd list --priority 1
bd list --priority 0,1  # 여러 값

# 유형별
bd list --type bug
bd list --type feature,task

# 레이블별
bd list --label-any urgent,critical
bd list --label-all backend,security

# 필터 조합
bd list --status open --priority 1 --type bug --json
```
