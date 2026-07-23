---
title: "bd update"
description: "하나 이상의 이슈를 업데이트합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc update`에서 생성되었습니다.

하나 이상의 이슈를 업데이트합니다.

이슈 ID를 지정하지 않으면 마지막으로 접근한 이슈(가장 최근 create, update, show
또는 close 작업)를 업데이트합니다.

```
bd update [id...] [flags]
```

**플래그:**

```
      --acceptance string            인수 기준
      --add-label strings            레이블 추가(반복 가능)
      --allow-empty-description      stdin 또는 파일에서 읽을 때 빈 설명으로 교체 허용
      --append-notes string          기존 메모에 추가(줄바꿈 구분자 포함)
  -a, --assignee string              담당자
      --await-id string              gate await_id 설정(예: gh:run gate의 GitHub 실행 ID)
      --body-file string             파일에서 설명 읽기(stdin은 - 사용)
      --claim                        이슈를 원자적으로 맡기(담당자를 자신으로, 상태를 in_progress로 설정, 이미 자신이 맡았다면 멱등)
      --defer string                 지정 날짜까지 연기(지우려면 빈 값). 그때까지 이슈가 bd ready에서 숨겨짐
  -d, --description string           이슈 설명
      --design string                설계 메모
      --design-file string           파일에서 설계 읽기(stdin은 - 사용)
      --due string                   마감 날짜/시간(지우려면 빈 값). 형식: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15
      --ephemeral                    이슈를 임시(wisp)로 표시 - JSONL로 내보내지 않음
  -e, --estimate int                 예상 시간(분)(예: 1시간은 60)
      --external-ref string          외부 참조(예: 'gh-9', 'jira-ABC', Linear URL)
      --history                      no-history 플래그 지우기(Dolt 커밋 이력 다시 활성화)
      --metadata string              사용자 정의 메타데이터 설정(JSON 문자열 또는 읽을 @file.json)
      --no-history                   이슈를 no-history로 표시(Dolt 커밋 건너뛰기, GC 대상 아님)
      --notes string                 추가 메모
      --parent string                새 상위 이슈 ID(이슈 상위 항목 변경, 제거하려면 빈 문자열 사용)
      --persistent                   이슈를 영구로 표시(wisp를 일반 이슈로 승격)
  -p, --priority string              우선순위(0-4 또는 P0-P4, 0이 가장 높음)
      --remove-label strings         레이블 제거(반복 가능)
      --session string               status=closed용 Claude Code 세션 ID(또는 CLAUDE_SESSION_ID 환경 변수 설정)
      --set-labels strings           모든 기존 레이블을 교체하여 설정(반복 가능)
      --set-metadata stringArray     메타데이터 key=value 설정(반복 가능, 예: --set-metadata team=platform)
      --spec-id string               사양 문서에 연결
  -s, --status string                새 상태
      --stdin                        stdin에서 설명 읽기(--body-file -의 별칭)
      --title string                 새 제목
  -t, --type string                  새 유형(bug|feature|task|epic|chore|decision), 사용자 정의 유형에는 types.custom 구성 필요
      --unset-metadata stringArray   메타데이터 키 제거(반복 가능, 예: --unset-metadata team)
```
