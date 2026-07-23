---
title: 복구 개요
description: 일반적인 Beads 문제 진단 및 해결
---

이 섹션에서는 일반적인 Beads 문제의 단계별 복구 절차를 제공합니다. 각 런북은 증상, 진단, 해결 방법(최대 5단계), 예방이라는 일관된 형식을 따릅니다.

## 일반적인 문제

| 이슈 | 증상 | 런북 |
|-------|----------|---------|
| 초기화 안전 거부 | `bd init` 또는 `bd dolt`가 `pk-fork-refused` 같은 패턴 코드와 함께 거부됨 | [복구 플레이북](/recovery/init-safety) |
| 데이터베이스 손상 | 데이터베이스 오류, 데이터 누락 | [데이터베이스 손상](/recovery/database-corruption) |
| 병합 충돌 | 동기화 중 Dolt 충돌 | [병합 충돌](/recovery/merge-conflicts) |
| 순환 의존성 | 순환 감지 오류 | [순환 의존성](/recovery/circular-dependencies) |
| 동기화 실패 | `bd dolt push`/`bd dolt pull` 오류 | [동기화 실패](/recovery/sync-failures) |
| 기록 비대화 | 저장소가 무한히 커지고 `dolt gc`가 아무것도 회수하지 못함 | [기록 비대화](/recovery/history-squash) |
| beads 제거 | bd를 제거하거나 저장소에서 beads를 삭제 | [제거](/recovery/uninstalling) |

## 빠른 진단

특정 런북을 살펴보기 전에 다음 항목을 빠르게 확인하세요.

```bash
# Beads 상태 확인
bd status

# Dolt 서버가 실행 중인지 확인
bd doctor

# 차단된 이슈 확인
bd blocked
```

<Tip>
대부분의 문제는 `bd status`로 진단할 수 있습니다. 특정 런북을 따르기 전에 여기서 시작하세요.
</Tip>

## 도움받기

이 런북으로 문제가 해결되지 않으면 다음을 수행하세요.

1. [FAQ](/reference/faq)를 확인합니다.
2. [기존 이슈](https://github.com/gastownhall/beads/issues)를 검색합니다.
3. 진단 출력을 첨부해 새 이슈를 엽니다.
