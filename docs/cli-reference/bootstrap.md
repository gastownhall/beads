---
title: "bd bootstrap"
description: "Bootstrap은 기존 데이터를 파괴하지 않고 beads 데이터베이스를 설정합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc bootstrap`에서 생성되었습니다.

Bootstrap은 기존 데이터를 파괴하지 않고 beads 데이터베이스를 설정합니다.
'bd init --force'와 달리 bootstrap은 기존 이슈를 절대 삭제하지 않습니다.

Bootstrap은 올바른 작업을 자동 감지합니다:
  • sync.remote가 구성된 경우: 원격에서 클론
  • git origin에 Dolt 데이터(refs/dolt/data)가 있는 경우: git에서 클론하고 향후 push/pull용 origin 연결
  • .beads/backup/*.jsonl이 있는 경우: 백업에서 복원
  • .beads/issues.jsonl이 있는 경우: git 추적 JSONL에서 가져오기
  • 데이터베이스가 없는 경우: 새로 생성
  • 데이터베이스가 이미 있는 경우: 검증하고 상태 보고

다음 작업에 권장되는 명령입니다:
  • 새 클론에 beads 설정
  • 새 컴퓨터로 이동한 후 복구
  • 손상된 데이터베이스 구성 복구

비대화형 모드(--non-interactive, --yes/-y 또는 BD_NON_INTERACTIVE=1):
  bootstrap 계획을 실행하기 전 확인 프롬프트를 건너뜁니다.
  stdin이 터미널이 아니거나 CI=true가 설정된 경우에도 자동 감지됩니다.

예시:
  bd bootstrap              # 자동 감지 및 설정
  bd bootstrap --dry-run    # 수행될 작업 표시
  bd bootstrap --json       # 계획을 JSON으로 출력
  bd bootstrap --yes        # 확인 프롬프트 건너뛰기


```
bd bootstrap [flags]
```

**플래그:**

```
      --dry-run           실행하지 않고 수행될 작업 표시
      --non-interactive   --yes의 별칭
  -y, --yes               확인 프롬프트 건너뛰기(CI/자동화용)
```
