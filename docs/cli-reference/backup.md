---
title: "bd backup"
description: "다른 컴퓨터에서 복구할 수 있도록 beads 데이터베이스를 백업합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc backup`에서 생성되었습니다.

다른 컴퓨터에서 복구할 수 있도록 beads 데이터베이스를 백업합니다.

Dolt 네이티브 데이터베이스 백업입니다. 테이블, 브랜치, 커밋 이력, working-set
데이터를 포함한 데이터베이스 상태를 보존합니다. 마이그레이션과 상호 운용성을 위해
이슈 레코드를 JSONL로 쓰는 'bd export'와 다릅니다.

명령:
  bd backup init &lt;path&gt;    백업 대상 설정(파일 시스템 또는 DoltHub)
  bd backup sync           구성된 백업 대상으로 push
  bd backup restore [path] 백업 디렉터리에서 복원
  bd backup remove         백업 대상 제거
  bd backup status         백업 상태 표시

DoltHub는 클라우드 백업에 권장됩니다:
  bd backup init https://doltremoteapi.dolthub.com/&lt;user&gt;/&lt;repo&gt;
  인증을 위해 DOLT_REMOTE_USER와 DOLT_REMOTE_PASSWORD를 설정하세요.

```
bd backup [flags]
```

## bd backup init

파일 시스템 경로 또는 URL을 백업 대상으로 구성합니다.

경로는 로컬 디렉터리(외장 드라이브, NAS, Dropbox 폴더) 또는 DoltHub 원격 URL일 수
있습니다. 대상이 이전에 구성되었다면 새 경로로 업데이트됩니다.

파일 시스템 예시:
  bd backup add /mnt/usb/beads-backup
  bd backup add ~/Dropbox/beads-backup

DoltHub(클라우드 백업에 권장):
  bd backup add https://doltremoteapi.dolthub.com/myuser/beads-backup

추가 후 데이터를 push하려면 'bd backup sync'를 실행하세요.

```
bd backup init <path> [flags]
```

**별칭:** add

## bd backup remove

구성된 백업 대상을 제거합니다.

Dolt에서 백업 원격 등록을 해제하고 로컬 백업 구성을 제거합니다.
대상에 있는 백업 데이터는 삭제되지 않습니다.

```
bd backup remove [flags]
```

**별칭:** rm

## bd backup restore

Dolt 네이티브 백업에서 beads 데이터베이스를 복원합니다.

기본적으로 .beads/backup/(또는 구성된 백업 디렉터리)에서 읽습니다.
Dolt 백업이 있는 디렉터리 경로를 선택적으로 지정할 수 있습니다.

'bd backup sync' 또는 동등한 Dolt 백업으로 생성된 전체 데이터베이스 백업을
복원합니다. 'bd export'가 생성한 JSONL 파일은 이슈 내보내기이며 이 명령의
복원 대상이 아닙니다.

기존 데이터베이스를 백업 콘텐츠로 덮어쓰려면 --force를 사용하세요.

데이터베이스가 이미 초기화되어 있어야 합니다(필요하면 먼저 'bd init' 실행).
한 단계에서 초기화하고 복원하려면 다음을 사용하세요: bd init &amp;&amp; bd backup restore

```
bd backup restore [path] [flags]
```

**플래그:**

```
      --force   기존 데이터베이스를 백업 콘텐츠로 덮어쓰기
```

## bd backup status

마지막 백업 상태를 표시합니다

```
bd backup status [flags]
```

## bd backup sync

현재 beads 데이터베이스를 구성된 Dolt 백업 대상과 동기화합니다.

전체 데이터베이스 상태(모든 브랜치, 전체 이력)를 'bd backup init'으로 구성한
백업 위치로 push합니다.

백업은 원자적입니다. 동기화에 실패하면 이전 백업 상태가 보존됩니다.

대상을 구성하려면 먼저 'bd backup init &lt;path&gt;'을 실행하세요.

```
bd backup sync [flags]
```
