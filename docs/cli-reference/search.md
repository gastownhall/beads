---
title: "bd search"
description: "제목과 ID에서 이슈를 검색합니다(기본적으로 닫힌 이슈 제외)."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc search`에서 생성되었습니다.

제목과 ID에서 이슈를 검색합니다(기본적으로 닫힌 이슈 제외).

ID 형태의 쿼리(예: "bd-123", "hq-319")는 빠른 정확/접두사 일치를 사용합니다.
텍스트 쿼리는 제목을 검색합니다. 설명 검색에는 --desc-contains를 사용하세요.
닫힌 이슈를 포함하려면 --status all을 사용하세요.

예시:
  bd search "인증 버그"
  bd search "로그인" --status open
  bd search "데이터베이스" --label backend --limit 10
  bd search --query "성능" --assignee alice
  bd search "bd-5q" # 일부 ID로 검색(빠른 접두사 일치)
  bd search "보안" --priority-min 0 --priority-max 2
  bd search "버그" --created-after 2025-01-01
  bd search "리팩터링" --status all  # 닫힌 이슈 포함
  bd search "버그" --sort priority
  bd search "작업" --sort created --reverse
  bd search "api" --desc-contains "엔드포인트"
  bd search "정리" --no-assignee --no-labels

```
bd search [query] [flags]
```

**플래그:**

```
  -a, --assignee string              담당자로 필터링
      --closed-after string          지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string         지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string         지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string        지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --desc-contains string         설명 부분 문자열로 필터링(대소문자 구분 안 함)
      --empty-description            설명이 비어 있거나 없는 이슈 필터링
      --external-contains string     외부 참조 부분 문자열로 필터링(대소문자 구분 안 함)
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함)
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함)
  -n, --limit int                    결과 수 제한(기본값: 50)(기본값 50)
      --long                         각 이슈의 상세한 여러 줄 출력 표시
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --no-assignee                  담당자가 없는 이슈 필터링
      --no-labels                    레이블이 없는 이슈 필터링
      --notes-contains string        메모 부분 문자열로 필터링(대소문자 구분 안 함)
      --priority-max string          최대 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --priority-min string          최소 우선순위로 필터링(경계값 포함, 0-4 또는 P0-P4)
      --query string                 검색 쿼리(위치 인수 대신 사용)
  -r, --reverse                      정렬 순서 반전
      --sort string                  필드로 정렬: priority, created, updated, closed, status, id, title, type, assignee
  -s, --status string                저장된 상태로 필터링(open, in_progress, blocked, deferred, closed, all). 기본값은 closed 제외, 포함하려면 'all' 사용. 참고: 의존성으로 차단된 이슈에는 'bd blocked' 사용
  -t, --type string                  유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate)
      --updated-after string         지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string        지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
```
