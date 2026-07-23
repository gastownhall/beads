---
title: "bd worktree"
description: "올바른 beads 구성으로 git 워크트리를 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc worktree`에서 생성되었습니다.

올바른 beads 구성으로 git 워크트리를 관리합니다.

워크트리를 사용하면 같은 git 저장소를 공유하는 여러 작업 디렉터리에서
병렬 개발(예: 여러 에이전트 또는 기능)이 가능합니다.

워크트리는 git 공통 디렉터리 검색을 통해 기본 저장소와 같은 beads 데이터베이스를
자동 공유하므로 수동 리디렉션 구성이 필요하지 않습니다.

예시:
  bd worktree create feature-auth           # 워크트리 생성
  bd worktree create bugfix --branch fix-1  # 특정 브랜치 이름으로 생성
  bd worktree list                          # 모든 워크트리 나열
  bd worktree remove feature-auth           # 워크트리 제거(안전 검사 포함)
  bd worktree info                          # 현재 워크트리 정보 표시

```
bd worktree [flags]
```

## bd worktree create

병렬 개발용 git 워크트리를 생성합니다.

이 명령은 다음을 수행합니다:
1. ./&lt;name&gt;(또는 지정 경로)에 git 워크트리 생성
2. .gitignore에 워크트리 경로 추가(저장소 루트 내부인 경우)

워크트리는 git 공통 디렉터리 검색을 통해 기본 저장소와 같은 beads 데이터베이스를
자동 공유하므로 리디렉션 파일이 필요하지 않습니다.

예시:
  bd worktree create feature-auth           # ./feature-auth에 생성
  bd worktree create bugfix --branch fix-1  # 브랜치 이름으로 생성
  bd worktree create ../agents/worker-1     # 상대 경로에 생성

```
bd worktree create <name> [--branch=<branch>] [flags]
```

**플래그:**

```
      --branch string   워크트리의 브랜치 이름(기본값: 이름과 동일)
```

## bd worktree info

현재 워크트리 정보를 표시합니다.

현재 디렉터리가 git 워크트리에 있으면 다음을 표시합니다:
- 워크트리 경로와 이름
- 브랜치
- Beads 구성(redirect 또는 main)
- 기본 저장소 위치

예시:
  bd worktree info          # 현재 워크트리 정보 표시
  bd worktree info --json   # JSON 출력

```
bd worktree info [flags]
```

## bd worktree list

모든 git 워크트리와 beads 구성 상태를 나열합니다.

각 워크트리에 다음을 표시합니다:
- 이름(디렉터리 이름)
- 경로(전체 경로)
- 브랜치
- Beads 상태: "redirect"(공유 DB 사용), "shared"(기본), "none"(beads 없음)

예시:
  bd worktree list          # 모든 워크트리 나열
  bd worktree list --json   # JSON 출력

```
bd worktree list [flags]
```

## bd worktree remove

안전 검사를 거쳐 git 워크트리를 제거합니다.

제거하기 전에 다음을 검사합니다:
- 커밋되지 않은 변경 사항
- push되지 않은 커밋
- stash 항목

안전 검사를 건너뛰려면 --force를 사용하세요(권장하지 않음).

예시:
  bd worktree remove feature-auth         # 안전 검사 후 제거
  bd worktree remove feature-auth --force # 안전 검사 건너뛰기

```
bd worktree remove <name> [flags]
```

**플래그:**

```
      --force   안전 검사 건너뛰기
```
