---
title: "bd comment"
description: "이슈에 댓글을 추가합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc comment`에서 생성되었습니다.

이슈에 댓글을 추가합니다.

'bd comments add &lt;id&gt; "text"'의 단축 명령입니다.

예시:
  bd comment bd-123 "지금 작업 중"
  bd comment bd-123 지금 작업 중
  echo "파이프에서 온 댓글" | bd comment bd-123 --stdin
  bd comment bd-123 --file notes.txt

```
bd comment <id> [text...] [flags]
```

**플래그:**

```
      --file string   파일에서 댓글 텍스트 읽기
      --stdin         stdin에서 댓글 텍스트 읽기
```
