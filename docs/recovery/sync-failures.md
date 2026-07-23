---
title: 동기화 실패
description: Dolt 동기화 실패에서 복구하기
---

이 런북은 Dolt 동기화 실패에서 복구하는 방법을 안내합니다.

## 증상

- `bd dolt push` 또는 `bd dolt pull`이 멈추거나 시간 초과됩니다.
- 네트워크 관련 오류 메시지가 나타납니다.
- "failed to push" 또는 "failed to pull" 오류가 발생합니다.
- Dolt 서버가 응답하지 않습니다.

## 진단

```bash
# Dolt 서버 상태 확인
bd doctor
bd dolt show

# Dolt 서버 로그 보기
tail -50 .beads/dolt-server.log   # 서버 모드
```

## 해결 방법

**1단계:** Dolt 서버를 중지합니다.
```bash
bd dolt stop
```

**2단계:** 잠금 파일을 확인합니다.
```bash
ls -la .beads/*.lock
# Dolt 서버가 확실히 중지되었다면 오래된 잠금을 제거합니다.
rm -f .beads/*.lock
```

**3단계:** 백업하고 수정 사항을 미리 봅니다.
```bash
cp -r .beads .beads.backup
bd doctor --dry-run
```

**4단계:** 필요하면 수정 사항을 적용합니다.
```bash
bd doctor --fix
```

**5단계:** Dolt 서버를 다시 시작합니다.
```bash
dolt sql-server
```

**6단계:** 동기화가 작동하는지 확인합니다.
```bash
bd dolt push
bd doctor
```

## 일반적인 원인

| 원인 | 해결 방법 |
|-------|----------|
| 네트워크 시간 초과 | 더 나은 연결에서 다시 시도합니다. |
| 오래된 잠금 파일 | Dolt 서버를 중지한 뒤 잠금을 제거합니다. |
| 손상된 상태 | 백업한 뒤 `bd doctor --fix`를 실행합니다. |
| 병합 충돌 | [병합 충돌](/recovery/merge-conflicts)을 참조하세요. |

## 예방

- 동기화 전에 네트워크가 안정적인지 확인합니다.
- 터미널을 닫기 전에 동기화가 완료되도록 합니다.
- 시스템을 종료하기 전에 `bd dolt stop`을 사용합니다.
