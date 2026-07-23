---
title: "bd upgrade"
description: "bd 버전 업그레이드를 확인하고 변경 사항을 검토하는 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc upgrade`에서 생성되었습니다.

bd 버전 업그레이드를 확인하고 변경 사항을 검토하는 명령입니다.

upgrade 명령은 bd 버전 변경 사항을 파악하는 데 도움이 됩니다:
  - bd upgrade status: 마지막 사용 이후 bd 버전 변경 여부 확인
  - bd upgrade review: 마지막 버전 이후 새 변경 사항 표시
  - bd upgrade ack: 현재 버전 확인 처리

버전 추적은 자동이며 bd가 실행될 때마다 metadata.json을 업데이트합니다.

```
bd upgrade [flags]
```

## bd upgrade ack

현재 bd 버전을 확인한 것으로 표시합니다.

현재 버전을 확인했다는 기록을 metadata.json에 업데이트합니다. 업그레이드 변경 사항을
검토한 후 향후 업그레이드 알림을 숨기는 데 주로 유용합니다.

참고: 버전 추적은 자동으로 수행되므로 명시적으로 확인 표시를 하려는 경우가 아니면
이 명령을 실행할 필요가 없습니다.

예시:
  bd upgrade ack
  bd upgrade ack --json

```
bd upgrade ack [flags]
```

## bd upgrade review

마지막으로 사용한 버전 이후 bd의 새 변경 사항을 표시합니다.

최근 3개 버전을 표시하는 'bd info --whats-new'와 달리 이 명령은 사용한 마지막
특정 버전 이후의 모든 변경 사항을 표시합니다.

구버전에서 업그레이드하는 경우 그 이후 변경된 모든 항목의 전체 변경 로그가 표시됩니다.

예시:
  bd upgrade review
  bd upgrade review --json

```
bd upgrade review [flags]
```

## bd upgrade status

마지막 사용 이후 bd가 업그레이드되었는지 확인합니다.

이 명령은 시작 시 자동으로 수행되는 버전 추적을 사용해 bd 업그레이드 여부를 감지합니다.

예시:
  bd upgrade status
  bd upgrade status --json

```
bd upgrade status [flags]
```
