---
title: 병합 충돌
description: Dolt 병합 충돌 해결하기
---

이 런북은 Dolt 동기화 작업 중 발생하는 병합 충돌을 해결하는 방법을 안내합니다.

## 증상

- `bd dolt pull`이 충돌 오류와 함께 실패합니다.
- 클론마다 이슈 상태가 다릅니다.

## 진단

```bash
# 데이터베이스 상태 확인
bd doctor

# 적용할 수정 사항 미리 보기
bd doctor --dry-run
```

## 해결 방법

**1단계:** 현재 상태를 백업합니다.
```bash
cp -r .beads .beads.backup
```

**2단계:** 충돌을 확인합니다.
```bash
bd doctor
```

**3단계:** 일치하도록 수정합니다.
```bash
bd doctor --fix
```

**4단계:** 상태를 확인합니다.
```bash
bd list
bd stats
```

**5단계:** 해결된 상태를 푸시합니다.
```bash
bd dolt push
```

## 예방

- 작업 세션 전후에 `bd dolt pull` / `bd dolt push`로 동기화합니다.
- Dolt 서버가 실행 중이지 않을 때 여러 클론에서 동시에 수정하지 않습니다.
