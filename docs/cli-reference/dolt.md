---
title: "bd dolt"
description: "Dolt 데이터베이스 설정과 서버 수명 주기를 구성하고 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc dolt`에서 생성되었습니다.

Dolt 데이터베이스 설정과 서버 수명 주기를 구성하고 관리합니다.

Beads는 모든 데이터베이스 작업에 dolt sql-server를 사용합니다. 서버는 필요할 때
투명하게 자동 시작됩니다. 명시적 제어나 진단에는 이 명령을 사용하세요.

서버 수명 주기:
  bd dolt start        이 프로젝트의 Dolt 서버 시작
  bd dolt stop         이 프로젝트의 Dolt 서버 중지
  bd dolt status       Dolt 서버 상태 표시

구성:
  bd dolt show         연결 테스트와 함께 현재 Dolt 구성 표시
  bd dolt set &lt;k&gt; &lt;v&gt;  구성 값 설정
  bd dolt test         서버 연결 테스트

버전 관리:
  bd dolt commit       대기 중인 변경 사항 커밋
  bd dolt push         Dolt 원격으로 커밋 push
  bd dolt pull         Dolt 원격에서 커밋 pull

원격 관리:
  bd dolt remote add &lt;name&gt; &lt;url&gt;   Dolt 원격 추가
  bd dolt remote list                구성된 원격 나열
  bd dolt remote remove &lt;name&gt;       Dolt 원격 제거

'bd dolt set' 구성 키:
  database  데이터베이스 이름(기본값: 이슈 접두사 또는 "beads")
  host      서버 호스트(기본값: 127.0.0.1)
  port      서버 포트(자동 감지, bd dolt set port &lt;N&gt;으로 재정의)
  user      MySQL 사용자(기본값: root)
  data-dir  사용자 정의 dolt 데이터 디렉터리(절대 경로, 기본값: .beads/dolt)

'bd dolt set' 플래그:
  --update-config  팀 전체 기본값을 위해 config.yaml에도 쓰기

예시:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject
  bd dolt test

```
bd dolt [flags]
```

## bd dolt clean-databases

중단된 테스트 실행과 종료된 에이전트 때문에 공유 Dolt 서버에 쌓인 남은 테스트 및
에이전트 데이터베이스를 식별하고 삭제합니다.

오래된 데이터베이스 접두사: testdb_*, doctest_*, doctortest_*, beads_pt*, beads_vr*, beads_t*

이들은 서버 메모리를 낭비하고 동시 부하에서 성능을 저하시킬 수 있습니다.
실제로 삭제하지 않고 삭제될 항목을 보려면 --dry-run을 사용하세요.

```
bd dolt clean-databases [flags]
```

**플래그:**

```
      --dry-run   삭제하지 않고 삭제될 항목 표시
```

## bd dolt commit

working set의 커밋되지 않은 변경 사항에서 Dolt 커밋을 생성합니다.

일괄 모드의 기본 커밋 지점입니다. auto-commit이 "batch"로 설정되면 여러 bd 명령의
변경 사항이 working set에 쌓이고 여기에서 설명적인 요약 메시지와 함께 커밋됩니다.

깨끗한 working set이 필요한 push 작업 전, auto-commit이 꺼져 있거나 외부에서
변경한 경우에도 유용합니다.

추가 옵션(--stdin, 사용자 정의 메시지)은 다음을 참조하세요: bd vc commit

```
bd dolt commit [flags]
```

**플래그:**

```
  -m, --message string   커밋 메시지(기본값: 자동 생성)
```

## bd dolt killall

현재 저장소의 Dolt 데이터 디렉터리에 대한 표준 PID 파일에서 추적하지 않는 고립된
dolt sql-server 프로세스를 찾아 종료합니다.

오케스트레이터에서는 표준 서버가 $GT_ROOT/.beads/에 있습니다. 해당 공유 데이터
디렉터리를 사용하는 다른 dolt sql-server 프로세스는 고립된 것으로 간주되어 종료됩니다.

독립 실행형 모드에서는 현재 프로젝트의 Dolt 데이터 디렉터리를 사용하는 dolt sql-server
프로세스만 정리 대상입니다. 다른 프로젝트의 서버는 보존됩니다.

```
bd dolt killall [flags]
```

## bd dolt pull

구성된 Dolt 원격에서 로컬 데이터베이스로 커밋을 pull합니다.

데이터베이스 디렉터리에 Dolt 원격이 구성되어 있어야 합니다. Hosted Dolt 인증에는
DOLT_REMOTE_USER와 DOLT_REMOTE_PASSWORD 환경 변수를 설정하세요.

