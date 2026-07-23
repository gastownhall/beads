---
title: 업그레이드
description: bd 바이너리 업그레이드, Git hook 갱신, schema 마이그레이션 실행, 원격 기반 및 시대가 다른 데이터베이스 처리
---

`bd`를 업그레이드하고 프로젝트를 동기화된 상태로 유지하는 방법입니다.

## 업데이트 확인

```bash
# 현재 버전
bd version

# 최근 버전의 새로운 기능
bd info --whats-new
bd info --whats-new --json  # 머신 판독 가능
```

## 요약 절차

1. 새 바이너리를 설치하기 전에 현재 `bd`로 원격 기반 데이터베이스를 동기화합니다.
   `bd dolt push`
   `bd dolt pull`
2. 마이그레이션 전에 backup합니다.
   `bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl`
3. 설치 방식에 맞는 명령으로 업그레이드합니다.
4. 업그레이드 후 다음 명령을 실행합니다.
   `bd info --whats-new`
   `bd hooks install`
   `bd version`
5. 원격 기반 데이터베이스의 schema 마이그레이션을 거치는 경우 지정된 migrator만
   다음 명령을 실행합니다.
   `bd migrate`
   `bd dolt push`

다른 클론은 독립적으로 마이그레이션하지 말고 새 바이너리를 설치한 뒤 `bd
bootstrap`을 실행해야 합니다. 전체 절차는 아래에 있습니다.

## 업그레이드

설치 방식에 맞는 명령을 사용하세요.

| 설치 방식 | 플랫폼 | 명령 |
|---|---|---|
| 빠른 설치 script | macOS, Linux, FreeBSD | `curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh \| bash` |
| PowerShell 설치 프로그램 | Windows | `irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 \| iex` |
| Homebrew | macOS, Linux | `brew upgrade beads` |
| go install(server 모드 전용) | macOS, Linux, FreeBSD, Windows | `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest` |
| go install(embedded 지원) | macOS, Linux, Windows | `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest` |
| npm | macOS, Linux, Windows | `npm update -g @beads/bd` |
| bun | macOS, Linux, Windows | `bun install -g --trust @beads/bd` |
| 소스에서 설치(Unix shell) | macOS, Linux, FreeBSD | `git pull && make build` |

### 빠른 설치 script(macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell 설치 프로그램(Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

### Homebrew

```bash
brew upgrade beads
```

{/* 정식 Homebrew tap 마이그레이션 snippet. 설치 페이지가 여기를 링크하므로
    두 내용을 동기화해서 유지합니다. */}
이전 tap formula가 `bd`로 설치되어 있다면 Homebrew core formula로 전환하세요.

```bash
brew uninstall bd
brew untap gastownhall/beads 2>/dev/null || true
brew untap steveyegge/beads 2>/dev/null || true
brew install beads
```

### go install

```bash
# server 모드 전용
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# embedded 지원
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### 소스에서 설치

```bash
cd beads
git pull
make build
sudo mv bd /usr/local/bin/
```

## 업그레이드 후

**중요:** 업그레이드 후 hook을 업데이트하세요.

```bash
# 1. 변경 내용 확인
bd info --whats-new

# 2. 새 버전에 맞게 Git hook 업데이트
bd hooks install

# 3. 오래된 hook 확인
bd info  # hook이 오래된 경우 경고 표시

# 4. Dolt backend를 사용한다면 서버 다시 시작
bd dolt stop && bd dolt start
```

**hook을 업데이트하는 이유:** Git hook은 `bd`와 함께 versioning됩니다. 오래된
hook에는 export 갱신, legacy fallback 또는 안전 수정이 빠질 수 있습니다.

## 데이터베이스 마이그레이션

주요 업그레이드 후 데이터베이스 마이그레이션을 확인하세요.

```bash
# 마이그레이션 계획 검사(AI 에이전트)
bd migrate --inspect --json

# 마이그레이션 변경 사항 미리 보기
bd migrate --dry-run

# 마이그레이션 적용
bd migrate

