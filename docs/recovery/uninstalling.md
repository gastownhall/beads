---
title: 제거
description: 이슈 데이터를 백업한 뒤 bd admin reset으로 저장소에서 beads를 제거하고 git 훅을 제거한 다음 bd 바이너리 삭제하기
---

이 가이드에서는 저장소에서 beads를 제거하거나 머신에서 `bd` 바이너리를
제거하는 방법을 설명합니다.

## 데이터를 제거하기 전에

`.beads/`를 제거하면 로컬 Dolt 데이터베이스가 영구적으로 삭제됩니다. 이슈
기록이 중요하다면 먼저 Dolt 네이티브 백업을 만드세요.

```bash
bd backup init /path/to/beads-backup
bd backup sync
```

검토, 마이그레이션 또는 상호 운용을 위해 이슈 테이블 내보내기 파일을
작성할 수도 있습니다.

```bash
bd export -o ~/beads-issues-$(date +%Y%m%d).jsonl
```

`bd export`는 완전히 복원할 수 있는 데이터베이스 백업이 아닙니다. Dolt 브랜치,
커밋 기록, 작업 세트 상태 또는 이슈가 아닌 테이블을 보존하지 않습니다.

## 저장소 초기화

저장소 루트에서 `bd admin reset`을 사용하세요. 기본적으로 제거될 항목을
미리 보여 줍니다.

```bash
bd admin reset
```

미리 보기 내용이 맞으면 다음을 실행합니다.

```bash
bd admin reset --force
```

다음과 같은 beads 관리 저장소 데이터가 제거됩니다.

- `.beads/` 디렉터리
- beads가 관리하는 git 훅 섹션
- `.git/beads-worktrees/` 아래의 레거시 beads 동기화 워크트리

## 훅만 제거하기

이슈 데이터는 유지하고 git 훅만 제거하려면 다음을 실행합니다.

```bash
bd hooks uninstall
```

beads는 관리하는 훅 마커 밖의 관련 없는 사용자 훅 내용을 보존하므로, 훅 파일을
수동으로 삭제하는 것보다 이 방법이 좋습니다.

## 수동 정리

`bd admin reset`을 사용할 수 없거나 저장소에서 실행할 수 없을 때만 수동 정리를
사용하세요.

```bash
# 로컬 Dolt 서버가 실행 중이면 중지합니다.
bd dolt stop 2>/dev/null || true

# bd hooks uninstall을 사용할 수 없으면 beads 관리 훅을 제거합니다.
rm -f .git/hooks/pre-commit
rm -f .git/hooks/prepare-commit-msg
rm -f .git/hooks/post-merge
rm -f .git/hooks/pre-push
rm -f .git/hooks/post-checkout

# 로컬 beads 데이터베이스와 구성을 제거합니다.
rm -rf .beads

# 이전 beads 버전의 레거시 동기화 브랜치 워크트리를 제거합니다.
rm -rf .git/beads-worktrees
git worktree prune
```

`.gitattributes`에 beads 병합 드라이버 구성만 있으면 파일을 제거합니다.
다른 프로젝트 항목도 있으면 beads 줄만 삭제합니다.

beads 전용 git 구성이 남아 있으면 제거합니다.

```bash
git config --unset beads.role 2>/dev/null || true
git config --unset merge.beads.driver 2>/dev/null || true
git config --unset merge.beads.name 2>/dev/null || true
```

## `bd` 바이너리 제거하기

CLI는 독립 실행형 바이너리입니다. 설치 방법에 따라 제거하세요.

```bash
# Homebrew
brew uninstall beads

# Go 설치
rm -f "$(which bd)"

# 수동 설치 위치
rm -f /usr/local/bin/bd
```

MCP 패키지를 별도로 설치했다면 설치에 사용한 도구로 해당 패키지를 제거하세요.

## 제거 확인하기

```bash
which bd
test ! -e .beads
bd hooks list 2>/dev/null || true
git config --get merge.beads.driver
```

## 나중에 다시 설치하기

beads를 다시 초기화하려면 다음을 실행합니다.

```bash
bd init
```
