---
title: "bd config"
description: "외부 통합 및 기본 설정의 구성 값을 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc config`에서 생성되었습니다.

외부 통합 및 기본 설정의 구성 값을 관리합니다.

구성은 프로젝트별로 beads 데이터베이스에 저장되며 버전 관리에 적합합니다.

일반 네임스페이스:
  - export.*          자동 내보내기 설정(config.yaml에 저장)
  - import.*          JSONL 가져오기 설정(config.yaml에 저장)
  - jira.*            Jira 통합 설정
  - linear.*          Linear 통합 설정
  - github.*          GitHub 통합 설정
  - custom.*          사용자 정의 통합 설정
  - status.*          이슈 상태 구성
  - doctor.suppress.* 특정 bd doctor 경고 숨기기(GH#1095)

자동 내보내기(config.yaml):
  쓰기 명령 후 .beads/issues.jsonl로 선택적 JSONL 내보내기(스로틀링됨).
  뷰어(bv), 교환, 이슈 수준 마이그레이션에 유용하며 백업은 아닙니다.
  컴퓨터 간 동기화가 아닙니다. Dolt 원격과 bd dolt push/pull을 사용하세요.
  기본적으로 비활성화됩니다. 최신 JSONL이 필요한 통합에만 활성화하세요.
  자동 스테이징은 별도이며 기본적으로 비활성화됩니다.

  키:
    export.auto       자동 내보내기 활성화/비활성화(기본값: false)
    export.path       .beads/ 기준 상대 출력 파일 이름(기본값: issues.jsonl)
    export.interval   내보내기 간 최소 시간(기본값: 60s)
    export.git-add    내보내기 파일 자동 스테이징(기본값: false)

자동 가져오기(config.yaml):
  JSONL 가져오기 경로가 암시되면 기본적으로 .beads/issues.jsonl을 읽습니다.
  가져오기가 프로젝트 .beads/ 디렉터리 내부에 유지되고 컴퓨터 간 이식 가능하도록
  상대 파일 이름/경로를 사용하세요.

  키:
    import.path       .beads/ 기준 상대 입력 파일 이름(기본값: issues.jsonl)

사용자 정의 상태:
  status.custom 구성 키로 다단계 파이프라인용 사용자 정의 상태를 정의할 수 있습니다.
  상태는 쉼표로 구분해야 합니다.

  예시:
    bd config set status.custom "awaiting_review,awaiting_testing,awaiting_docs"

  이 설정을 사용하면 이슈에서 기본 제공 상태(open, in_progress, blocked, deferred,
  closed) 외에 'awaiting_review' 같은 상태를 사용할 수 있습니다.

Doctor 경고 숨기기:
  검사 이름 slug로 특정 bd doctor 경고를 숨깁니다:
    bd config set doctor.suppress.pending-migrations true
    bd config set doctor.suppress.git-hooks true
  검사 이름은 slug로 변환됩니다: "Git Hooks" → "git-hooks".
  경고만 숨겨집니다(오류와 통과한 검사는 항상 표시).
  숨기기 해제: bd config unset doctor.suppress.&lt;slug&gt;

예시:
  bd config set export.auto true                       # 뷰어 통합용 자동 내보내기 활성화
  bd config set export.path "beads.jsonl"              # 사용자 정의 내보내기 파일 이름
  bd config set import.path "beads.jsonl"              # 사용자 정의 가져오기 파일 이름
  bd config set export.git-add true                    # 내보내기 파일도 스테이징
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set status.custom "awaiting_review,awaiting_testing"
  bd config set doctor.suppress.pending-migrations true
  bd config set dolt.debug true                        # Dolt sql-server 디버그 모드 활성화(loglevel=debug, --prof cpu)
  bd config set dolt.local-only true                   # bd init 중 Dolt 동기화 원격 연결 건너뛰기
  bd config get export.auto
  bd config list
  bd config unset jira.url

```
bd config [flags]
```

## bd config apply

실제 시스템 상태를 선언된 구성과 일치하도록 조정합니다.

드리프트를 감지한 뒤 발견한 불일치를 수정합니다:

  - hooks     누락되거나 오래된 경우 git 훅 재설치
  - remote    federation.remote와 일치하도록 Dolt origin 원격 추가/업데이트
  - server    dolt.shared-server가 활성화된 경우 Dolt 서버 시작

이 명령은 멱등하므로 여러 번 실행해도 안전합니다. 수정하지 않고 변경될 항목을
미리 보려면 --dry-run을 사용하세요.

예시:
  bd config apply
  bd config apply --dry-run
  bd config apply --json

```
bd config apply [flags]
```

**플래그:**

```
      --dry-run   수정하지 않고 변경될 항목 표시
```

## bd config drift

선언된 구성과 실제 시스템 상태 간 드리프트를 감지합니다.

"환경이 구성과 일치하는가?"에 답하는 읽기 전용 진단이며 변경을 수행하지 않습니다.

검사:
  - hooks     Git 훅이 설치되어 있고 최신임
  - remote    Dolt 원격이 federation.remote 구성과 일치함
  - server    서버 상태가 dolt.shared-server 구성과 일치함

종료 코드:
  0  드리프트 감지 안 됨(모든 검사 ok/info/skipped)
  1  드리프트 감지됨(하나 이상의 검사 상태가 "drift")

예시:
  bd config drift
  bd config drift --json

```
bd config drift [flags]
```

## bd config get

구성 값을 가져옵니다

```
bd config get <key> [flags]
```

## bd config list

모든 구성을 나열합니다

```
bd config list [flags]
```

## bd config set

구성 값을 설정합니다

```
bd config set <key> <value> [flags]
```

**플래그:**

```
      --force-git-tracked   git 추적 구성 파일에 시크릿 키 쓰기 허용(주의해서 사용)
```

## bd config set-many

단일 자동 커밋과 자동 push로 여러 구성 값을 한 번에 설정합니다.

각 인수는 key=value 형식이어야 합니다. 쓰기 전에 모든 값을 검증합니다.
특히 CI에서 'bd config set'을 따로 호출하는 것보다 빠르고 잡음이 적습니다.

예시:
  bd config set-many ado.state_map.open=New ado.state_map.closed=Closed
  bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ

```
bd config set-many <key=value>... [flags]
```

**플래그:**

```
      --force-git-tracked   git 추적 구성 파일에 시크릿 키 쓰기 허용(주의해서 사용)
```

## bd config show

각 값의 출처를 보여 주는 주석과 함께 모든 소스의 유효 구성을 통합하여 표시합니다.

소스(Viper 관리 키의 우선순위순):
  - env          환경 변수(BD_* 또는 BEADS_*)
  - config.yaml  프로젝트 구성 파일(.beads/config.yaml)
  - default      기본 제공 기본값

추가 소스:
  - metadata     .beads/metadata.json의 연결 설정
  - database     Dolt 데이터베이스에 저장된 통합 구성
  - git          Git 구성(예: beads.role)

예시:
  bd config show
  bd config show --json
  bd config show --source config.yaml

```
bd config show [flags]
```

**플래그:**

```
      --source string   소스로 필터링(예: config.yaml, env, default, metadata, database, git)
```

## bd config unset

구성 값을 삭제합니다

```
bd config unset <key> [flags]
```

## bd config validate

동기화 관련 구성 설정을 검증합니다.

검사:
  - federation.sovereignty가 유효함(T1, T2, T3, T4 또는 빈 값)
  - Dolt 동기화용 federation.remote가 설정됨
  - 원격 URL 형식이 유효함(dolthub://, gs://, s3://, az://, file://)
  - routing.mode가 유효함(auto, maintainer, contributor, explicit)

	예시:
	  bd config validate
	  bd config validate --json

```
bd config validate [flags]
```
