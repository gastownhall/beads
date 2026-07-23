---
title: "bd rules"
description: "Claude 규칙을 감사하고 압축합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc rules`에서 생성되었습니다.

Claude 규칙을 감사하고 압축합니다

```
bd rules [flags]
```

## bd rules audit

규칙에서 모순과 병합 기회를 검색합니다

```
bd rules audit [flags]
```

**플래그:**

```
      --path string       규칙 디렉터리 경로(기본값 ".claude/rules/")
      --threshold float   Jaccard 유사도 임곗값(기본값 0.6)
```

## bd rules compact

관련 규칙을 복합 규칙으로 병합합니다

```
bd rules compact [flags]
```

**플래그:**

```
      --auto            감사 제안 적용
      --dry-run         적용하지 않고 미리 보기
      --group strings   병합할 규칙 이름
      --path string     규칙 디렉터리 경로(기본값 ".claude/rules/")
```
