---
title: "bd notion"
description: "beads와 Notion 간 이슈 동기화 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc notion`에서 생성되었습니다.

beads와 Notion 간 이슈 동기화 명령입니다.

```
bd notion [flags]
```

## bd notion connect

bd를 기존 Notion 데이터베이스 또는 데이터 소스에 연결합니다

```
bd notion connect [flags]
```

**플래그:**

```
      --url string   기존 Notion 데이터베이스 또는 데이터 소스 URL
```

## bd notion init

Notion에 전용 Beads 데이터베이스를 생성합니다

```
bd notion init [flags]
```

**플래그:**

```
      --parent string   상위 페이지 ID
      --title string    데이터베이스 제목(기본값 "Beads Issues")
```

## bd notion pull

Notion에서 하나 이상의 항목을 가져옵니다.

bead ID 또는 외부 참조를 위치 인수로 받습니다.
다음과 같습니다: bd notion sync --pull --issues &lt;refs&gt;

```
bd notion pull [refs...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 pull 미리 보기
```

## bd notion push

하나 이상의 beads 이슈를 Notion으로 푸시합니다.

bead ID를 위치 인수로 받습니다.
다음과 같습니다: bd notion sync --push --issues &lt;ids&gt;

```
bd notion push [bead-ids...] [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 push 미리 보기
```

## bd notion status

Notion 동기화 상태를 표시합니다

```
bd notion status [flags]
```

## bd notion sync

beads와 Notion 간에 이슈를 동기화합니다.

기본적으로 양방향 동기화를 수행합니다. 방향을 제한하려면 --pull 또는 --push를 사용하세요.

```
bd notion sync [flags]
```

**플래그:**

```
      --create-only     누락된 원격 페이지만 생성하고 기존 페이지는 업데이트하지 않음
      --dry-run         변경을 적용하지 않고 미리 보기
      --issues string   선택적으로 동기화할 쉼표 구분 bead ID(예: bd-abc,bd-def). --parent와 함께 사용할 수 없음.
      --parent string   이 bead와 하위 항목으로 push 제한(push 전용). --issues와 함께 사용할 수 없음.
      --prefer-local    충돌 시 로컬 beads 버전 유지
      --prefer-notion   충돌 시 Notion 버전 사용
      --pull            Notion에서 이슈만 pull
      --push            Notion으로 이슈만 push
      --state string    동기화할 이슈 상태: open, closed 또는 all(기본값 "all")
```
