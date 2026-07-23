---
title: "bd hooks"
description: "beads 통합용 git 훅을 설치, 제거 또는 나열합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc hooks`에서 생성되었습니다.

beads 통합용 git 훅을 설치, 제거 또는 나열합니다.

훅은 다음을 제공합니다:
- pre-commit: 커밋 전 연결된 훅 실행
- post-merge: pull/merge 후 연결된 훅 실행
- pre-push: push 전 연결된 훅 실행
- post-checkout: 브랜치 checkout 후 연결된 훅 실행
- prepare-commit-msg: 포렌식용 에이전트 신원 trailer 추가

```
bd hooks [flags]
```

## bd hooks install

beads 통합용 git 훅을 설치합니다.

기본적으로 훅은 현재 저장소의 .git/hooks/에 설치됩니다. .beads/hooks/에 설치하려면
--beads를 사용하세요(Dolt 백엔드에 권장). git에 커밋하고 팀원과 공유할 수 있는
버전 관리 디렉터리(.beads-hooks/)에 설치하려면 --shared를 사용하세요.

훅은 기존 훅과 공존하도록 섹션 marker를 사용합니다. marker 밖의 사용자 콘텐츠는
설치와 업그레이드 전반에 걸쳐 보존됩니다.

설치된 훅:
  - pre-commit: 커밋 전 연결된 훅 실행
  - post-merge: pull/merge 후 연결된 훅 실행
  - pre-push: push 전 연결된 훅 실행
  - post-checkout: 브랜치 checkout 후 연결된 훅 실행
  - prepare-commit-msg: 에이전트 신원 trailer 추가(오케스트레이터 에이전트용)

```
bd hooks install [flags]
```

**플래그:**

```
      --beads    .beads/hooks/에 훅 설치(Dolt 백엔드에 권장)
      --chain    기존 훅과 연결(bd 훅 전에 실행)
      --force    백업 없이 기존 훅 덮어쓰기
      --shared   .git/hooks/ 대신 .beads-hooks/(버전 관리)에 훅 설치
```

## bd hooks list

bd git 훅의 상태를 표시합니다(설치됨, 오래됨, 누락됨).

```
bd hooks list [flags]
```

## bd hooks run

git 훅의 로직을 실행합니다. 일반적으로 .git/hooks/에 설치된 얇은 shim 스크립트가
이 명령을 호출합니다.

지원되는 훅:
  - pre-commit: 커밋 전 연결된 훅 실행
  - post-merge: pull/merge 후 연결된 훅 실행
  - pre-push: push 전 연결된 훅 실행
  - post-checkout: 브랜치 checkout 후 연결된 훅 실행
  - prepare-commit-msg: 포렌식용 에이전트 신원 trailer 추가

얇은 shim 패턴은 훅 로직이 설치된 bd 버전과 항상 동기화되도록 합니다.
bd를 업그레이드하면 훅 동작이 자동으로 업데이트됩니다.

```
bd hooks run <hook-name> [args...] [flags]
```

## bd hooks uninstall

.git/hooks/ 디렉터리에서 bd git 훅을 제거합니다.

```
bd hooks uninstall [flags]
```