# 마이그레이션 후 이전 파일 정리
bd migrate --yes
```

### 원격 기반 데이터베이스와 여러 클론

`bd`는 Dolt 원격이 설정된 데이터베이스에 대기 중인 schema 마이그레이션을 조용히
적용하지 않습니다. 공유 원격의 클론을 둘 이상 독립적으로 마이그레이션하면 schema가
fork되고, 이후 `bd dolt pull`로 병합할 수 없습니다. 이 손상은 조용히 발생하며
primary key 구조를 변경하는 마이그레이션에서는 복구할 수 없습니다
([#4259](https://github.com/gastownhall/beads/issues/4259)). 지원되는 흐름은 한
머신에서 마이그레이션하고 게시한 뒤 다른 모든 클론이 마이그레이션된 데이터베이스를
다시 클론하는 방식입니다.

이 절차는 원격 기반 데이터베이스에서 대기 중인 마이그레이션을 거치는 **모든**
업그레이드에 적용됩니다. prerelease와 stable release 모두 절차가 같습니다.

Gate는 기본적으로 **상태를 인식합니다**
([#4516](https://github.com/gastownhall/beads/issues/4516)). 차단하기 전에 `bd`가
원격의 *cache된* schema 상태를 확인하고 다음과 같이 동작합니다.

- 원격과 현재 클론의 schema 버전이 같으면 **자동 마이그레이션**합니다. 아직 아무도
  마이그레이션하지 않았으므로 이 클론이 안전한 첫 실행자가 될 수 있습니다. 동시에
  처음 실행해도 같은 table로 수렴합니다. 이후 `bd dolt push`를 실행하라고 알립니다.
- 다른 클론이 이미 원격을 마이그레이션했다면 **중지하고 adopt**(`bd bootstrap`)하도록
  안내합니다.
- 현재 클론과 원격이 같은 마이그레이션에 서로 다른 내용을 적용한 실제 fork이거나
  cache된 ref에서 원격 schema 상태를 읽을 수 없으면 **사람의 결정을 기다리며
  중지**합니다.

이 동작을 사용하지 않고 Gate가 항상 차단하게 하려면 `BD_SMART_GATE=0`을 설정하세요.
아래 절차는 명시적인 경로이며 어느 모드에서나 동일하게 작동합니다.

**중요한 순서:** 새 바이너리를 설치하면 대기 중인 마이그레이션이 있는 데이터베이스는
**열 때마다** Gate의 적용을 받습니다. `bd migrate`뿐 아니라 `bd dolt push`와 `bd
dolt pull`도 거부됩니다. 따라서 새 바이너리를 설치하기 *전에* **현재** 바이너리로
모든 동기화를 수행하세요.

**마이그레이션 전에 backup하세요.** Schema 마이그레이션은 데이터베이스 구조가 이전
마이그레이션 결과와 일치한다고 가정하지만, 실제 데이터베이스는 중단된 쓰기, 도구
버그, 매우 오래된 bootstrap 등으로 drift할 수 있습니다. JSONL export는 비용이
적고 이슈 전체를 포함하며 모든 `bd` 버전에서 import할 수 있습니다.

```bash
bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl
```

`bd export`는 이슈만 캡처하고 Dolt 기록이나 설정은 포함하지 않습니다. 전체
snapshot이 필요하면 `bd` 명령이 실행 중이지 않을 때 `.beads` 디렉터리도
복사하거나 server 모드에서 `dolt backup`을 실행하세요.

**단일 클론(원격을 사용하는 개인 포함):**

```bash
bd dolt push                              # 1. 현재 바이너리: 모든 로컬 작업 게시
bd export --all -o .beads/backup/pre-migrate.jsonl   # 2. backup(위 내용 참고)
# 3. 새 바이너리 설치(위 업그레이드 참고)
bd migrate                                # 4. 지정된 migrator로 마이그레이션
bd dolt push                              # 5. 마이그레이션된 schema 게시
bd version                                # 6. 새 버전이 활성화되었는지 확인
```

`bd`의 remote-migrate Gate가 실행을 차단하면 사용 가능한 옵션을 출력하고 운영자의
명시적 결정을 요청합니다. 현재 위치에서 지정된 migrator로 마이그레이션하거나,
원격의 이미 마이그레이션된 데이터베이스를 adopt하거나, fork를 복구할 수 있습니다.
출력되는 안내를 따르세요.

누구도 prompt를 읽지 않는 script 또는 CI 업그레이드에서는
`BD_ALLOW_REMOTE_MIGRATE=1 bd migrate`를 사용하세요. boolean true 값은 모두
작동합니다. 이 명령은 현재 클론을 지정된 migrator로 선언하고 이미 fork된 상태의
검사를 포함한 Gate 전체를 우회하므로, 모든 클론이 아니라 정확히 한 클론의 업그레이드
job에만 연결하세요.

**하나의 원격을 공유하는 여러 클론:**

```bash
# 1. 모든 클론에서 현재(이전) 바이너리로 모든 작업을 게시하고 동기화한 뒤
#    업그레이드가 끝날 때까지 편집 중지
bd dolt push
bd dolt pull

# 2. 지정된 migrator만: backup 후 새 바이너리를 설치하고 마이그레이션하여 게시
bd export --all -o .beads/backup/pre-migrate.jsonl
bd migrate
bd dolt push

