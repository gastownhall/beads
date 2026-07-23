---
title: "bd diff"
description: "두 커밋 또는 브랜치 간 이슈 차이를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc diff`에서 생성되었습니다.

두 커밋 또는 브랜치 간 이슈 차이를 표시합니다.

ref로 다음을 사용할 수 있습니다:
- 커밋 해시(예: abc123def)
- 브랜치 이름(예: main, feature-branch)
- HEAD, HEAD~1 같은 특수 ref

예시:
  bd diff main feature-branch   # main과 feature 브랜치 비교
  bd diff HEAD~5 HEAD           # 최근 커밋 5개의 변경 사항 표시
  bd diff abc123 def456         # 두 특정 커밋 비교

```
bd diff <from-ref> <to-ref> [flags]
```
