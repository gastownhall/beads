---
title: 문제 해결
description: 설치, 데이터베이스와 Dolt 서버, 동기화, git 훅, 의존성, 플랫폼별 문제에서 발생하는 일반적인 bd 문제 해결 방법
---

일반적인 문제와 해결 방법입니다. 단계별 런북은 [복구 섹션](/recovery/index)을 참조하세요.

## 설치 문제

### `bd: command not found`

```bash
# 설치 여부 확인
which bd
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Go bin을 PATH에 추가(~/.bashrc 또는 ~/.zshrc에 추가)
export PATH="$PATH:$(go env GOPATH)/bin"

# 또는 권장 설치 프로그램으로 다시 설치
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### 잘못된 버전의 bd가 실행됨

`bd version`에 예상치 못한 버전(예: 방금 설치한 버전보다 오래된 버전)이 표시된다면
PATH에 `bd` 바이너리가 여러 개 있을 가능성이 큽니다.

```bash
# PATH의 모든 bd 바이너리 확인
which -a bd

# 충돌을 보여 주는 출력 예시:
# /Users/you/go/bin/bd        <- go install에서 설치(이전 버전)
# /opt/homebrew/bin/bd        <- Homebrew에서 설치(최신 버전)

# 이전 go install 버전 제거
rm ~/go/bin/bd

