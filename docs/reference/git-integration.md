---
title: Git 통합
description: 훅 설치, 외부 훅 관리자, 워크트리, 브랜치 워크플로를 포함해 bd가 호스팅과 훅에 git을 사용하는 방식
---

beads와 git의 통합 방식을 설명합니다.

## 개요

Beads는 git을 다음 용도로 사용합니다.
- **프로젝트 호스팅** - 코드 저장소가 beads 구성도 호스팅
- **훅** - git 작업 시 자동 동기화

데이터 저장과 동기화는 버전 관리 SQL 데이터베이스인 Dolt가 처리합니다. 이슈 데이터가
머신 간에 이동하는 방식은 [동기화 개념](/core-concepts/sync-concepts)을 참조하세요.

## 파일 구조

```
.beads/
├── config.yaml        # 프로젝트 구성(git에서 추적)
├── metadata.json      # 백엔드 메타데이터(git에서 추적)
├── .gitignore         # bd init이 작성(git에서 추적)
├── embeddeddolt/      # Dolt 데이터베이스 — 임베디드 모드, 기본값(git에서 무시)
└── dolt/              # Dolt 데이터베이스 — 서버 모드(git에서 무시)
```

`bd init`은 데이터베이스 디렉터리와 런타임 파일이 git에 들어가지 않도록
`.beads/.gitignore`를 작성하므로 수동 gitignore 규칙이 필요 없습니다. 데이터베이스
디렉터리(`.beads/embeddeddolt/` 또는 `.beads/dolt/`)를 git이나 Git LFS로 추적하지 마세요.

## Git 훅

### 설치

`bd init`은 기본적으로 훅을 설치합니다(`bd init --skip-hooks`로 건너뜀). 수동으로
설치하거나 새로 고치려면 다음을 실행합니다.

```bash
bd hooks install
```

설치된 훅은 `bd hooks run <hook-name>`을 호출하는 얇은 shim이므로 `bd`를
업그레이드하면 훅 동작도 자동으로 업데이트됩니다.

| 훅 | 수행 작업 |
|------|--------------|
| `pre-commit` | 연결된 훅 실행. `export.auto`가 활성화되면 같은 커밋에 들어가도록 `.beads/issues.jsonl` 내보내기 |
| `post-merge` | 연결된 훅 실행. Dolt 원격이 없을 때만 레거시 대체 수단으로 JSONL 가져오기. `sync.remote`가 설정되면 `bd dolt pull`이 정식 동기화 |
| `pre-push` | 푸시 전에 연결된 훅 실행 |
| `post-checkout` | 브랜치 체크아웃 후 연결된 훅 실행 |
| `prepare-commit-msg` | 에이전트(`BD_ACTOR`)가 커밋할 때 `Executed-By:` 에이전트 ID 트레일러 추가 |

shim은 기존 훅과 공존하도록 섹션 마커를 사용합니다. 마커 밖의 내용은 설치와 업그레이드
간에 보존됩니다. 설치 변형은 다음과 같습니다.

```bash
bd hooks install --beads    # .beads/hooks/에 설치(Dolt 백엔드에 권장)
bd hooks install --shared   # .beads-hooks/에 설치(버전 관리, 팀과 공유 가능)
bd hooks install --chain    # bd 훅 전에 기존 훅 실행
```

훅 설치는 워크트리를 인식합니다. `bd`가 공유 git 디렉터리를 확인하므로 연결된
워크트리에서 설치해도 작동합니다.

### 상태

```bash
bd hooks list
```

### 제거

```bash
bd hooks uninstall
```

### 외부 훅 관리자

bd는 다음 외부 git 훅 관리자를 감지하고 해당 구성이 `bd hooks run`을 호출하는지 확인합니다.

