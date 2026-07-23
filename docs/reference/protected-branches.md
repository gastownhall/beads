---
title: 보호된 브랜치
description: Dolt가 Git 참조 외부에 이슈 데이터를 저장해 beads에 보호된 브랜치 우회가 필요 없는 이유와 팀 워크플로 및 레거시 동기화 브랜치 정리
---

현재 릴리스의 Beads에는 보호된 브랜치 우회 방법이 필요 없습니다.

이슈 데이터는 `main` 같은 일반 Git 브랜치와 별도로 Dolt의 `refs/dolt/data` 아래에
저장됩니다. Beads 명령은 이슈 업데이트를 현재 코드 브랜치에 커밋하지 않으므로 GitHub,
GitLab, Bitbucket의 브랜치 보호 규칙은 계속 코드 기록에만 적용됩니다.

## 현재 워크플로

프로젝트에서 beads를 초기화합니다.

```bash
bd init
```

프로젝트 정책에서 요구한다면 추적되는 작은 구성 파일을 커밋합니다.

```bash
git add .beads/.gitignore .beads/metadata.json .beads/config.yaml .gitignore
git commit -m "beads 이슈 트래커 초기화"
```

로컬 Dolt 데이터베이스 디렉터리는 계속 git에서 무시됩니다. Dolt 원격을 통해 이슈
데이터를 동기화합니다.

```bash
bd dolt pull
bd dolt push
```

`beads-sync` Git 브랜치, 보호된 브랜치 예외, beads 관리 Git 워크트리가 필요 없습니다.

## 보호된 브랜치가 안전한 이유

보호된 브랜치는 `refs/heads/main` 같은 Git 참조를 보호합니다. Dolt는 자체 참조
네임스페이스에 beads 데이터를 저장합니다. 따라서 다음과 같이 동작합니다.

- `bd create`, `bd update`, `bd close`는 `main`에 커밋을 생성하지 않습니다.
- `bd dolt push`는 코드 브랜치가 아니라 Dolt 데이터를 푸시합니다.
- 일반 코드 변경은 계속 기존 풀 리퀘스트 워크플로를 거칩니다.

## 팀 사용법

공유 트래커에서는 다음을 실행합니다.

```bash
bd init --team
bd dolt pull
bd ready
bd update <id> --claim
bd dolt push
```

다른 클론이 최신 이슈 상태를 볼 수 있도록 작업 시작 전에 풀하고 인계 전에 푸시하세요.

## 레거시 동기화 브랜치 정리

이전 beads 버전에는 `.beads` 변경 사항을 `beads-sync` 같은 브랜치에 커밋하고
`.git/beads-worktrees/` 아래의 숨겨진 Git 워크트리를 사용하는 실험적 `sync.branch`
워크플로가 문서화되어 있었습니다. 이 워크플로는 제거되었습니다.

이전 체크아웃에 동기화 브랜치 구성이 남아 있다면 지웁니다.

```bash
bd config set sync.branch ""
```

오래된 숨겨진 워크트리 때문에 브랜치를 체크아웃할 수 없다면 이를 제거하고 Git 워크트리
레지스트리를 정리합니다.

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

원격 `beads-sync` 브랜치가 제거된 워크플로에만 사용된다면 현재 이슈 데이터가 모두
Dolt를 통해 동기화되었는지 확인한 뒤 저장소 정책에 따라 보관하거나 삭제하세요.

## 문제 해결

### `bd dolt push`에 원격이 없음

Dolt 원격을 추가하거나 검사합니다.

```bash
bd dolt remote list
bd dolt remote add origin <remote-url>
bd dolt push
```

### `bd dolt pull` 중 충돌

Dolt는 데이터베이스 수준 충돌을 Git 브랜치 충돌과 별도로 보고합니다. 실패한 명령이
표시하는 병합 전략 또는 doctor 안내를 사용하세요.

```bash
bd vc merge <branch> --strategy [ours|theirs]
bd doctor --fix
```

### 오래된 훅에서 레거시 동기화 명령을 언급함

생성된 훅을 새로 고칩니다.

```bash
bd hooks install
```

## 함께 보기

- [Git 워크트리 가이드](/reference/worktrees) - Git 워크트리 동작
- [Git 통합](/reference/git-integration) - 일반 Git 통합 가이드
- [복구 플레이북](/recovery/init-safety) - 복구 플레이북
