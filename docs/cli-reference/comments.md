---
title: "bd comments"
description: "이슈의 댓글을 보거나 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc comments`에서 생성되었습니다.

이슈의 댓글을 보거나 관리합니다.

예시:
  # 이슈의 모든 댓글 나열(이슈 ID 필수, "comments list"는 없음)
  bd comments bd-123

  # 댓글을 JSON 형식으로 나열
  bd comments bd-123 --json

  # 댓글 추가
  bd comments add bd-123 "댓글입니다"

  # 파일에서 댓글 추가
  bd comments add bd-123 -f notes.txt

```
bd comments [issue-id] [flags]
```

**플래그:**

```
      --local-time   UTC 대신 현지 시간으로 타임스탬프 표시
```

## bd comments add

이슈에 댓글을 추가합니다.

예시:
  # 댓글 추가
  bd comments add bd-123 "지금 작업 중"

  # 파일에서 댓글 추가
  bd comments add bd-123 -f notes.txt

```
bd comments add [issue-id] [text] [flags]
```

**플래그:**

```
  -a, --author string   댓글에 작성자 추가
  -f, --file string     파일에서 댓글 텍스트 읽기
```

## bd comments list

잘못된 명령입니다. 댓글을 나열하려면 bd comments &lt;issue-id&gt;를 사용하세요

```
bd comments list [flags]
```
