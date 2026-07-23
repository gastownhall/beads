---
title: 고급 기능
description: 이슈와 접두사 이름 변경, 중복 병합, 압축, 데이터베이스 리디렉션, 성능 조정을 위한 고급 bd 작업
---

beads의 고급 기능입니다.

## 이슈 이름 변경

참조를 보존하면서 이슈 이름을 변경합니다.

```bash
bd rename bd-42 bd-new-id
bd rename bd-42 bd-new-id --dry-run  # 미리 보기
```

업데이트되는 항목:
- 이전 ID를 가리키는 모든 의존성
- 다른 이슈의 모든 참조
- 댓글과 설명

## 접두사 이름 변경

데이터베이스의 모든 이슈에 대한 접두사를 변경합니다. 예를 들어
`knowledge-work-`를 `kw-`로 줄일 수 있습니다.

```bash
bd rename-prefix kw- --dry-run  # 적용하지 않고 미리 보기
bd rename-prefix kw-            # 모든 knowledge-work-* ID가 kw-*로 변경
```

이름 변경은 모든 필드의 모든 이슈 ID와 텍스트 참조를 업데이트합니다. 접두사는 소문자,
숫자, 하이픈을 사용해 최대 8자이며, 문자로 시작하고 하이픈으로 끝나야 합니다. 손상된
데이터베이스에 여러 접두사를 가진 이슈가 있다면 `bd rename-prefix <prefix> --repair`가
통합합니다. [bd rename-prefix](/cli-reference/rename-prefix)를 참조하세요.

## 중복 감지 및 병합

내용(제목, 설명, 설계, 인수 조건)이 동일한 이슈를 찾아 통합합니다.

```bash
bd duplicates              # 권장 작업과 함께 중복 그룹 보고
bd duplicates --dry-run    # --auto-merge가 수행할 작업 미리 보기
bd duplicates --auto-merge # 모든 중복 그룹 병합
```

상태가 일치할 때만(open은 open끼리, closed는 closed끼리) 내용 해시로 이슈를
그룹화합니다. 병합 대상은 각 그룹에서 가장 많이 참조되는 이슈이며, 동률이면 가장 작은
ID를 사용합니다. `--auto-merge`는 각 그룹에서 다음을 수행합니다.

- 중복 이슈의 자식을 대상의 자식으로 변경
- `Duplicate of <target>` 이유로 중복 이슈 닫기
- `related` 의존성으로 각 중복 이슈를 대상에 연결

알려진 중복 하나를 수동으로 표시하려면 다음을 실행합니다.

```bash
bd duplicate bd-42 --of bd-41  # bd-42를 bd-41의 중복으로 닫기
```

닫기는 영구적이지만 Dolt 버전 기록이 원래 상태를 보존합니다. `bd show bd-41`과
`bd dep tree bd-41`로 결과를 확인하세요.

## 데이터베이스 압축

오래된 이슈를 압축해 데이터베이스 크기를 줄입니다.

```bash
# 압축 통계 보기
bd admin compact --stats

# 후보 미리 보기(닫힌 지 30일 이상)
bd admin compact --analyze --json

# 에이전트가 생성한 요약 적용
bd admin compact --apply --id bd-42 --summary summary.txt

# 즉시 삭제(주의!)
bd admin cleanup --force
```

**압축 시점:**
- 오래된 닫힌 이슈가 있고 데이터베이스가 10MB 초과일 때
- 주요 마일스톤 후
- 프로젝트 단계를 보관하기 전

## 기록에서 복원

압축된 이슈의 압축 전 내용을 복구합니다.

```bash
bd restore bd-42          # 보관된 원본 내용 표시
bd restore bd-42 --apply  # 원본 내용을 이슈에 다시 쓰기
```

보관된 스냅샷이 없으면 `bd restore`는 Dolt 버전 기록에서 최선의 방식으로 재구성합니다.
이 내용은 표시만 가능하고 적용할 수는 없습니다.

## 데이터베이스 검사

`bd sql`에는 Dolt 서버 모드가 필요합니다(`bd dolt start`, 아래 성능 조정 참조).
기본 임베디드 모드 데이터베이스에는 사용할 수 없습니다.

```bash
# 스키마 정보
bd info --schema --json

# 원시 데이터베이스 쿼리(서버 모드 전용)
bd sql "SELECT * FROM issues LIMIT 5"
```

## 데이터베이스 리디렉션

여러 git 클론이 하나의 beads 데이터베이스를 공유할 수 있습니다. 여러 에이전트 또는
체크아웃 디렉터리가 같은 이슈를 작업할 때 유용합니다. 보조 클론에 대상 `.beads`
디렉터리의 단일 경로(상대 또는 절대)를 담은 `.beads/redirect` 파일을 만드세요.

```bash
# 보조 클론에서
mkdir -p .beads
echo "../main-clone/.beads" > .beads/redirect
```

실제로 사용 중인 데이터베이스를 확인합니다.

```bash
bd where          # 리디렉션 정보를 포함한 활성 .beads 위치
bd where --json
```

제한 사항 및 지침:

