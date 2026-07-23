---
title: Git 워크트리 가이드
description: 하나의 .beads 워크스페이스를 공유하는 Git 워크트리에서 beads 사용하기, 외부 BEADS_DIR 설정 및 레거시 동기화 브랜치 정리
---

Beads는 별도의 동기화 브랜치 없이 일반 Git 워크트리에서 작동합니다. 현재 beads는
`refs/dolt/data` 아래의 Dolt에 이슈 데이터를 저장하므로 이슈 동기화가 Git 브랜치
커밋과 분리됩니다.

## 현재 모델

`BEADS_DIR`로 검색을 재정의하지 않는 한 같은 저장소의 모든 워크트리는 동일한 beads
워크스페이스를 사용합니다.

```
project/
├── .git/                 # 공유 Git 디렉터리
├── .beads/               # 공유 beads 구성과 로컬 Dolt 데이터
├── main-worktree/
└── feature-worktree/
```

핵심 사항:

- `bd`는 연결된 워크트리에서 저장소의 `.beads` 디렉터리를 찾습니다.
- 이슈 변경 사항은 현재 Git 브랜치에 커밋되지 않고 Dolt에 저장됩니다.
- 클론 간 동기화에는 `bd dolt pull`과 `bd dolt push`를 사용합니다.
- `sync.branch` 또는 beads 관리 Git 워크트리가 필요 없습니다.

## 기본 사용법

저장소에서 beads를 한 번 초기화합니다.

```bash
cd project
bd init
```

평소처럼 연결된 워크트리를 생성합니다.

```bash
git worktree add ../project-feature feature-branch
cd ../project-feature
bd ready
bd create "기능 X 구현" -t feature -p 1
```

구성된 Dolt 원격을 통해 이슈 데이터를 동기화합니다.

```bash
bd dolt pull
bd dolt push
```

## 외부 Beads 워크스페이스

여러 코드 워크트리가 공유하는 별도의 이슈 트래커 저장소를 사용하려면 `BEADS_DIR`이
해당 워크스페이스를 가리키도록 합니다.

```bash
export BEADS_DIR=~/project-beads/.beads

cd ~/project/main       && bd list
cd ~/project/feature-1  && bd list
cd ~/project/feature-2  && bd list
```

외부 `BEADS_DIR`을 사용하면 `bd dolt push`와 `bd dolt pull`이 코드 저장소가 아닌
외부 beads 워크스페이스를 대상으로 합니다.

## 훅

beads가 설치한 Git 훅은 워크트리를 인식합니다. 훅이 오래되었거나 제거된 레거시 동기화
명령을 언급하면 새로 고칩니다.

```bash
bd hooks install
```

<a id="legacy-cleanup"></a>

## 레거시 정리

이전 beads 버전에는 `.git/beads-worktrees/<branch>/` 같은 숨겨진 워크트리를 만드는
실험적 `sync.branch` 워크플로가 있었습니다. 이 워크플로는 제거되었습니다.

beads가 만든 워크트리가 아직 브랜치를 점유해 레거시 체크아웃에서 브랜치를 전환할 수
없다면 오래된 워크트리 레코드를 제거합니다.

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

이전 구성에 동기화 브랜치가 남아 있다면 지웁니다.

```bash
bd config set sync.branch ""
```

## 문제 해결

### 워크트리에서 데이터베이스를 찾을 수 없음

주 저장소에 `.beads` 디렉터리가 있고 워크트리가 해당 저장소에 속하는지 확인합니다.

```bash
git worktree list
cd /path/to/main/repo
ls -la .beads
```

저장소에 아직 beads 워크스페이스가 없다면 주 저장소에서 `bd init`을 실행합니다.

### 여러 `.beads` 디렉터리

워크트리에 실수로 자체 `.beads` 디렉터리가 생겼다면 고유한 이슈 데이터가 없는지
확인한 뒤 추가 사본을 제거하거나 보관합니다. 기본적으로 워크트리는 저장소 워크스페이스를
공유해야 합니다.

### 동시 기록자

일반적인 단일 사용자 워크트리에서는 명령을 직접 실행합니다. 여러 머신 또는 에이전트가
실제로 동시에 기록하는 워크플로에서는 `bd dolt pull`과 `bd dolt push`로 자주
동기화하고 같은 이슈를 동시에 작업하지 않도록 트래커를 통해 조율하세요.

## 함께 보기

- [보호된 브랜치](/reference/protected-branches) - 보호된 브랜치 동작
- [Git 통합](/reference/git-integration) - 일반 Git 통합 가이드
- [다중 저장소 마이그레이션 가이드](/multi-agent/multi-repo-migration) - 다중 워크스페이스 패턴
