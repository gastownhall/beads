---
title: "bd human"
description: "가장 일반적인 명령만 보여 주는 집중 도움말 메뉴를 표시합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc human`에서 생성되었습니다.

가장 일반적인 명령만 보여 주는 집중 도움말 메뉴를 표시합니다.

bd에는 70개가 넘는 명령이 있으며 다수는 AI 에이전트, 통합, 고급 워크플로용입니다.
이 명령은 사람이 가장 자주 필요로 하는 약 15개의 필수 명령을 표시합니다.

전체 명령 목록을 보려면 다음을 실행하세요: bd --help

하위 명령:
  human list              사람이 필요한 모든 bead 나열('human' 레이블 이슈)
  human respond &lt;id&gt;      사람이 필요한 bead에 응답(댓글 추가 후 닫기)
  human dismiss &lt;id&gt;      사람이 필요한 bead를 영구 기각
  human stats             사람이 필요한 bead의 요약 통계 표시

```
bd human [flags]
```

## bd human dismiss

응답하지 않고 사람이 필요한 bead를 영구 기각합니다.

이슈는 "Dismissed" 사유와 선택적 메모로 닫힙니다.

예시:
  bd human dismiss bd-123
  bd human dismiss bd-123 --reason "더 이상 해당하지 않음"

```
bd human dismiss <issue-id> [flags]
```

**플래그:**

```
      --reason string   기각 사유(선택 사항)
```

## bd human list

'human' 태그가 지정된 모든 이슈를 나열합니다.

사람의 개입이나 입력이 필요한 이슈입니다.

예시:
  bd human list
  bd human list --status=open
  bd human list --json

```
bd human list [flags]
```

**플래그:**

```
  -s, --status string   상태로 필터링(open, closed 등)
```

## bd human respond

사람이 필요한 bead에 댓글을 추가하고 닫아 응답합니다.

응답은 댓글로 추가되고 이슈는 "Responded" 사유로 닫힙니다.

예시:
  bd human respond bd-123 --response "인증에 OAuth2 사용"
  bd human respond bd-123 -r "승인됨, 구현 진행"

```
bd human respond <issue-id> [flags]
```

**플래그:**

```
  -r, --response string   응답 텍스트(필수)
```

## bd human stats

사람이 필요한 bead의 요약 통계를 표시합니다.

전체, 대기(open), 응답됨(기각 없이 닫힘), 기각된 bead 수를 표시합니다.

예시:
  bd human stats

```
bd human stats [flags]
```
