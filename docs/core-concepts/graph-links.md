---
title: Beads의 그래프 링크
description: "이슈 간 비차단 링크: replies-to 스레드, relates-to, duplicates 및 supersedes 체인"
---

Beads는 지식 그래프를 만들기 위해 이슈 간 여러 유형의 링크를 지원합니다. 이 링크를 사용하면 단순한 차단 의존성을 넘어 풍부한 쿼리와 순회가 가능합니다.

## 링크 유형

### replies-to - 대화 스레드

이메일 또는 채팅 대화와 비슷한 메시지 스레드를 만듭니다.

**생성 방법:**
- 오케스트레이터 메일 답장 명령(오케스트레이터가 메시징 처리)
- `bd dep add <new-id> <original-id> --type replies-to`(수동 연결)

**사용 사례:**
- 에이전트 간 메시지 스레드
- 이슈의 토론 체인
- 후속 커뮤니케이션

**예시:**

```bash
# 원본 메시지(오케스트레이터 메일을 통해)
# orchestrator mail send worker/ -s "검토 필요" -m "issue-xyz를 검토해 주세요"
# 생성: msg-a1b2

# 답장(replies-to 자동 설정)
# orchestrator mail reply msg-a1b2 -m "완료했습니다. 사소한 의견과 함께 승인합니다."
# 생성: replies-to: msg-a1b2가 있는 msg-c3d4
```

**스레드 보기:**

```bash
bd show gt-a1b2 --thread
```

### relates-to - 느슨한 연관 관계

관련 이슈 간 양방향 "함께 보기" 링크입니다. 차단하거나 계층화하지 않고 단순히 연관시킵니다.

**생성 방법:**
- `bd dep relate <id1> <id2>` - 두 이슈를 서로 연결

**제거 방법:**
- `bd dep unrelate <id1> <id2>` - 양방향 링크 제거

**사용 사례:**
- 관련 기능 상호 참조
- 버그와 관련 작업 연결
- 지식 그래프 구축
- "함께 보기" 연결

**예시:**

```bash
# 관련 이슈 두 개 연결
bd dep relate bd-auth bd-security
# 결과: bd-auth.relates-to에 bd-security 포함
#       bd-security.relates-to에 bd-auth 포함

# 관련 이슈 보기
bd show bd-auth
# 표시: Related: bd-security

# 링크 제거
bd dep unrelate bd-auth bd-security
```

**여러 링크:**
한 이슈에 relates-to 링크가 여러 개 있을 수 있습니다.

```bash
bd dep relate bd-api bd-auth
bd dep relate bd-api bd-docs
bd dep relate bd-api bd-tests
# 이제 bd-api가 이슈 3개와 관련됨
```

### duplicates - 중복 제거

이슈를 정식 이슈의 중복으로 표시합니다. 중복 이슈는 자동으로 닫힙니다.

**생성 방법:**
- `bd duplicate <id> --of <canonical>`

**사용 사례:**
- 중복 버그 보고서 통합
- 비슷한 기능 요청 병합
- 대규모 데이터베이스 중복 제거

**예시:**

```bash
# 비슷한 버그 보고서 두 개가 존재
bd show bd-bug1  # "Safari에서 로그인 실패"
bd show bd-bug2  # "Safari 로그인 작동 안 함"

# bug2를 bug1의 중복으로 표시
bd duplicate bd-bug2 --of bd-bug1
# 결과: bd-bug2가 duplicate_of: bd-bug1과 함께 닫힘

# 보기에 관계가 표시됨
bd show bd-bug2
# Status: closed
# Duplicate of: bd-bug1
```

**동작:**
- 중복 이슈가 자동으로 닫힙니다.
- 원본(정식) 이슈는 열린 상태로 유지됩니다.
- `duplicate_of` 필드에 정식 ID가 저장됩니다.

### supersedes - 버전 체인

이슈가 더 새로운 버전으로 대체되었음을 표시합니다. 이전 이슈는 자동으로 닫힙니다.

**생성 방법:**
- `bd supersede <old-id> --with <new-id>`

**사용 사례:**
- 설계 문서 버전
- 사양 발전
- 아티팩트 버전 관리
- RFC 체인

**예시:**

