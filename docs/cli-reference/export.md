---
title: "bd export"
description: "모든 이슈를 JSONL(줄바꿈으로 구분한 JSON) 형식으로 내보냅니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc export`에서 생성되었습니다.

모든 이슈를 JSONL(줄바꿈으로 구분한 JSON) 형식으로 내보냅니다.

각 줄은 레이블, 의존성, 댓글을 포함한 이슈 하나를 나타내는 완전한 JSON 객체입니다.

이 명령은 이슈 내보내기, 마이그레이션, 상호 운용성을 위한 것입니다. issues 테이블의
레코드를 내보냅니다. 전체 데이터베이스 백업이 아니며 Dolt 브랜치, 커밋 이력,
working-set 상태 또는 비이슈 테이블을 캡처하지 않습니다. 지원되는 전체 백업/복원
흐름에는 'bd backup init', 'bd backup sync', 'bd backup restore'를 사용하세요.

기본적으로 일반 이슈만 내보냅니다(agent, role, message 같은 인프라 bead 제외).
모든 항목을 포함하려면 --all을 사용하세요.

메모리('bd remember'에서 생성)는 민감한 에이전트 컨텍스트를 포함할 수 있어
기본적으로 제외됩니다. 포함하려면 --include-memories 또는 --all을 사용하세요.

예시:
  bd export                              # 이슈를 stdout으로 내보내기
  bd export -o issues.jsonl              # 이슈를 파일로 내보내기
  bd export --include-memories           # 이슈 + 메모리 내보내기
  bd export --all -o full.jsonl          # 인프라 + 템플릿 + gate + 메모리 포함
  bd export --scrub -o clean.jsonl       # 테스트/오염 레코드 제외

```
bd export [flags]
```

**플래그:**

```
      --all                모든 레코드 포함(인프라, 템플릿, gate, 메모리)
      --include-infra      인프라 bead 포함(agent, role, message)
      --include-memories   내보내기에 영구 메모리('bd remember'에서 생성) 포함
  -o, --output string      출력 파일 경로(기본값: stdout)
      --scrub              테스트/오염 레코드 제외
```
