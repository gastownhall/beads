---
title: "bd label"
description: "이슈 레이블을 관리합니다"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc label`에서 생성되었습니다.

이슈 레이블을 관리합니다

```
bd label [flags]
```

## bd label add

하나 이상의 이슈에 레이블을 추가합니다

```
bd label add [issue-id...] [label] [flags]
```

## bd label list

이슈의 레이블을 나열합니다

```
bd label list [issue-id] [flags]
```

## bd label list-all

데이터베이스의 고유 레이블을 모두 나열합니다

```
bd label list-all [flags]
```

## bd label propagate

상위 항목의 레이블을 아직 갖지 않은 모든 직접 하위 항목에 전파합니다. epic의 하위 작업 전체에 branch: 레이블을 적용할 때 유용합니다.

```
bd label propagate [parent-id] [label] [flags]
```

## bd label remove

하나 이상의 이슈에서 레이블을 제거합니다

```
bd label remove [issue-id...] [label] [flags]
```