# 3. 다른 모든 클론: 새 바이너리를 설치한 뒤 마이그레이션된 데이터베이스 adopt
#    (클론에 대기 중인 마이그레이션이 남아 있어 bd dolt pull이 거부되므로 대신
#    다시 클론. 1단계에서 모든 작업을 이미 push했으므로 안전함)
bd bootstrap
```

`bd bootstrap`은 로컬 데이터베이스를 교체하므로 1단계에서 push하지 않은 작업은
손실됩니다. 이것이 1단계에서 모든 내용을 먼저 게시하는 이유입니다. 클론을 독립적으로
마이그레이션한 뒤 `bd dolt pull`이 `cannot merge because table dependencies has
different primary keys in its common ancestor` 오류와 함께 실패했다면 schema가 이미
fork된 것입니다. 복구 플레이북인
[pk-fork-refused runbook](/recovery/init-safety#pk-fork-refused)을 따르세요.

<Note>
`bd doctor`에는 cache된 원격 ref를 기준으로 fork된 schema를 표시하는
migration-content-skew 검사가 포함됩니다. 업그레이드 후 유용한 검증이며 server와
embedded 모드 모두에서 실행됩니다.
</Note>

## 시대가 다른 버전 간 업그레이드

매우 오래된 `bd` 버전에서 업그레이드한다면 프로젝트가 다른 저장소 backend를 사용할
수 있습니다. `bd`는 여러 저장소 시대를 거쳤습니다.

`.beads/` 아래의 내용을 보고 설치 시대를 식별하세요.

| 시대 | 저장소 배치 |
|---|---|
| SQLite(Dolt 이전, 약 v0.50까지) | `.beads/beads.db` |
| Dolt server | `.beads/dolt/` |
| Embedded Dolt(도입 이후 기본값) | `.beads/embeddeddolt/` |

### v0.63.3 이상(현재 시대)에서 업그레이드

바이너리를 업그레이드하고 다음을 실행하세요.

```bash
bd migrate
```

`bd init`이 Git origin을 Dolt 원격으로 자동 연결하는 기능이 생기기 전에 프로젝트를
초기화했다면 업그레이드 후 원격을 확인하세요.

```bash
bd dolt remote list
```

목록이 비어 있으면 로컬 데이터베이스가 기준인 머신에서 수정하세요.

```bash
bd export -o .beads/issues.pre-remote.jsonl   # 선택적 이슈 감사 export
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
bd dolt push
```

다른 클론에서 `bd bootstrap` 또는 `bd dolt pull`을 실행할 수 있도록 결과로 생긴
`.beads/config.yaml` 변경 사항을 commit하세요.

### v0.59~v0.63.2(이전 embedded)에서 업그레이드

직접 업그레이드가 자동으로 작동합니다.

```bash
# 새 바이너리를 사용하면 변환을 처리함
bd list
```

### v0.50~v0.58(Dolt server 시대)에서 업그레이드

이전 바이너리는 외부 Dolt SQL 서버를 사용했고 새 바이너리는 embedded engine을
사용합니다.

```bash
# 1. 이전 바이너리가 작동할 때 데이터 export
bd list --json -n 0 --all > .beads/issues.jsonl

# 2. Dolt 서버 중지
# dolt sql-server 프로세스 중지(PID 종료, --stop flag는 없음)

# 3. 오래된 서버 metadata와 이전 저장소 디렉터리 제거
rm -f .beads/metadata.json .beads/config.json
rm -rf .beads/dolt .beads/embeddeddolt

# 4. 새 바이너리로 초기화
bd init --from-jsonl --quiet

# 5. 확인
bd list --all
```

### v0.30~v0.50(SQLite 시대)에서 업그레이드

이전 바이너리는 SQLite에 데이터를 저장했고 새 바이너리는 Dolt를 사용합니다.

**권장: 마이그레이션 script 사용**(`sqlite3` 및 `jq` 필요):

```bash
# Beads 저장소에서 script 다운로드
curl -fsSLO https://raw.githubusercontent.com/gastownhall/beads/main/scripts/migrate-sqlite-to-current.sh
chmod +x migrate-sqlite-to-current.sh

# 프로젝트 디렉터리에서 실행
./migrate-sqlite-to-current.sh
```

이 script는 SQLite에서 이슈, 의존성, 레이블을 export하고 유형을 정규화한 뒤 모든
내용을 새 Dolt backend로 import합니다.

**대안: 이전 바이너리로 수동 export.** 이전 바이너리는 항상 [GitHub
Releases](https://github.com/gastownhall/beads/releases)에서 받을 수 있습니다.
프로젝트와 일치하는 버전을 다운로드한 뒤 다음을 실행하세요.

```bash
# 1. 이전 바이너리로 export
./bd-old list --json -n 0 --all > .beads/issues.jsonl

# 2. 현재 바이너리로 import
bd init --from-jsonl --quiet

# 3. 확인
bd list --all
```

> **참고:** 수동 export는 이슈 내용은 보존하지만 의존성과 레이블은 보존하지 않습니다.
> 더 완전하게 이전하려면 마이그레이션 script를 사용하세요.

## 업그레이드 문제 해결

### 오래된 hook

```bash
bd hooks install
```

### 데이터베이스 schema 변경

```bash
bd migrate --dry-run
bd migrate
```

### 업그레이드 후 복구

backup에서 복원해야 한다면 다음을 실행하세요.

```bash
bd init
bd backup restore [path] --force
```

또는 Dolt 원격에서 pull하세요.

```bash
bd dolt pull
```
