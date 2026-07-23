---
title: "bd sql"
description: "기반 데이터베이스(SQLite 또는 Dolt)에 원시 SQL 쿼리를 실행합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc sql`에서 생성되었습니다.

기반 데이터베이스(SQLite 또는 Dolt)에 원시 SQL 쿼리를 실행합니다.

디버깅, 유지보수, 상위 수준 명령의 버그 우회에 유용합니다.

예시:
  bd sql 'SELECT COUNT(*) FROM issues'
  bd sql 'SELECT id, title FROM issues WHERE status = "open" LIMIT 5'
  bd sql 'DELETE FROM dirty_issues WHERE issue_id = "bd-abc123"'
  bd sql --csv 'SELECT id, title, status FROM issues'

쿼리는 데이터베이스에 직접 전달됩니다. SELECT 쿼리는 결과를 표로 반환합니다
(--json/--csv 사용 시 JSON/CSV). SELECT가 아닌 쿼리(INSERT, UPDATE, DELETE)는
영향을 받은 행 수를 보고합니다.

경고: 직접 데이터베이스 접근은 저장소 계층을 우회합니다. 주의해서 사용하세요.

```
bd sql <query> [flags]
```

**플래그:**

```
      --csv   결과를 CSV 형식으로 출력
```