- 리디렉션 체인은 따라가지 않으며 한 단계만 작동하므로 리디렉션은 실제 `.beads`
  디렉터리를 직접 가리켜야 합니다.
- 대상 디렉터리가 존재하고 유효한 데이터베이스를 포함해야 합니다.
- 별도 프로젝트와 장기 포크에는 리디렉션 대신 자체 데이터베이스를 제공하세요.
- Git 워크트리에는 리디렉션이 필요 없습니다. 연결된 워크트리는 저장소의 `.beads`
  워크스페이스를 자동으로 찾습니다. [Git 워크트리](/reference/worktrees)를 참조하세요.

## 확장 가능한 데이터베이스

Dolt 기반 프로젝트에서는 확장 상태를 beads 데이터베이스 외부에 유지하고 안정적인 CLI
인터페이스를 통해 beads에 연결하세요.

```bash
# 통합 워크플로용 이슈 쿼리
bd list --json
bd query "status=open AND priority<=2" --json

# 검사를 위해 직접 SQL 실행(서버 모드 전용)
bd sql "SELECT id, title, status FROM issues LIMIT 5"
```

저장소 직접 접근을 통한 사용자 지정 테이블은 레거시 SQLite 전용 패턴입니다. SQLite
기반 확장을 유지 관리하는 경우에만 [bd-example-extension-go 예시](https://github.com/gastownhall/beads/blob/main/examples/bd-example-extension-go/README.md)를 참조하세요.

## 감사 데이터

Beads는 감사와 복구 워크플로를 위해 이슈 수명 주기 이벤트를 데이터베이스에 기록합니다.
독립 실행형 `bd events` 명령은 없습니다. JSON 출력을 통해 현재 이슈 상태를 검사하거나
필요할 때 감사 테이블을 직접 쿼리하세요.

```bash
# 현재 이슈 상태
bd show bd-a1b2 --json

# 한 이슈에 대해 최근 저장된 이벤트(서버 모드 전용)
bd sql "SELECT event_type, actor, created_at FROM events WHERE issue_id = 'bd-a1b2' ORDER BY created_at DESC LIMIT 20"
```

이벤트:
- `issue.created`
- `issue.updated`
- `issue.closed`
- `dependency.added`
- `sync.completed`

## 일괄 작업

### 여러 항목 생성

JSONL 내보내기에서 새 데이터베이스를 부트스트랩합니다.

```bash
# 소스 프로젝트에서
bd export -o issues.jsonl

# 새 프로젝트에서: 내보내기 파일을 .beads/issues.jsonl
# (또는 구성된 import.path)에 배치한 다음 여기서 초기화
bd init --from-jsonl
```

ID가 이미 존재하는 레코드를 가져오면 해당 이슈가 제자리에서 업데이트됩니다. 해시 ID는
내용에서 파생되고 안정적이므로 일치하는 ID는 충돌이 아니라 업데이트입니다.

### 여러 항목 업데이트

```bash
bd list --status open --priority 4 --json | \
  jq -r '.[].id' | \
  xargs -I {} bd update {} --priority 3
```

### 여러 항목 닫기

```bash
bd list --label "sprint-1" --status open --json | \
  jq -r '.[].id' | \
  xargs -I {} bd close {} --reason "스프린트 완료"
```

## 통합 접근

CLI를 지원되는 통합 경계로 사용합니다.

```bash
# 기계 판독 가능한 이슈 데이터
bd show bd-a1b2 --json

# 자동화용 준비된 작업 큐
bd ready --json

# 활성 Dolt 데이터베이스 직접 SQL 검사(서버 모드 전용)
bd sql "SELECT id, priority, status FROM issues WHERE status != 'closed'"
```

`internal/` 아래의 저장소 패키지는 공개 Go API가 아닙니다.

[MCP 서버](/integrations/mcp-server)는 같은 경계 위의 무상태 어댑터입니다. MCP 호출을
`bd` CLI 실행으로 변환하고 작업 디렉터리에 따라 각 호출을 올바른 `.beads`
워크스페이스로 라우팅합니다. 이슈 데이터를 직접 캐시하거나 저장하지 않습니다.

## 성능 조정

### 큰 데이터베이스

```bash
# 오래된 닫힌 이슈 요약(위의 데이터베이스 압축 참조)
bd admin compact --stats

# Dolt 가비지 컬렉션으로 디스크 공간 회수
bd admin compact --dolt

# 30일보다 오래된 Dolt 커밋 스쿼시(먼저 미리 보기)
bd compact --dry-run
bd compact --force
```

### 다수의 동시 에이전트

Beads는 여러 에이전트의 동시 접근을 처리하기 위해 Dolt 서버 모드를 사용합니다. 서버가
트랜잭션 격리를 자동으로 관리합니다.

```bash
# Dolt 서버 시작
bd dolt start

# 서버 상태 확인
bd doctor
```

### CI/CD 최적화

CI/CD 환경에서 beads는 기본적으로 임베디드 모드를 사용합니다(서버 불필요).

```bash
# 특별한 플래그 없이 명령을 직접 실행
bd list
```
