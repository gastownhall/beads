---
title: 동기화 설정 가이드
description: "여러 머신에서 이슈 데이터를 이어서 사용할 수 있도록 Dolt 동기화 설정: 원격, 클론 bootstrap, 일상적인 push와 pull"
---

Dolt 동기화로 Beads를 설정하여 여러 컴퓨터에서 이슈를 이어서 사용하세요.

## 사전 요구 사항

모든 머신에 다음 두 도구를 설치해야 합니다.

| 도구 | 최소 버전 | 설치 |
|------|-----------------|---------|
| **bd**(Beads CLI) | 0.59.0 이상 | [설치](/getting-started/installation) 참고 |
| **Dolt** | 2.2.0 이상 | `brew install dolt` 또는 [Dolt 설치 script](https://github.com/dolthub/dolt/releases/latest/download/install.sh) |

두 도구가 모두 설치되었는지 확인합니다.

```bash
bd version     # 0.59.0 이상이어야 함
dolt version   # 2.2.0 이상이어야 함
```

## 초기 설정(첫 번째 컴퓨터)

### 1. Beads 초기화

```bash
cd your-project
bd init
```

이 명령은 Dolt 데이터베이스가 있는 `.beads/` 디렉터리를 생성합니다. Git 저장소에
`origin` 원격이 있으면 `bd init`은 같은 Git URL을 가리키는 `origin`이라는 Dolt
원격도 설정합니다. Dolt는 일반 소스 브랜치와 별도로 `refs/dolt/data` 아래에 이슈
데이터를 저장합니다.

### 2. 이슈 생성

```bash
bd create "CI 파이프라인 설정" -p 1 -t task
bd create "인증 추가" -p 2 -t feature
bd list
```

### 3. Dolt 원격 확인 또는 추가

`origin`이 있는 일반 Git 저장소라면 이미 설정되어 있어야 합니다.

```bash
bd dolt remote list
# 예상 결과: origin  <Git origin URL>
```

초기화할 때 저장소에 `origin`이 없었다면 동기화를 위해 Beads가 Git 원격을 가리키도록
설정합니다.

```bash
# GitHub(SSH 권장)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# GitHub(HTTPS)
bd dolt remote add origin git+https://github.com/org/repo.git

# 기타 옵션: DoltHub, S3, GCS, 로컬 경로
# 모든 원격 유형은 DOLT.md 참고
```

### 4. 이슈 push

```bash
bd dolt push
```

push가 성공했는지 확인합니다.

```bash
git ls-remote origin | grep dolt
# 예상 결과: <hash>  refs/dolt/data
```

## Dolt 원격이 없는 기존 프로젝트

이전 버전의 `bd init`으로 초기화한 프로젝트에는 로컬 embedded Dolt 데이터베이스와
commit된 `.beads/issues.jsonl`이 있지만 Dolt 원격은 없을 수 있습니다. 로컬
데이터베이스가 기준인 머신에서 다음과 같이 수정하세요.

```bash
bd dolt remote list
bd export -o .beads/issues.pre-remote.jsonl   # 선택적 이슈 감사 export
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
bd dolt push
```

`bd dolt remote add origin ...`은 `.beads/config.yaml`에 `sync.remote`를 씁니다.
일반 Git 워크플로로 이 설정 파일을 commit하고 push하세요. 다른 클론에서는
데이터베이스가 없거나 오래된 경우 `bd bootstrap`을 실행하고, 이미 올바른
데이터베이스가 있으면 `bd dolt pull`을 실행할 수 있습니다.

## 새 컴퓨터에 클론

원격에 Beads 데이터가 이미 있는 저장소를 클론할 때 일반 `git clone`은
`refs/dolt/data`를 가져오지 **않습니다**. Dolt 데이터베이스를 bootstrap해야 합니다.

### 빠른 방법: bd bootstrap

최신 버전의 `bd`에서는 `bd bootstrap`이 모든 작업을 자동으로 처리합니다.

```bash
git clone git@github.com:org/repo.git
cd repo

bd bootstrap
```

`bd bootstrap`은 origin의 `refs/dolt/data`를 자동 감지하고 Dolt 데이터베이스를
클론한 뒤 원격을 설정합니다. 다음 명령으로 확인하세요.

```bash
bd list       # 이슈가 표시되어야 함
bd history    # 최근 이슈 기록이 표시되어야 함
```

`bd bootstrap`이 성공하면 설정이 끝났습니다. [일상적인 동기화](#day-to-day-sync)로 이동하세요.

<a id="manual-path-if-bootstrap-fails"></a>

### 수동 방법(bootstrap 실패 시)

`bd bootstrap`이 작동하지 않으면(이전 `bd` 버전, 특수한 원격 설정) 다음 단계를 따르세요.

**1단계: 원격에 Beads 데이터가 있는지 확인**

```bash
git ls-remote origin | grep dolt
# 예상 결과: <hash>  refs/dolt/data
# 없으면 원격에 Beads 데이터가 없는 것이므로 일반적으로 bd init 사용
```

**2단계: Beads 초기화**

```bash
bd init
```

이 명령은 빈 데이터베이스가 있는 `.beads/`를 생성합니다. 빈 데이터베이스는
수동으로 교체할 것이므로 `bd bootstrap` 관련 경고는 무시하세요.

**3단계: Dolt 서버 중지**

```bash
bd dolt stop
```

**4단계: 데이터베이스 이름 확인 및 빈 데이터베이스 제거**

```bash
# 데이터베이스 이름 확인
cat .beads/metadata.json    # "dolt_database" 찾기
```

`dolt_database` 필드가 `<dbname>`입니다. 보통 저장소 이름과 같습니다.

```bash
# 빈 데이터베이스 제거
rm -rf .beads/dolt/<dbname>/
```

**5단계: 원격에서 Dolt 데이터 클론**

```bash
cd .beads/dolt
dolt clone git@github.com:org/repo.git <dbname>
cd ../..
```

**6단계: 서버 시작 및 마이그레이션**

```bash
bd dolt start
bd migrate --yes
```

**7단계: 원격 등록 확인**

```bash
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
```

`remote already exists`가 표시되어도 괜찮습니다. `dolt clone`이 이미 설정한 것입니다.

**8단계: 확인**

```bash
bd dolt remote list   # origin이 표시되어야 함
bd list               # 이슈가 표시되어야 함
```

<a id="day-to-day-sync"></a>

## 일상적인 동기화

두 머신의 설정이 끝나면 두 명령으로 동기화할 수 있습니다.

```bash
# 변경 사항을 원격에 push
bd dolt push

# 원격의 변경 사항을 pull
bd dolt pull
```

### 일반적인 워크플로

```
머신 A                            머신 B
─────────                          ─────────
bd create "새 작업" -p 1
bd dolt push
                                   bd dolt pull
                                   bd update bd-a1b2 --claim
                                   bd close bd-a1b2 --reason "완료"
                                   bd dolt push
bd dolt pull
bd list                            # 종료된 작업 확인
```

### 중요 규칙

- **항상 `bd dolt ...` 명령 사용** — Dolt 서버가 실행 중일 때 원시 `dolt` CLI 명령을 실행하지 마세요. journal이 손상됩니다.
- **pull 전에 commit** — working set에 commit하지 않은 변경 사항이 있으면 `bd dolt pull`이 `cannot merge with uncommitted changes` 오류와 함께 실패합니다. 먼저 `bd dolt commit`을 실행하세요.
- **머신 전환 전에 push** — push하지 않은 변경 사항은 로컬에만 있습니다.
- **JSONL을 동기화에 사용하지 않기** — `.beads/issues.jsonl`은 뷰어와 교환을 위한 내보내기입니다. 단일 원본이나 전체 데이터베이스 백업이 아니며 삭제 또는 prune을 안전하게 조정할 수 없습니다.

## 문제 해결

### push 시 "no common ancestor"

이전 데이터베이스의 오래된 `refs/dolt/data`가 충돌합니다. 삭제한 뒤 다시 시도하세요.

```bash
git update-ref -d refs/dolt/data
bd dolt push
```

### pull 시 "cannot merge with uncommitted changes"

먼저 working set을 commit하세요.

```bash
bd dolt commit
bd dolt pull
```

### push 또는 commit 시 "no store available"

이 문제는 `bd` 0.59.0 미만 버전의 버그였습니다. `bd`를 업그레이드하세요.

```bash
brew upgrade beads
# 또는 설치 script 다시 실행
```

### 클론 후 bd list에 아무것도 표시되지 않음

Dolt 데이터베이스가 bootstrap되지 않았습니다. `bd bootstrap`을 실행하거나 위의
[수동 방법](#manual-path-if-bootstrap-fails)을 따르세요.

### 비정상 종료 후 오래된 lock 파일

```bash
bd doctor --fix --yes
```

**경고**: `.dolt/` 디렉터리 안의 파일(`noms/LOCK` 포함)을 수동으로 제거하지
마세요. Dolt 내부 파일이므로 제거하면 **복구할 수 없는 데이터 손상**이 발생합니다.
Dolt가 이 파일을 직접 관리합니다.

### "fatal: Unable to read current working directory"

Dolt 서버의 작업 디렉터리가 더 이상 존재하지 않습니다. 브랜치 전환 후 흔히
발생합니다. 서버를 다시 시작하세요.

```bash
bd dolt stop
bd dolt start
```

## 관련 문서

- [동기화 개념](/core-concepts/sync-concepts) — 이 설정의 개념 모델(Dolt가 단일 원본인 이유와 JSONL의 용도)
- [빠른 시작](/getting-started/quickstart) — Beads 시작하기
- [Beads용 Dolt backend](/architecture/dolt) — Dolt backend 상세 정보, 서버 모드, federation, 원격 유형, 동기화 모드
- [설치](/getting-started/installation) — 모든 플랫폼의 설치 방법

## 출처

이 가이드는 수동 bootstrap 워크플로를 포함한 전체 설정 및 동기화 절차를 문서화한
[@leonletto](https://github.com/leonletto)의 커뮤니티 설정 가이드
[leonletto.github.io/thrum](https://leonletto.github.io/thrum/docs.html#guides/beads-setup.html)에서
영감을 받았습니다. Beads 커뮤니티에 기여해 주셔서 감사합니다.