- [lefthook](https://lefthook.dev/) — YAML/TOML/JSON 구성
- [husky](https://typicode.github.io/husky/) — `.husky/` 디렉터리 스크립트
- [pre-commit](https://pre-commit.com/) — `.pre-commit-config.yaml`
- [prek](https://prek.j178.dev/) — Rust 기반 pre-commit 대안(같은 구성)
- [hk](https://hk.jdx.dev/) — Pkl 구성을 사용하는 빠른 훅 관리자
- [overcommit](https://github.com/sds/overcommit) — Ruby 기반(감지만 지원)
- yorkie — 감지만 지원
- [simple-git-hooks](https://github.com/toplenboren/simple-git-hooks) — 경량 JS(감지만 지원)

`bd doctor`는 감지한 관리자가 bd와 통합되었는지 보고하며, `bd doctor --fix`는 관리자
기존 훅이 계속 실행되도록 `--chain`과 함께 훅을 다시 설치합니다.

구성 기반 관리자에는 bd 단계를 직접 추가합니다. `hk.pkl` 예시:

```pkl
hooks {
    ["pre-commit"] {
        steps {
            ["bd-pre-commit"] {
                check = "bd hooks run pre-commit"
            }
        }
    }
    ["post-merge"] {
        steps {
            ["bd-post-merge"] {
                check = "bd hooks run post-merge"
            }
        }
    }
    ["pre-push"] {
        steps {
            ["bd-pre-push"] {
                check = "bd hooks run pre-push \"$@\""
            }
        }
    }
}
```

### 훅 시간 제한

훅 shim은 훅이 git 작업을 무기한 멈추지 않도록 `bd hooks run`을 OS 수준 `timeout`으로
감쌉니다. 기본값은 **300초**(5분)이며 연결된 pre-commit 파이프라인(eslint, prettier,
TypeScript 컴파일)을 수용합니다. `BEADS_HOOK_TIMEOUT` 환경 변수로 재정의하세요.

```bash
# 더 긴 시간 제한 설정(초)
export BEADS_HOOK_TIMEOUT=600  # 10분

# 또는 호출마다 설정
BEADS_HOOK_TIMEOUT=600 git commit -m "..."
```

시간 제한에 도달하면 beads가 경고를 표시하고 git 작업을 계속 진행합니다. 커밋이나
푸시를 차단하지 않습니다.

## 충돌 해결

Dolt는 내장 병합 기능을 사용해 데이터베이스 수준에서 병합 충돌을 처리합니다. 동기화 중
충돌이 발생하면 Dolt가 충돌하는 행을 식별하고 SQL을 통해 해결할 수 있게 합니다.

```bash
# 충돌 확인 및 수정
bd doctor --fix
```

## 보호된 브랜치

Dolt는 Git 참조와 별도로 `refs/dolt/data` 아래에 데이터를 저장합니다. 따라서 beads
데이터가 보호된 Git 브랜치와 충돌하지 않으며 별도의 `beads-sync` 브랜치나 보호된 브랜치
예외가 필요 없습니다. Git `origin`이 있는 새 프로젝트에서 `bd init`은 해당 origin을
Dolt 원격으로 자동 구성합니다.

레거시 `beads-sync` 정리를 포함한 전체 워크플로는
[보호된 브랜치](/reference/protected-branches)를 참조하세요.

## Git 워크트리

Beads는 추가 설정 없이 Git 워크트리에서 작동합니다. 연결된 워크트리는 저장소의
`.beads` 워크스페이스를 찾고 Dolt를 통해 이슈 데이터를 동기화합니다.

```bash
# 연결된 워크트리에서
bd create "작업"
bd list
bd dolt pull
bd dolt push
```

모든 워크트리는 저장소의 `.beads` 워크스페이스를 공유합니다. 설정되어 있으면
`BEADS_DIR`, 그다음 주 저장소의 `.beads` 순으로 검색해 워크트리 간 데이터베이스 중복을
방지합니다. 활성 워크스페이스의 기준 확인에는 `bd where`를 사용하세요. 워크트리에 로컬
`./.beads`가 없어도 정상일 수 있습니다. 임베디드 모드(기본값)는 한 번에 한 기록자를
처리합니다. 워크트리 간 동시 기록자에는 서버 모드를 사용하세요. 전체 가이드는
[Git 워크트리](/reference/worktrees)를 참조하세요.

이전 beads 버전에는 숨겨진 Git 워크트리를 만드는 `sync.branch` 워크플로가
문서화되어 있었습니다. 이 워크플로는 제거되었으며 현재 동기화는 Dolt 원격을 사용합니다.

## 브랜치 워크플로

### 기능 브랜치

```bash
git checkout -b feature-x
bd create "기능 X" -t feature
# 작업...
bd dolt push
git push
```

### 포크 워크플로

```bash
# 포크에서
bd init --contributor   # 대화형 마법사
# 별도 계획 저장소에서 작업...
bd dolt push
```

기여자 마법사는 이슈 데이터를 별도 계획 저장소에 유지하므로 업스트림 저장소에는
`.beads/`가 남지 않습니다. 오픈 소스 기여자, 개인 개발자, 공개 저장소의 비공개 작업
추적에 가장 적합합니다.

`bd init`은 포크를 자동 감지하고 beads 파일이 로컬에 유지되도록 `.git/info/exclude`
구성(`--setup-exclude`)을 제안합니다. `--role contributor` 또는 `--role maintainer`
(비대화형 모드의 기본값)로 묻지 않고 역할을 설정할 수 있습니다.

### 팀 워크플로

```bash
bd init --team
# 모든 팀원이 Dolt 데이터베이스 공유
bd dolt pull   # Dolt 원격에서 최신 변경 사항 풀
bd dolt push   # Dolt 원격에 변경 사항 푸시
```

보호된 브랜치와 병합 전 검토 정책을 사용하는 팀에 가장 적합합니다. 다중 저장소 패턴은
[다중 저장소 마이그레이션](/multi-agent/multi-repo-migration)을 참조하세요.

### 중복 감지

브랜치를 병합한 뒤 다음을 실행합니다.

```bash
bd duplicates --auto-merge
```

## 브랜치리스 워크플로(Jujutsu / jj)

Beads는 [Jujutsu(jj)](https://martinvonz.github.io/jj/) 같은 브랜치리스 VCS 도구와
함께 작동합니다. beads 데이터는 git 브랜치가 아니라 Dolt에 저장되므로 "현재 브랜치"
개념에 의존하지 않습니다.

### 훅 없이 작동하는 기능

beads의 모든 핵심 기능은 git 훅 없이 작동합니다.

| 기능 | 훅 필요 여부 | 참고 |
|---------|----------------|-------|
| `bd create`, `bd update`, `bd close` | 아니요 | 핵심 CRUD가 Dolt를 직접 사용 |
| `bd ready`, `bd list`, `bd show` | 아니요 | 읽기 전용 쿼리 |
| `bd dolt push` / `bd dolt pull` | 아니요 | git과 독립적인 Dolt 네이티브 동기화 |
| `bd onboard`, `bd doctor` | 아니요 | 진단과 온보딩 |
| 에이전트 ID 트레일러 | 예 | `prepare-commit-msg` 훅이 커밋에 `Executed-By:` 추가 |
| 훅 연결 | 예 | 기존 pre-commit, post-merge 훅 보존 |

초기화 중 훅을 완전히 건너뛰려면 다음을 실행합니다.

```bash
bd init --skip-hooks
```

### AGENTS.md 없이 작동하는 기능

`bd init`이 생성하는 AGENTS.md 파일은 AI 에이전트 지침을 제공합니다. 자체 에이전트
지침을 관리하거나 beads가 추적되는 파일을 수정하지 않기를 원한다면 다음을 실행합니다.

```bash
bd init --skip-agents    # AGENTS.md와 Claude/Codex 설정 생성 건너뛰기
bd init --stealth        # 완전한 비가시 모드(훅 + 에이전트도 건너뜀)
```

### Jujutsu 설정

**같은 위치의 저장소**(`jj git init --colocate`): Git 훅이 정상적으로 작동합니다.
Beads는 단순화된 훅(`pre-commit`과 `post-merge`만, 스테이징 로직 없음)을 설치합니다.

**순수 jj 저장소**(git 없음): jj에는 아직 네이티브 훅이 없으므로 푸시 별칭을 설정합니다.

```toml
# ~/.config/jj/config.toml
[aliases]
push = ["util", "exec", "--", "sh", "-c", "bd dolt commit && bd dolt push && jj git push \"$@\"", ""]
```

그런 다음 `jj git push` 대신 `jj push`를 사용합니다.

## 모범 사례

1. **훅 설치** - `bd hooks install`
2. **정기적으로 푸시** - 세션 종료 시 `bd dolt push`
3. **작업 전 풀** - `bd dolt pull`로 최신 이슈 가져오기
4. **일반 Git 워크트리 사용** - 동기화 브랜치 불필요
