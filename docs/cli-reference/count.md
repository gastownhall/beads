---
title: "bd count"
description: "지정한 필터와 일치하는 이슈 수를 계산합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc count`에서 생성되었습니다.

지정한 필터와 일치하는 이슈 수를 계산합니다.

기본적으로 필터와 일치하는 총 이슈 수를 반환합니다.
다른 속성별로 수를 그룹화하려면 --by-* 플래그를 사용하세요.

예시:
  bd count                          # 모든 이슈 수 계산
  bd count --status open            # 열린 이슈 수 계산
  bd count --by-status              # 상태별 수 그룹화
  bd count --by-priority            # 우선순위별 수 그룹화
  bd count --by-type                # 이슈 유형별 수 그룹화
  bd count --by-assignee            # 담당자별 수 그룹화
  bd count --by-label               # 레이블별 수 그룹화
  bd count --assignee alice --by-status  # alice의 이슈 수를 상태별로 계산
  bd count --include-infra          # 이슈 + wisp 계층 수 계산('bd list --include-infra --all'의 카디널리티와 일치)


```
bd count [flags]
```

**플래그:**

```
  -a, --assignee string         담당자로 필터링
      --by-assignee             담당자별 수 그룹화
      --by-label                레이블별 수 그룹화
      --by-priority             우선순위별 수 그룹화
      --by-status               상태별 수 그룹화
      --by-type                 이슈 유형별 수 그룹화
      --closed-after string     지정 날짜 이후 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --closed-before string    지정 날짜 이전 닫힌 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-after string    지정 날짜 이후 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --created-before string   지정 날짜 이전 생성된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --desc-contains string    설명 부분 문자열로 필터링
      --empty-description       설명이 비어 있는 이슈 필터링
      --id string               특정 이슈 ID로 필터링(쉼표로 구분)
      --include-infra           인프라 bead와 wisp 계층 포함('bd list --include-infra --all'의 카디널리티와 일치)
  -l, --label strings           레이블로 필터링(AND: 모두 있어야 함)
      --label-any strings       레이블로 필터링(OR: 하나 이상 있어야 함)
      --no-assignee             담당자가 없는 이슈 필터링
      --no-labels               레이블이 없는 이슈 필터링
      --notes-contains string   메모 부분 문자열로 필터링
  -p, --priority int            우선순위로 필터링(0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)
      --priority-max int        최대 우선순위로 필터링(경계값 포함)
      --priority-min int        최소 우선순위로 필터링(경계값 포함)
  -s, --status string           저장된 상태로 필터링(open, in_progress, blocked, deferred, closed). 참고: 의존성으로 차단된 이슈에는 'bd blocked' 사용
      --title string            제목 텍스트로 필터링(대소문자 구분 없는 부분 문자열 일치)
      --title-contains string   제목 부분 문자열로 필터링
  -t, --type string             유형으로 필터링(bug, feature, task, epic, chore, decision, merge-request, molecule, gate)
      --updated-after string    지정 날짜 이후 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
      --updated-before string   지정 날짜 이전 업데이트된 이슈 필터링(YYYY-MM-DD 또는 RFC3339)
```
