---
title: "bd vc"
description: "beads 데이터베이스의 버전 관리 작업입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc vc`에서 생성되었습니다.

beads 데이터베이스의 버전 관리 작업입니다.

이 명령은 브랜치 생성, 병합, 이력 보기를 포함해 이슈 데이터에 git과 유사한 버전 관리를 제공합니다.

참고: 빠른 접근에는 'bd history', 'bd diff', 'bd branch'도 사용할 수 있습니다.
이 하위 명령은 merge, commit 같은 추가 작업을 제공합니다.

```
bd vc [flags]
```

## bd vc commit

현재 변경 사항을 모두 포함한 새 Dolt 커밋을 생성합니다.

예시:
  bd vc commit -m "새 기능 이슈 추가"
  bd vc commit --message "여러 이슈의 우선순위 수정"
  echo "여러 줄 메시지" | bd vc commit --stdin

```
bd vc commit [flags]
```

**플래그:**

```
  -m, --message string   커밋 메시지
      --stdin            stdin에서 커밋 메시지 읽기
```

## bd vc merge

지정한 브랜치를 현재 브랜치에 병합합니다.

병합 충돌이 있으면 보고됩니다. --strategy로 충돌을 해결할 수 있습니다.

예시:
  bd vc merge feature-xyz                    # feature-xyz를 현재 브랜치에 병합
  bd vc merge feature-xyz --strategy ours    # 충돌 시 우리 변경 사항을 우선하여 병합
  bd vc merge feature-xyz --strategy theirs  # 충돌 시 상대 변경 사항을 우선하여 병합

```
bd vc merge <branch> [flags]
```

**플래그:**

```
      --strategy string   충돌 해결 전략: 'ours' 또는 'theirs'
```

## bd vc status

현재 브랜치, 커밋 해시, 커밋되지 않은 변경 사항을 표시합니다.

예시:
  bd vc status

```
bd vc status [flags]
```
