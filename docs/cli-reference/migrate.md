---
title: "bd migrate"
description: "데이터베이스 마이그레이션 및 데이터 변환 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc migrate`에서 생성되었습니다.

데이터베이스 마이그레이션 및 데이터 변환 명령입니다.

하위 명령이 없으면 데이터베이스 메타데이터를 검사하고 현재 버전으로 업데이트합니다.

하위 명령:
  hooks       marker 관리 형식으로 git 훅 마이그레이션 계획
  issues      저장소 간 이슈 이동
  schema      대기 중인 스키마 마이그레이션 적용(멱등)
  sync        다중 클론 설정용 sync.branch 워크플로 설정


```
bd migrate [flags]
```

**플래그:**

```
      --dry-run          변경하지 않고 수행될 작업 표시
      --inspect          AI 에이전트 분석용 마이그레이션 계획과 데이터베이스 상태 표시
      --json             마이그레이션 통계를 JSON 형식으로 출력
      --update-repo-id   저장소 ID 업데이트(git 원격 변경 후 사용)
      --yes              확인 프롬프트 자동 승인
```

## bd migrate hooks

marker 관리 형식으로 마이그레이션하기 위해 git 훅 파일과 sidecar 아티팩트를 분석합니다.

모드:
  --dry-run  파일을 변경하지 않고 마이그레이션 작업 미리 보기
  --apply    마이그레이션 작업 적용

예시:
  bd migrate hooks --dry-run
  bd migrate hooks --apply
  bd migrate hooks --apply --yes
  bd migrate hooks --dry-run --json

```
bd migrate hooks [path] [flags]
```

**플래그:**

```
      --apply     계획된 훅 마이그레이션 변경 사항 적용
      --dry-run   변경하지 않고 수행될 작업 표시
      --json      JSON 형식으로 출력
      --yes       --apply의 확인 프롬프트 건너뛰기
```

## bd migrate issues

필터링하고 의존성을 보존하면서 한 소스 저장소에서 다른 저장소로 이슈를 이동합니다.

이 명령은 선택한 이슈의 source_repo 필드를 업데이트하여 다음을 수행할 수 있게 합니다:
- 기여자 계획 이슈를 업스트림 저장소로 이동
- 여러 단계의 저장소에 걸쳐 이슈 재구성
- 여러 저장소의 이슈 통합

예시:
  # 계획 저장소에서 현재 저장소로의 마이그레이션 미리 보기
  bd migrate-issues --from ~/.beads-planning --to . --dry-run

  # 열린 P1 버그 모두 이동
  bd migrate-issues --from ~/repo1 --to ~/repo2 --priority 1 --type bug --status open

  # 특정 이슈와 그 의존성 이동
  bd migrate-issues --from . --to ~/archive --id bd-abc --id bd-xyz --include closure

  # 레이블 필터로 이슈 이동
  bd migrate-issues --from . --to ~/feature-work --label frontend --label urgent

```
bd migrate issues [flags]
```

**플래그:**

```
      --dry-run            변경하지 않고 계획 표시
      --from string        소스 저장소(필수)
      --id strings         마이그레이션할 특정 이슈 ID(여러 개 지정 가능)
      --ids-file string    이슈 ID가 포함된 파일(한 줄에 하나)
      --include string     포함할 의존성: none/upstream/downstream/closure(기본값 "none")
      --label strings      레이블로 필터링(여러 개 지정 가능)
      --priority int       우선순위로 필터링(0-4)(기본값 -1)
      --status string      상태로 필터링(open/closed/all)
      --strict             고립 의존성이나 누락된 저장소가 있으면 실패
      --to string          대상 저장소(필수)
      --type string        이슈 유형으로 필터링(bug/feature/task/epic/chore/decision)
      --within-from-only   소스 저장소의 의존성만 포함(기본값 true)
      --yes                확인 프롬프트 건너뛰기
```

## bd migrate schema

대기 중인 스키마 마이그레이션을 멱등하게 적용합니다.

스키마 마이그레이션은 저장소를 열 때 자동으로도 실행되므로 이 하위 명령은 일반적으로
아무 작업도 하지 않습니다. CI, 릴리스 gate, 복구 시나리오에서 마이그레이션을
명시하고 관찰할 수 있도록 존재합니다.

예시:
  bd migrate schema
  bd migrate schema --json

```
bd migrate schema [flags]
```

**플래그:**

```
      --json   JSON 형식으로 출력
```

## bd migrate sync

다중 클론 설정을 위한 별도 브랜치 워크플로를 구성합니다.

이슈 데이터가 전용 브랜치에 커밋되어 main 브랜치를 깨끗하게 유지하도록
sync.branch 구성 값을 설정합니다.

예시:
  bd migrate sync beads-sync

```
bd migrate sync <branch> [flags]
```

**플래그:**

```
      --dry-run   변경하지 않고 수행될 작업 표시
      --json      JSON 형식으로 출력
```
