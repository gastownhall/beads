---
title: "bd ready"
description: "준비된 작업(활성 차단 요소가 없는 열린 이슈)을 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc ready`에서 생성되었습니다.

준비된 작업(활성 차단 요소가 없는 열린 이슈)을 표시합니다.

in_progress, blocked, deferred, hooked 이슈를 제외합니다. 실제로 맡을 수 있는 작업을
찾기 위해 차단 요소 인식 의미론을 적용하는 GetReadyWork API를 사용합니다.

참고: 'bd list --ready'는 동일한 차단 요소 인식 준비 작업 의미론을 사용합니다.

--mol을 사용해 특정 molecule의 단계로 필터링합니다:
  bd ready --mol bd-patrol   # molecule 내 준비된 단계 표시

--gated를 사용해 gate-resume 디스패치 준비가 된 molecule을 찾습니다:
  bd ready --gated           # gate가 닫힌 molecule 찾기

--claim을 사용해 필터와 일치하는 첫 준비 이슈를 원자적으로 맡습니다:
  bd ready --claim --json

molecule을 실행하는 에이전트가 다음에 실행할 수 있는 단계를 확인할 때 유용합니다.

```
bd ready [flags]
```

**플래그:**

```
  -a, --assignee string              담당자로 필터링
      --claim                        필터와 일치하는 첫 준비 이슈를 원자적으로 맡기
      --exclude-label strings        지정 레이블 중 하나라도 있는 이슈 제외
      --exclude-type strings         결과에서 이슈 유형 제외(쉼표로 구분하거나 반복 가능, 예: --exclude-type=convoy,epic)
      --explain                      이슈가 준비되거나 차단된 이유를 의존성 인식 방식으로 표시
      --gated                        gate-resume 디스패치 준비가 된 molecule 찾기
      --has-metadata-key string      이 메타데이터 키가 설정된 이슈 필터링
      --include-deferred             향후 defer_until 타임스탬프가 있는 이슈 포함
      --include-ephemeral            결과에 임시 이슈(wisp) 포함
  -l, --label strings                레이블로 필터링(AND: 모두 있어야 함). --label-any와 함께 사용 가능
      --label-any strings            레이블로 필터링(OR: 하나 이상 있어야 함). --label과 함께 사용 가능
  -n, --limit int                    표시할 최대 이슈 수(무제한은 0 사용)(기본값 100)
      --metadata-field stringArray   메타데이터 필드로 필터링(key=value, 반복 가능)
      --mol string                   특정 molecule 내 단계로 필터링
      --mol-type string              molecule 유형으로 필터링: swarm, patrol 또는 work
      --offset int                   처음 N개의 일치 결과 건너뛰기(0부터 시작). --proxied-server에서만 지원.
      --parent string                이 bead/epic의 하위 항목으로 필터링
      --plain                        이슈를 일반 번호 목록으로 표시
      --pretty                       상태/우선순위 기호가 있는 트리 형식으로 이슈 표시(기본값 true)
  -p, --priority int                 우선순위로 필터링
  -s, --sort string                  정렬 정책: priority(기본값), hybrid, oldest(기본값 "priority")
  -t, --type string                  이슈 유형으로 필터링(task, bug, feature, epic, decision, merge-request). 별칭: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision
  -u, --unassigned                   담당자 없는 이슈만 표시
```
