---
title: "bd note"
description: "이슈의 notes 필드에 메모를 추가합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc note`에서 생성되었습니다.

이슈의 notes 필드에 메모를 추가합니다.

'bd update &lt;id&gt; --append-notes "text"'의 단축 명령입니다.

예시:
  bd note gt-abc "불안정한 테스트 수정"
  bd note gt-abc 불안정한 테스트 수정
  echo "파이프에서 온 메모" | bd note gt-abc --stdin
  bd note gt-abc --file notes.txt

```
bd note <id> [text...] [flags]
```

**플래그:**

```
      --file string   파일에서 메모 텍스트 읽기
      --stdin         stdin에서 메모 텍스트 읽기
```