# 또는 mise가 관리하는 Go 설치 제거
rm ~/.local/share/mise/installs/go/*/bin/bd

# 확인
which bd
bd version
```

이전 `go install`로 설치한 바이너리가 `~/go/bin/`에 있고 최신 패키지 관리자 설치보다
PATH에서 앞설 때 발생합니다. 한 가지 설치 방법(Homebrew 권장)을 선택해 계속 사용하세요.

### macOS의 `zsh: killed bd`

CGO/SQLite 호환성 문제입니다.

```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest

# 또는 소스에서 빌드하는 경우
git clone https://github.com/gastownhall/beads
cd beads
CGO_ENABLED=1 go build -tags gms_pure_go -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

Homebrew 빌드는 이미 CGO를 활성화하므로 이 작업이 필요하지 않습니다. Homebrew
버전에서도 충돌이 계속 발생하면 [이슈를 제출](https://github.com/gastownhall/beads/issues)하세요.

### 권한 거부

```bash
chmod +x $(which bd)

# 또는 사용자 디렉터리에 설치
mkdir -p ~/.local/bin
mv bd ~/.local/bin/
export PATH="$HOME/.local/bin:$PATH"
```

### 바이러스 백신이 bd를 악성 코드로 표시

Kaspersky, Windows Defender 등이 `bd`를 일반 트로이 목마로 표시할 수 있습니다. 이는
**오탐**입니다. Go 바이너리는 바이러스 백신 휴리스틱을 흔히 트리거합니다. 예외를
추가하기 전에 [GitHub 릴리스 페이지](https://github.com/gastownhall/beads/releases)와
대조해 바이너리의 SHA256 체크섬을 확인하세요. 공급업체별 지침은
[바이러스 백신 오탐](/reference/antivirus)을 참조하세요.

## 데이터베이스 문제

### 데이터베이스를 찾을 수 없음

```bash
# beads 초기화
bd init --quiet

# 또는 bd가 기존 .beads 디렉터리를 가리키게 함
BEADS_DIR=/path/to/.beads bd list
```

### 데이터베이스 잠김

```bash
# Dolt 서버가 실행 중이면 중지(서버 모드)
bd dolt stop

# 멈춘 bd 프로세스를 찾아 종료
ps aux | grep bd
kill <pid>

# 다시 시도
bd list
```

<Warning>
`.dolt/` 디렉터리 안의 파일(`noms/LOCK` 포함)을 제거하지 **마세요**. Dolt 내부
파일이며 제거하면 복구할 수 없는 데이터 손상이 **발생합니다**. Dolt가 직접 관리합니다.
</Warning>

동시성이 높은 시나리오(여러 에이전트)에서는 서버 모드(`bd init --server`)가
`dolt sql-server`를 통해 동시 접근을 네이티브로 처리합니다.

### `bd init`이 실행을 거부함

`bd init`과 `bd dolt`는 로컬 또는 원격 기록을 파괴할 수 있는 작업을 거부하고
`init-local-exists` 또는 `pk-fork-refused` 같은 패턴 코드를 표시합니다. 코드마다
런북이 있습니다. [복구 플레이북](/recovery/init-safety)을 참조하세요. 기존 데이터 위에
다시 초기화하려면 먼저 내보내세요(`bd export -o backup.jsonl`).

### 손상된 데이터베이스

**논리적 일관성 문제**(ID 충돌, 잘못된 접두사)와 **물리적 데이터베이스 손상**(디스크
장애, 전원 손실, 파일 시스템 오류)을 구분하세요.

논리적 일관성 문제의 경우 — 손상이 아닙니다.

```bash
bd doctor --fix
```

물리적 손상은 Dolt 원격 또는 백업에서 다시 빌드합니다.

```bash
# 손상된 데이터 디렉터리를 옆으로 이동:
mv .beads/embeddeddolt .beads/embeddeddolt.backup   # 임베디드 모드(기본값)
mv .beads/dolt .beads/dolt.backup                   # 서버 모드

bd init
bd dolt pull    # 구성된 경우 Dolt 원격에서 풀

# 또는 백업에서 복원:
# bd backup restore [path] --force
```

전체 런북은 [데이터베이스 손상](/recovery/database-corruption)을 참조하세요.

### 다시 시작 후 Dolt 저널 손상

**증상(서버 모드):** 시스템을 다시 시작한 후 `bd`는 Dolt 서버가 시작되었지만 연결을
수락하지 않는다고 보고하며 `.beads/dolt-server.log`에 다음이 포함됩니다.

```text
possible data loss detected in journal file at offset ...: corrupted journal
```

**원인:** 비정상 종료 후 Dolt가 손상된 저널 블록을 감지했습니다. 오래된 PID, 포트 또는
잠금 파일과는 다릅니다. `bd`는 Dolt의 데이터 손실 복구 모드를 자동으로 실행하지 않습니다.

**원격이 최신일 때 안전한 복구:**

```bash
# 서버 모드 데이터는 .beads/dolt, 임베디드 모드는 .beads/embeddeddolt에 저장
mv .beads/dolt .beads/dolt.corrupt.$(date +%Y%m%dT%H%M%S)
bd bootstrap --dry-run
bd bootstrap --yes
bd stats
```

원격이 오래되었을 수 있다면 포렌식을 위해 손상된 디렉터리를 유지하고
`dolt fsck --revive-journal-with-data-loss`를 고려하기 전에 `dolt fsck`로 검사하세요.
Dolt의 데이터 손실 경고를 검토한 뒤에만 복구 경로를 사용하세요.

### `failed to import: issue already exists`

기존 이슈와 충돌하는 이슈로 데이터베이스를 부트스트랩하려고 합니다. 로컬 데이터베이스를
지우고 내보내기에서 다시 초기화하세요.

```bash
# 로컬 데이터베이스를 파괴함 — 확실하지 않으면 먼저 내보내기
rm -rf .beads/embeddeddolt   # 임베디드 모드(기본값)
rm -rf .beads/dolt           # 서버 모드

bd init --from-jsonl
```

### 부모 누락 오류로 가져오기 실패

JSONL에서 부트스트랩하거나 계층적 이슈(예: `bd-abc.1`)를 풀할 때
`parent issue bd-abc does not exist` 같은 오류가 발생하면 부모 이슈가 삭제되었지만
자식이 여전히 참조한다는 뜻입니다. 보통 부모에 `bd delete`를 실행했거나, 한쪽에서
부모를 삭제한 브랜치 병합 또는 불완전한 가져오기 후에 발생합니다.

가져오기는 기본적으로 검증 없이 고아를 허용하므로 자식은 계속 들어옵니다. 오류는 부모
자체가 사라졌음을 나타냅니다. 가져온 뒤 부모를 다시 만들거나 고아 자식을 닫으세요.

**예방:** `bd delete --cascade`로 자식도 함께 삭제하고 먼저
`bd children <parent-id>`로 자식을 검토하세요.

### 초기화 후 이전 데이터가 돌아옴

`bd admin reset --force`는 **로컬** beads 데이터만 제거합니다. 구성된 Dolt 원격이나
초기화 후 푸시하는 다른 머신에서 이전 이슈가 돌아올 수 있습니다. 완전히 깨끗하게
시작하려면 `bd init`을 다시 실행하기 전에 모든 클론을 초기화하거나 원격의 beads
데이터를 지우세요.

제거된 레거시 동기화 브랜치 기능을 사용했다면 해당 브랜치와 워크트리도 삭제하세요.
[워크트리: 레거시 정리](/reference/worktrees#legacy-cleanup)를 참조하세요.

### 데이터베이스에 데이터가 있지만 `bd`에 이슈 0개가 표시됨

**증상(서버 모드):** 데이터가 존재하지만 모든 `bd` 명령이 빈 결과를 반환합니다.

**원인:** `bd`가 예상과 다른 Dolt 서버 또는 데이터베이스, 즉 잘못된 서버의 빈 "섀도"
데이터베이스에 연결하고 있습니다.

**진단:**

```bash
# bd가 사용하는 모드와 서버 확인
cat .beads/metadata.json | grep -E "dolt_mode|dolt_server_port"

# 서버 모드 상태 검사 실행
bd doctor --server

# 연결된 데이터베이스 내용 확인
bd sql 'SELECT COUNT(*) FROM issues'
```

**해결:** Dolt 서버가 올바른 데이터 디렉터리에서 실행되고 `metadata.json`이 올바른
서버와 포트를 가리키는지 확인하세요. 외부 서버 구성과 함께 오래된 `.beads/dolt/`
디렉터리가 있으면 실제 데이터베이스를 가릴 수 있습니다. 오래된 디렉터리를 제거하기
전에 실제 데이터가 서버에 있는지 확인하세요.

### 구성된 서버에 연결할 수 없음(자동 시작 비활성화)

**증상(서버 모드):** 구성된 서버가 중지되면 `bd`가 "database not found on Dolt server"를
반환합니다.

**원인:** `metadata.json`에 명시적 `dolt_server_port`가 있으면 bd는 서버를 외부 관리로
취급하고 자동 시작을 의도적으로 비활성화합니다. 다른 서버를 생성하면 섀도 데이터베이스가
만들어지기 때문입니다.

**해결:**

```bash
# 구성된 Dolt 서버 시작
bd dolt start

# 또는 올바른 데이터 디렉터리로 수동 시작
dolt sql-server --host 127.0.0.1 --port 3307 --data-dir /path/to/your/dolt/data
```

자동 시작 동작을 원하면 `.beads/metadata.json`에서 `dolt_server_port`를 제거하세요.

### 여러 프로젝트의 포트 충돌

**증상(서버 모드):** 두 번째 프로젝트의 명령이 실패하거나 잘못된 데이터베이스에
연결되며 여러 `dolt sql-server` 프로세스가 실행 중입니다.

**원인:** 기본적으로 각 서버 모드 프로젝트가 자체 Dolt 서버를 시작하므로 프로젝트가 많은
머신에서 충돌할 수 있습니다.

**해결:** 모든 프로젝트가 단일 Dolt 서버를 사용하도록 공유 서버 모드를 활성화합니다.

```bash
# 옵션 1: 머신 전체(~/.bashrc 또는 ~/.zshrc에 추가)
export BEADS_DOLT_SHARED_SERVER=1

# 옵션 2: 프로젝트별
bd config set dolt.shared-server true
```

활성화 후 기존 프로젝트는 공유 서버에 데이터베이스를 만들기 위해
`bd init --reinit-local -q`가 필요할 수 있습니다.

**확인:** 어느 프로젝트에서든 `bd dolt status`에 같은 서버, 3308 포트,
`~/.beads/shared-server/` 데이터 디렉터리가 표시되어야 합니다.

### 여러 데이터베이스 감지 경고

bd는 디렉터리 계층에서 `.beads` 디렉터리를 둘 이상 찾으면 경고하고 사용 중인 항목
(보통 현재 디렉터리에 가장 가까운 항목)을 `▶`로 표시합니다. 데이터베이스가 여러 개면
잘못된 데이터베이스에서 작업하거나 같은 작업을 두 번 추적할 위험이 있습니다.

- **중첩 프로젝트(의도적):** 지원됩니다. 활성 데이터베이스를 확인하거나 명시적으로 고정하세요.
- **우발적 중복:** 원하지 않는 데이터베이스에서 내보낸 뒤(`bd export -o issue-export.jsonl`)
  해당 `.beads` 디렉터리를 제거합니다.
- **선택 재정의:**

  ```bash
  # bd가 특정 .beads 디렉터리를 가리키게 함(권장)
  export BEADS_DIR=/path/to/.beads

  # 레거시 방법(사용 중단, 데이터베이스 파일을 직접 가리킴)
  export BEADS_DB=/path/to/db
  ```

<a id="circuit-breaker-server-appears-down-failing-fast"></a>

### 회로 차단기: "server appears down, failing fast"

**증상(서버 모드):** 모든 `bd` 명령이
`dolt circuit breaker is open: server appears down, failing fast (cooldown 30s)`와
함께 실패하며 반복 호출해도 지속됩니다.

**원인:** 반복된 연결 실패 후 회로 차단기가 작동했습니다. 상태는
`/tmp/beads-circuit/` 아래의 파일(`beads-dolt-circuit-<host>-<port>[-<db>].json`,
host:port를 키로 사용)에 저장되며 모든 `bd` 프로세스가 공유합니다. 작동하면 성공한
프로브가 재설정할 때까지 해당 host:port의 모든 명령이 거부됩니다.

beads 관리 로컬 서버에서 `bd dolt status`는 서버 PID 파일을 바탕으로 보고합니다.
"running" 상태가 서버가 실제로 예상 포트에서 연결을 수락한다는 보장은 아닙니다.

**진단:**

```bash
# 회로 차단기 상태 확인
cat /tmp/beads-circuit/beads-dolt-circuit-*.json

# Dolt 서버가 실제로 수신 중인지 확인
lsof -i :<port>

# 구성된 포트와 실행 중인 포트 비교
cat .beads/metadata.json | grep port
```

**해결:**

```bash
rm /tmp/beads-circuit/beads-dolt-circuit-*.json
bd dolt stop
bd dolt start
bd list
```

macOS에서 `/tmp`는 `/private/tmp`의 심볼릭 링크이며 다시 시작할 때 항상 지워지지는
않습니다. 상태 파일이 재부팅 후에도 유지될 수 있습니다.

## Dolt 서버 문제

### 서버가 시작되지 않음

```bash
# 서버 상태 확인
bd doctor

# 서버 로그 확인(서버 모드, 임베디드 모드는 프로세스 내부 실행으로 서버 로그 없음)
cat .beads/dolt-server.log

# 서버 다시 시작
bd dolt stop
bd dolt start
```

### 버전 불일치

bd를 업그레이드한 뒤:

```bash
bd dolt stop
bd dolt start
```

## 동기화 문제

### 변경 사항이 동기화되지 않음

```bash
# Dolt 원격에 강제 푸시
bd dolt push

# 훅 확인
bd hooks list
```

### 백업에서 복구

```bash
# Dolt 백업에서 복원
bd backup restore [path] --force

# 또는 Dolt 원격에서 풀
bd dolt pull
```

<a id="merge-conflicts"></a>

### 병합 충돌

Dolt는 셀 수준에서 병합하므로 동시 변경 사항이 같은 이슈의 같은 필드를 건드릴 때만
충돌합니다. 해시 기반 ID 덕분에 서로 다른 이슈의 ID는 충돌하지 않습니다.

```bash
# Dolt 충돌 확인 및 수정
bd doctor --fix

# 다시 푸시
bd dolt push
```

전체 런북은 [병합 충돌](/recovery/merge-conflicts)을 참조하세요.

## Git 훅 문제

### 훅이 실행되지 않음

```bash
# 설치 여부 확인
ls -la .git/hooks/

# 다시 설치
bd hooks install
```

### 훅 오류

```bash
# 훅 스크립트 확인
cat .git/hooks/pre-commit

# 수동 실행
.git/hooks/pre-commit
```

### 훅 시간 제한으로 연결된 pre-commit 훅 종료

**증상:** `bd hooks install` 후 연결된 pre-commit 훅(eslint, prettier, ruff 등)이
다음 메시지와 함께 실행을 중지합니다.
`beads: hook 'pre-commit' timed out after 300s -- continuing without beads`.

**원인:** beads 훅 shim이 `bd hooks run`을 OS 수준 시간 제한으로 감쌉니다.
`bd hooks run`이 내부에서 원본 훅에 연결되므로 시간 제한은 beads 자체 작업과 전체 훅
파이프라인을 모두 포함합니다.

**해결:** 시간 제한(기본값 300초)을 늘립니다.

```bash
# ~/.bashrc 또는 ~/.zshrc에 추가
export BEADS_HOOK_TIMEOUT=600  # 10분(초 단위)
```

### git 훅 권한 거부

Git 훅에는 실행 권한이 필요합니다.

```bash
chmod +x .git/hooks/pre-commit
chmod +x .git/hooks/post-merge
chmod +x .git/hooks/post-checkout
```

### 손상된 심볼릭 링크 `CLAUDE.md`

**증상:** Git은 `CLAUDE.md`를 심볼릭 링크 항목(모드 `120000`)으로 보고하지만 색인된
blob에는 한 줄짜리 심볼릭 링크 대상 대신 여러 줄 Markdown이 들어 있습니다. macOS에서는
이로 인해 클론 또는 체크아웃이 실패할 수 있습니다.

이전 설정 동작으로 손상된 저장소에 영향을 줍니다([#4192](https://github.com/gastownhall/beads/pull/4192)에서
수정됨). 기존의 잘못된 색인 항목을 복구하려면 다음을 실행합니다.

```bash
# 잘못된 항목 확인: 모드 120000이지만 Markdown 내용
git ls-files -s CLAUDE.md
git cat-file -p :CLAUDE.md | sed -n '1,5p'

# 내용을 바꾸지 않고 blob을 일반 추적 파일로 변환
sha=$(git rev-parse :CLAUDE.md)
git update-index --cacheinfo 100644,$sha,CLAUDE.md
git checkout-index -f -- CLAUDE.md

# 확인: 이제 첫 열이 100644여야 함
git ls-files -s CLAUDE.md
git diff -- CLAUDE.md
```

검토 후 모드 복구를 커밋하세요.

### "Branch already checked out" 또는 예상치 못한 `.git/beads-worktrees/`

이전 beads 버전은 현재 제거된 동기화 브랜치 기능을 위해 숨겨진 git 워크트리를
생성했습니다. 남은 항목이 브랜치를 잠글 수 있습니다(`fatal: 'main' is already checked out
at .../beads-worktrees/...`). 다음과 같이 제거하세요.

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

[워크트리: 레거시 정리](/reference/worktrees#legacy-cleanup)를 참조하세요.

## 의존성 문제

### 열린 이슈가 있지만 `bd ready`에 아무것도 표시되지 않음

해당 이슈에 열린 차단 요소가 있을 가능성이 큽니다.

```bash
# 차단된 이슈 보기
bd blocked

# 의존성 트리 표시(기본 최대 깊이: 50)
bd dep tree <issue-id>
bd dep tree <issue-id> --max-depth 10

# 필요하면 차단 의존성 제거
bd dep remove <from-id> <to-id>
```

`blocks` 의존성만 준비된 작업에 영향을 준다는 점을 기억하세요.

### 순환 의존성

bd는 준비된 작업 감지를 깨뜨리는 의존성 순환을 방지합니다.

```bash
# 순환 감지
bd dep cycles

# 의존성 하나 제거
bd dep remove bd-A bd-B
```

전체 런북은 [순환 의존성](/recovery/circular-dependencies)을 참조하세요.

### 의존성이 표시되지 않음

```bash
# 의존성을 포함한 전체 이슈 세부 정보 표시
bd show <issue-id>

# 의존성 트리 시각화
bd dep tree <issue-id>
```

의존성 유형마다 의미가 다릅니다. `blocks`만 준비된 작업을 차단합니다.
[의존성](/core-concepts/dependencies)을 참조하세요.

## 성능 문제

### 느린 쿼리

```bash
# 데이터베이스 통계 확인
bd stats

# 디스크 크기 확인
du -sh .beads/embeddeddolt   # 임베디드 모드(기본값)
du -sh .beads/dolt           # 서버 모드

# 압축 후보 미리 보기
bd admin compact --dry-run --all

# 크면 압축
bd admin compact --analyze
```

매우 큰 프로젝트는 여러 데이터베이스로 분할하는 방안을 고려하세요.

```bash
cd ~/project/component1 && bd init --prefix comp1
cd ~/project/component2 && bd init --prefix comp2
```

### 높은 메모리 사용량

```bash
# Dolt 가비지 컬렉션을 실행해 저장소 압축
bd admin compact --dolt
```

## 에이전트 문제

### 에이전트가 중복 이슈를 생성함

에이전트는 이슈가 이미 존재한다는 사실을 모를 수 있습니다. 예방 전략은 다음과 같습니다.

- 에이전트가 먼저 검색하게 하기: `bd list --json | grep "title"`
- 자동 생성 이슈에 레이블 지정: `bd create "..." -l auto-generated`
- 중복 통합: `bd duplicate <dup-id> --of <canonical-id>`가 정식 이슈 참조와 함께
  중복 이슈를 닫음

### 에이전트가 복잡한 의존성으로 혼란스러워함

의존성 구조를 단순화합니다.

```bash
# 지나치게 복잡한 트리 확인
bd dep tree <issue-id>

# 불필요한 의존성 제거
bd dep remove <from-id> <to-id>

# 느슨한 관계에는 의존성 대신 레이블 사용
bd label add <issue-id> related-to-feature-X
```

### MCP 서버가 작동하지 않음

```bash
# MCP 서버 설치 확인
pip list | grep beads-mcp

# MCP 구성 확인(macOS의 Claude Desktop)
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json

# CLI 자체가 작동하는지 테스트
bd version
bd ready
bd doctor
```

설정과 구성은 [MCP 서버](/integrations/mcp-server)를 참조하세요.

### 샌드박스 환경(Codex, Claude Code 등)

프로세스와 네트워크 권한을 제한하는 샌드박스는 bd가 Dolt 서버를 제어하지 못하게 할 수
있으며, 이로 인해 "database out of sync" 오류가 계속되거나 `bd dolt stop`이
"operation not permitted" 오류로 실패할 수 있습니다.

bd는 샌드박스 환경을 자동 감지하고 `Sandbox detected, using direct mode`를 표시합니다.
자동 감지에 실패하면 전역 `--sandbox` 플래그를 명시적으로 전달하세요.

```bash
bd --sandbox ready
bd --sandbox create "버그 수정" -p 1
```

샌드박스 모드는 Dolt 자동 푸시를 비활성화해 서버 제어나 네트워크 접근 없이 bd가
작동하게 합니다. 샌드박스 밖에서 수동으로 한 번 동기화하세요.

```bash
bd dolt push
```

오래된 상태로 인한 오류가 지속되면 `bd doctor --fix`가 메타데이터 새로 고침을
강제합니다(위험이 낮습니다. 이슈가 아니라 추적 메타데이터를 업데이트합니다). 배경:
[GH#353](https://github.com/gastownhall/beads/issues/353).

## 플랫폼별 문제

### Windows: 경로 문제

```pwsh
# bd.exe가 PATH에 있는지 확인
where.exe bd

# Go bin을 PATH에 추가(영구적)
[Environment]::SetEnvironmentVariable(
    "Path",
    $env:Path + ";$env:USERPROFILE\go\bin",
    [EnvironmentVariableTarget]::User
)

# 현재 세션에서 PATH 다시 로드
$env:Path = [Environment]::GetEnvironmentVariable("Path", "User")
```

### Windows: 방화벽이 Dolt 서버를 차단

서버 모드에서 Dolt 서버는 루프백 TCP를 수신합니다. Windows 방화벽에서 `bd.exe`를
허용하세요. Windows 보안(Windows Security) → 방화벽 및 네트워크 보호(Firewall & network protection) →
방화벽을 통해 앱 허용(Allow an app through firewall) → 개인 네트워크(Private networks)에 `bd.exe`를 추가합니다.

### Windows: 제어된 폴더 액세스(Controlled Folder Access)가 `bd init`을 차단

**증상:** `bd init`이 높은 CPU 사용량과 함께 무기한 멈추고 CTRL+C가 작동하지 않습니다.
제어된 폴더 액세스(Controlled Folder Access)가 알림 없이 bd를 차단할 수 있어 `-v` 플래그 없이는 진단하기
어렵습니다.

```pwsh
bd init -v
# Error: failed to create .beads directory: mkdir .beads: The system cannot find the file specified
```

**해결:** `bd.exe`를 허용 목록에 추가하세요. Windows 보안(Windows Security) → 바이러스 및 위협
방지(Virus & threat protection) → 랜섬웨어 방지(Ransomware protection) → 제어된 폴더 액세스(Controlled folder access) →
제어된 폴더 액세스를 통해 앱 허용(Allow an app through Controlled folder access) → `bd.exe`(일반적으로
`%USERPROFILE%\go\bin\bd.exe`)를 찾아 선택합니다. 그런 다음 `bd init`을 다시 시도하세요.

### macOS: Gatekeeper가 실행을 차단

1. 다운로드한 바이너리 체크섬이 릴리스 `checksums.txt`와 일치하는지 확인합니다.
2. `scripts/install.sh`를 사용했다면 macOS 임시 재서명은 선택 사항
   (`BEADS_INSTALL_RESIGN_MACOS=1`)임을 유의하세요.
3. 바이너리를 승인합니다.

```bash
# 격리 속성 제거
xattr -d com.apple.quarantine /usr/local/bin/bd

# 또는: 시스템 환경설정(System Preferences) → 보안 및 개인 정보 보호(Security & Privacy) → 일반(General) → "그래도 허용(Allow anyway)"
```

## 디버그 환경 변수

bd는 특정 하위 시스템 디버깅용 환경 변수를 지원합니다. 문제를 해결할 때 또는 유지
관리자가 요청할 때 활성화하세요.

| 변수 | 용도 | 출력 |
|----------|---------|--------|
| `BD_DEBUG` | 일반 디버그 로깅 | stderr |
| `BD_DEBUG_RPC` | CLI와 Dolt 서버 간 RPC 통신 | stderr |
| `BD_DEBUG_SYNC` | 동기화 및 가져오기 타임스탬프 보호 | stderr |
| `BD_DEBUG_ROUTING` | 이슈 라우팅 및 다중 저장소 해결 | stderr |
| `BD_DEBUG_FRESHNESS` | 데이터베이스 파일 교체 감지 | 서버 로그 |

활성화하려면 `1`로 설정하고 비활성화하려면 `unset`하세요.

```bash
# 일반 디버깅
BD_DEBUG=1 bd ready

# 디버그 출력을 파일로 캡처
BD_DEBUG=1 bd dolt push 2> debug.log

# 동기화 타임스탬프 보호, 예:
# [debug] Protected bd-123: local=2024-01-20T10:00:00Z >= incoming=2024-01-20T09:55:00Z
BD_DEBUG_SYNC=1 bd dolt push

# 최신 상태 출력은 stderr가 아니라 서버 로그로 이동(서버 모드)
BD_DEBUG_FRESHNESS=1 bd dolt start
tail -f .beads/dolt-server.log | grep freshness
```

다중 저장소 라우팅 구성은 [라우팅](/multi-agent/routing)을 참조하세요.

## 도움받기

### 디버그 출력

```bash
bd --verbose list
```

### 로그

```bash
# 서버 모드(임베디드 모드는 프로세스 내부에서 실행되어 서버 로그 없음)
cat .beads/dolt-server.log
```

### 시스템 정보

```bash
bd info --json
```

### 이슈 제출

```bash
# 다음 정보 포함
bd version
bd info --json
uname -a
```

신고 위치: https://github.com/gastownhall/beads/issues — 또는
[GitHub Discussions](https://github.com/gastownhall/beads/discussions)에서 질문하세요.
