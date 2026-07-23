---
title: 데이터베이스 손상
description: Dolt 데이터베이스 손상에서 복구하기
---

이 런북은 Beads의 데이터베이스 손상에서 복구하는 방법을 안내합니다.

## 증상

- `bd` 명령 실행 중 오류 메시지가 나타납니다.
- "database is locked" 오류가 계속 발생합니다.
- 존재해야 할 이슈가 누락됩니다.
- 데이터베이스 상태가 일관되지 않습니다.

## 진단

```bash
# 데이터베이스 무결성 확인
bd doctor

# Dolt 서버 상태 확인
bd dolt show
```

## 해결 방법

**1단계:** Dolt 서버를 중지합니다.
```bash
bd dolt stop
```

**2단계:** 현재 상태를 백업합니다.
```bash
cp -r .beads .beads.backup
```

**3단계:** doctor가 수정할 내용을 미리 봅니다.
```bash
bd doctor --dry-run
```

**4단계:** 데이터베이스를 다시 빌드합니다.
```bash
bd doctor --fix
```

**5단계:** 복구를 확인합니다.
```bash
bd doctor
bd list
```

**6단계:** Dolt 서버를 다시 시작합니다.
```bash
dolt sql-server
```

## 예방

- 동기화는 Dolt 서버가 처리하도록 합니다.
- 시스템을 종료하기 전에 `bd dolt stop`을 사용합니다.
- 문제를 조기에 발견하도록 `bd doctor`를 주기적으로 실행합니다.