```bash
# 원본 설계 문서
bd create --title "설계 문서 v1" --type task
# 생성: bd-doc1

# 나중에 업데이트된 버전 생성
bd create --title "설계 문서 v2" --type task
# 생성: bd-doc2

# v1이 대체되었다고 표시
bd supersede bd-doc1 --with bd-doc2
# 결과: bd-doc1이 superseded_by: bd-doc2와 함께 닫힘

# 보기에 체인이 표시됨
bd show bd-doc1
# Status: closed
# Superseded by: bd-doc2
```

**동작:**
- 이전 이슈가 자동으로 닫힙니다.
- 새 이슈는 현재 상태로 유지됩니다.
- `superseded_by` 필드에 대체 이슈 ID가 저장됩니다.

## 스키마 필드

이슈에 다음 필드가 추가됩니다.

| 필드 | 유형 | 설명 |
|-------|------|-------------|
| `replies-to` | string | 부모 메시지 ID(스레드) |
| `relates-to` | []string | 관련 이슈 ID(양방향) |
| `duplicate_of` | string | 정식 이슈 ID |
| `superseded_by` | string | 대체 이슈 ID |

## 링크 쿼리

### 이슈 세부 정보 보기

```bash
bd show <id>
```

이슈의 모든 링크 유형을 표시합니다.

```
bd-auth: 인증 구현
Status: open
Priority: P1

Related to (3):
  bd-security: 보안 감사
  bd-users: 사용자 관리
  bd-sessions: 세션 처리
```

### 스레드 보기

```bash
bd show <id> --thread
```

`replies-to` 체인을 따라 대화 기록을 표시합니다.

### JSON 출력

```bash
bd show <id> --json
```

그래프 링크를 포함한 모든 필드를 반환합니다.

```json
{
  "id": "bd-auth",
  "title": "인증 구현",
  "relates-to": ["bd-security", "bd-users", "bd-sessions"],
  "duplicate_of": "",
  "superseded_by": ""
}
```

## 의존성과 비교

| 링크 유형 | 차단 여부 | 계층 여부 | 방향 |
|-----------|-----------|---------------|-----------|
| `blocks` | 예 | 아니요 | 단방향 |
| `parent_id` | 아니요 | 예 | 단방향 |
| `relates-to` | 아니요 | 아니요 | 양방향 |
| `replies-to` | 아니요 | 아니요 | 단방향 |
| `duplicate_of` | 아니요 | 아니요 | 단방향 |
| `superseded_by` | 아니요 | 아니요 | 단방향 |

## 사용 사례

### 지식 베이스

관련 문서를 연결합니다.

```bash
bd dep relate bd-api-ref bd-quickstart
bd dep relate bd-api-ref bd-examples
bd dep relate bd-quickstart bd-install
```

### 버그 분류

중복 보고서를 통합합니다.

```bash
# 잠재적 중복 찾기
bd duplicates

# 중복 병합
bd duplicate bd-bug42 --of bd-bug17
bd duplicate bd-bug58 --of bd-bug17
```

### 버전 기록

문서의 발전 과정을 추적합니다.

```bash
bd supersede bd-rfc1 --with bd-rfc2
bd supersede bd-rfc2 --with bd-rfc3
# 이제 bd-rfc3가 현재 버전
```

### 메시지 스레드

대화 체인을 구축합니다(오케스트레이터 메일 사용).

```bash
# orchestrator mail send dev/ -s "질문" -m "X는 어떻게 작동하나요?"
# orchestrator mail reply msg-q1 -m "X는 ... 방식으로 작동합니다."
# orchestrator mail reply msg-q1.reply -m "감사합니다!"
```

## 모범 사례

1. **relates-to를 아껴서 사용** - 링크가 너무 많으면 잡음이 됩니다.
2. **구체적인 링크 유형 선호** - `duplicates`가 일반적인 relates-to보다 명확합니다.
3. **스레드를 얕게 유지** - 깊은 답장 체인은 따라가기 어렵습니다.
4. **supersedes 체인 문서화** - 버전이 변경된 이유를 기록합니다.
5. **중복 생성 전 쿼리** - 먼저 `bd search`를 실행합니다.

## 함께 보기

- [메시징](https://github.com/gastownhall/beads/blob/main/engdocs/messaging.md) - 메일 명령과 스레드
- [의존성](/getting-started/quickstart#add-dependencies) - 차단 의존성
- [CLI 참조](/cli-reference/index) - 모든 명령
