---
title: 순환 의존성
description: 의존성 순환을 감지하고 해소하기
---

이 런북은 이슈의 순환 의존성을 감지하고 해소하는 방법을 안내합니다.

## 증상

- "circular dependency detected" 오류가 발생합니다.
- `bd blocked`가 예상치 못한 결과를 표시합니다.
- 준비 상태여야 할 이슈가 차단된 것으로 나타납니다.

## 진단

```bash
# 차단된 이슈 확인
bd blocked

# 특정 이슈의 의존성 보기
bd show <issue-id>

# 모든 의존성 나열
bd dep tree
```

## 해결 방법

**1단계:** 순환을 식별합니다.
```bash
bd blocked --verbose
```

**2단계:** 의존성 체인을 파악합니다.
```bash
bd show <issue-a>
bd show <issue-b>
# <issue-a>로 돌아올 때까지 체인을 따라갑니다.
```

**3단계:** 제거할 의존성을 결정합니다.
워크플로에서 가장 덜 중요한 의존성이 무엇인지 고려합니다.

**4단계:** 문제가 되는 의존성을 제거합니다.
```bash
bd dep remove <dependent-issue> <blocking-issue>
```

**5단계:** 순환이 해소되었는지 확인합니다.
```bash
bd blocked
bd ready
```

## 예방

- 의존성을 추가할 때 "Y보다 먼저 X"가 아니라 "X에는 Y가 필요하다"고 생각하세요.
- 의존성을 추가한 뒤 `bd blocked`를 사용해 순환이 있는지 확인하세요.
- 가능하면 의존성 체인을 얕게 유지하세요.
