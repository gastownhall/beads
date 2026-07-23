---
title: "bd todo"
description: "TODO 항목을 경량 task 이슈로 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc todo`에서 생성되었습니다.

TODO 항목을 경량 task 이슈로 관리합니다.

TODO는 편리한 단축 명령을 제공하는 일반 task 유형 이슈입니다:
  bd todo add "제목"     -&gt; bd create "제목" -t task -p 2
  bd todo                -&gt; bd list --type task --status open
  bd todo done &lt;id&gt;      -&gt; bd close &lt;id&gt;

유형이나 우선순위를 변경하여 TODO를 전체 이슈로 승격할 수 있습니다:
  bd update todo-123 --type bug --priority 0

```
bd todo [flags]
```

## bd todo add

새 TODO 항목을 추가합니다

```
bd todo add <title> [flags]
```

**플래그:**

```
  -d, --description string   설명
  -p, --priority int         우선순위(0-4, 기본값 2)(기본값 2)
```

## bd todo done

TODO를 완료로 표시합니다

```
bd todo done <id> [<id>...] [flags]
```

**플래그:**

```
      --reason string   닫는 사유(기본값: Completed)
```

## bd todo list

TODO 항목을 나열합니다

```
bd todo list [flags]
```

**플래그:**

```
      --all   완료된 항목을 포함해 모든 TODO 표시
```
