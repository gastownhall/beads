---
title: "bd metrics"
description: "익명 사용량 메트릭 활성화 여부와 전송되는 정확한 내용을 표시하고"
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc metrics`에서 생성되었습니다.

익명 사용량 메트릭 활성화 여부와 전송되는 정확한 내용을 표시하고
이를 켜거나 끕니다.

bd는 사람들이 실제로 어떻게 사용하는지 파악하기 위해 익명 사용량 메트릭을 공유합니다.
실행된 명령, bd 버전, OS 플랫폼만 포함합니다. 이를 바탕으로 다음 개선 대상을 정합니다.
이슈, 경로, 원격, 신원 또는 사용자가 제공한 텍스트는 절대 수집하지 않습니다.

  bd metrics            현재 상태와 수집 항목 표시
  bd metrics on         메트릭 켜기
  bd metrics off        메트릭 끄기
  bd metrics example    bd가 보내는 이벤트의 실제 예시 표시

```
bd metrics [flags]
```

## bd metrics example

bd가 보내는 익명 메트릭의 실제 예시를 표시합니다

```
bd metrics example [flags]
```

## bd metrics off

익명 사용량 메트릭을 끕니다

```
bd metrics off [flags]
```

## bd metrics on

익명 사용량 메트릭을 켭니다

```
bd metrics on [flags]
```
