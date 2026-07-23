---
title: "bd audit"
description: "감사 로그 항목은 .beads/interactions.jsonl에 추가됩니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc audit`에서 생성되었습니다.

감사 로그 항목은 .beads/interactions.jsonl에 추가됩니다.

각 줄은 이벤트 하나입니다. 이 파일은 git으로 버전 관리하며 다음 용도로 사용합니다:
- 감사("에이전트가 왜 그렇게 했는가?")
- 데이터세트 생성(SFT/RL 미세 조정)

항목은 추가 전용입니다. 레이블 지정은 상위 항목을 참조하는 새 "label" 항목을 생성합니다.

```
bd audit [flags]
```

## bd audit label

기존 상호작용을 참조하는 레이블 항목을 추가합니다

```
bd audit label <entry-id> [flags]
```

**플래그:**

```
      --label string    레이블 값(예: "good" 또는 "bad")
      --reason string   레이블 지정 사유
```

## bd audit record

감사 상호작용 항목을 추가합니다

```
bd audit record [flags]
```

**플래그:**

```
      --error string       오류 문자열(llm_call/tool_call)
      --exit-code int      종료 코드(tool_call)(기본값 -1)
      --issue-id string    관련 이슈 ID(bd-...)
      --kind string        항목 종류(예: llm_call, tool_call, label)
      --model string       모델 이름(llm_call)
      --prompt string      프롬프트 텍스트(llm_call)
      --response string    응답 텍스트(llm_call)
      --stdin              stdin에서 JSON 객체 읽기(audit.Entry 스키마와 일치해야 함)
      --tool-name string   도구 이름(tool_call)
```
