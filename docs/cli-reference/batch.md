---
title: "bd batch"
description: "여러 쓰기 작업을 단일 데이터베이스 트랜잭션에서 실행합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc batch`에서 생성되었습니다.

여러 쓰기 작업을 단일 데이터베이스 트랜잭션에서 실행합니다.

명령은 stdin(한 줄에 하나) 또는 -f/--file을 통해 파일에서 읽습니다. 모든 작업은
단일 dolt 트랜잭션에서 실행됩니다. 오류가 있으면 전체 일괄 작업을 롤백하고,
그렇지 않으면 DOLT_COMMIT 하나로 커밋합니다.

루프에서 현재 'bd'를 여러 번 호출해 btrfs+compression 기반 dolt sql-server에
심각한 쓰기 증폭을 유발하는 셸 스크립트용입니다. 일괄 처리는 N번의 호출을
트랜잭션 하나와 dolt 커밋 하나로 줄입니다.

문법(한 줄에 명령 하나):
  close &lt;id&gt; [reason...]
  update &lt;id&gt; &lt;key&gt;=&lt;value&gt; [&lt;key&gt;=&lt;value&gt; ...]
  create &lt;type&gt; &lt;priority&gt; &lt;title...&gt;
  dep add &lt;from-id&gt; &lt;to-id&gt; [type]
  dep remove &lt;from-id&gt; &lt;to-id&gt;
  #comment  (빈 줄과 '# ...' 댓글은 무시)

지원되는 'update' 키: status, priority, title, assignee
지원되는 의존성 유형: 'bd dep add --help' 참조(기본값: blocks)

토큰은 공백으로 구분됩니다. 큰따옴표 문자열("like this")에는 공백을 포함할 수 있습니다.
큰따옴표를 포함하려면 \"를, 백슬래시에는 \\를 사용하세요.

예시:
  # 파이프에서
  bd list --status stale -q | awk '&#123;print "close",$1," stale"&#125;' | bd batch

  # 파일에서
  bd batch -f operations.txt

  # 인라인
  printf 'close bd-1 done\nupdate bd-2 status=in_progress\n' | bd batch

성공 시 0으로 종료하고 요약(--json 사용 시 JSON)을 출력합니다. 오류가 있으면
전체 트랜잭션을 롤백하고 실패한 줄과 함께 0이 아닌 값으로 종료합니다.

참고: 좁은 하위 집합입니다. 'show', 'list', 'ready', 'sync' 같은 명령, 복잡한 create
흐름 또는 위에 나열되지 않은 플래그는 허용되지 않습니다. 대화형/읽기 작업에는
일반 'bd' 하위 명령을 사용하세요.

```
bd batch [flags]
```

**플래그:**

```
      --dry-run          입력을 구문 분석하고 실행하지 않은 채 명령 출력
  -f, --file string      stdin 대신 파일에서 명령 읽기
  -m, --message string   DOLT_COMMIT 메시지(기본값: 'bd: batch N ops by <actor>')
```