기본값 대신 이름을 지정한 특정 원격에서 pull하려면 --remote를 사용하세요.
원격이 이미 존재해야 합니다('bd dolt remote add' 참조).

```
bd dolt pull [flags]
```

**플래그:**

```
      --remote string   기본값 대신 이름을 지정한 특정 원격에서 pull
```

## bd dolt push

로컬 Dolt 커밋을 구성된 원격으로 push합니다.

데이터베이스 디렉터리에 Dolt 원격이 구성되어 있어야 합니다. Hosted Dolt 인증에는
DOLT_REMOTE_USER와 DOLT_REMOTE_PASSWORD 환경 변수를 설정하세요.

원격 변경 사항을 덮어쓰려면 --force를 사용하세요(예: 원격 working set에
커밋되지 않은 변경 사항이 있는 경우).

기본값 대신 이름을 지정한 특정 원격으로 push하려면 --remote를 사용하세요.
원격이 이미 존재해야 합니다('bd dolt remote add' 참조).

```
bd dolt push [flags]
```

**플래그:**

```
      --force           강제 push(원격 변경 사항 덮어쓰기)
      --remote string   기본값 대신 이름을 지정한 특정 원격으로 push
```

## bd dolt remote

push/pull 복제를 위한 Dolt 원격을 관리합니다.

하위 명령:
  add &lt;name&gt; &lt;url&gt;   새 원격 추가
  list               구성된 모든 원격 나열
  remove &lt;name&gt;      원격 제거

```
bd dolt remote [flags]
```

### bd dolt remote add

Dolt 원격을 추가합니다

```
bd dolt remote add <name> <url> [flags]
```

### bd dolt remote list

구성된 Dolt 원격을 나열합니다

```
bd dolt remote list [flags]
```

### bd dolt remote remove

Dolt 원격을 제거합니다

```
bd dolt remote remove <name> [flags]
```

## bd dolt set

metadata.json에 Dolt 구성 값을 설정합니다.

키:
  database  데이터베이스 이름(기본값: 이슈 접두사 또는 "beads")
  host      서버 호스트(기본값: 127.0.0.1)
  port      서버 포트(자동 감지, bd dolt set port &lt;N&gt;으로 재정의)
  user      MySQL 사용자(기본값: root)
  data-dir  사용자 정의 dolt 데이터 디렉터리(절대 경로, 기본값: .beads/dolt)

팀 전체 기본값을 위해 config.yaml에도 쓰려면 --update-config를 사용하세요.

예시:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100
  bd dolt set port 3307 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject

```
bd dolt set <key> <value> [flags]
```

**플래그:**

```
      --update-config   팀 전체 기본값을 위해 config.yaml에도 쓰기
```

## bd dolt show

연결 상태와 함께 현재 Dolt 구성을 표시합니다

```
bd dolt show [flags]
```

## bd dolt start

현재 beads 프로젝트의 dolt sql-server를 시작합니다.

서버는 프로젝트 경로에서 파생된 프로젝트별 포트로 백그라운드에서 실행됩니다.
PID와 로그는 .beads/에 저장됩니다.

서버는 필요할 때 투명하게 자동 시작되므로 수동 시작은 거의 필요하지 않습니다.
명시적 제어나 진단에는 이 명령을 사용하세요.

```
bd dolt start [flags]
```

## bd dolt status

현재 프로젝트의 Dolt 엔진 상태를 표시합니다.

임베디드 모드에서는 Dolt 엔진이 프로세스 내에서 실행된다고 보고하고 디스크 데이터
디렉터리를 표시합니다. beads가 관리하는 로컬 서버에는 로컬 PID 파일의 PID, 포트,
데이터 디렉터리를 표시합니다. 외부 관리 서버(원격 dolt_server_host 또는 bd 외부에서
관리하는 로컬 서버, dolt.auto-start: false, 예: 오케스트레이터 공유 sql-server)는
SQL로 구성된 엔드포인트를 ping하고 접근 가능성, 서버 버전, 데이터베이스를 보고합니다.

```
bd dolt status [flags]
```

## bd dolt stop

현재 프로젝트에서 beads가 관리하는 dolt sql-server를 중지합니다.

정상 종료 신호를 보냅니다. 자동 시작이 비활성화되지 않았다면 다음 bd 명령에서
서버가 자동으로 다시 시작됩니다.

```
bd dolt stop [flags]
```

**플래그:**

```
      --force   서버 강제 중지
```

## bd dolt test

구성된 Dolt 서버 연결을 테스트합니다.

다음을 확인합니다:
  1. 구성된 host:port에서 서버에 접근 가능
  2. 연결 수립 가능

서버 모드로 전환하기 전에 서버가 실행 중인지 확인하려면 사용하세요.

```
bd dolt test [flags]
```
