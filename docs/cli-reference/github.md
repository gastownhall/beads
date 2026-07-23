---
title: "bd github"
description: "beads와 GitHub 간 이슈 동기화 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc github`에서 생성되었습니다.

beads와 GitHub 간 이슈 동기화 명령입니다.

구성은 'bd config' 또는 환경 변수로 설정할 수 있습니다:
  github.token / GITHUB_TOKEN           - 개인용 액세스 토큰
  github.owner / GITHUB_OWNER           - 저장소 소유자
  github.repo / GITHUB_REPO             - 저장소 이름
  github.repository / GITHUB_REPOSITORY - 결합된 "owner/repo" 형식
  github.url / GITHUB_API_URL           - 사용자 정의 API URL(GitHub Enterprise)

```
bd github [flags]
```

## bd github pull

GitHub에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd github sync --pull-only --issues &lt;refs&gt;

```
bd github pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

## bd github push

하나 이상의 beads 이슈를 GitHub로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd github sync --push-only --issues &lt;ids&gt;

```
bd github push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd github repos

구성된 토큰으로 접근 가능한 GitHub 저장소를 나열합니다.

```
bd github repos [flags]
```

## bd github status

현재 GitHub 구성과 동기화 상태를 표시합니다.

```
bd github status [flags]
```

## bd github sync

beads와 GitHub 간에 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다:
- GitHub의 새 이슈 또는 업데이트된 이슈를 beads로 pull
- 로컬 beads 이슈를 GitHub로 push

방향을 제한하려면 --pull-only 또는 --push-only를 사용하세요.

```
bd github sync [flags]
```

**플래그:**

```
      --dry-run         변경하지 않고 동기화될 항목 표시
      --issues string   선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string   이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-github   충돌 시 GitHub 버전 사용
      --prefer-local    충돌 시 로컬 beads 버전 유지
      --prefer-newer    충돌 시 최신 버전 사용(기본값)
      --pull-only       GitHub에서 이슈만 pull
      --push-only       GitHub로 이슈만 push
```
