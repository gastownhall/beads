---
title: "bd admin"
description: "beads 데이터베이스 유지보수용 관리 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc admin`에서 생성되었습니다.

beads 데이터베이스 유지보수용 관리 명령입니다.

이 명령은 고급 사용자용이므로 주의해서 사용해야 합니다:
  cleanup   닫힌 이슈 삭제(이슈 수명 주기)
  compact   오래된 닫힌 이슈를 압축해 공간 절약(저장소 최적화)
  reset     모든 beads 데이터와 구성 제거(전체 초기화)

일상적인 유지보수에는 일반적인 복구를 자동으로 처리하는 'bd doctor --fix'를
선호하세요. 특정 데이터베이스 작업에는 이 admin 명령을 사용하세요.

```
bd admin [flags]
```

## bd admin cleanup

데이터베이스 크기를 줄이기 위해 닫힌 이슈를 삭제합니다.

이 명령은 데이터베이스에서 닫힌 이슈를 영구 제거합니다.

참고: 이 명령은 이슈 수명 주기(closed -&gt; deleted)만 관리합니다. 일반 상태 검사와
자동 복구에는 대신 'bd doctor --fix'를 사용하세요.

기본적으로 닫힌 이슈를 모두 삭제합니다. 특정 날짜 전에 닫힌 이슈만 삭제하려면
--older-than을 사용하세요.

예시:
  bd admin cleanup --force                          # 닫힌 이슈 모두 삭제
  bd admin cleanup --older-than 30 --force          # 닫힌 지 30일 이상 된 이슈만 삭제
  bd admin cleanup --ephemeral --force              # 닫힌 wisp(일시적 molecule)만 삭제
  bd admin cleanup --dry-run                        # 삭제될 항목 미리 보기

안전:
- 실제 삭제에는 --force 플래그 필요(--dry-run 제외)
- 종속 항목 삭제용 --cascade 지원
- 삭제될 항목의 미리 보기 표시
- 프로그래밍용 출력에는 --json 사용

함께 보기:
  bd doctor --fix    자동 상태 검사 및 복구(일상적인 유지보수에 권장)
  bd admin compact   오래된 닫힌 이슈를 압축해 공간 절약

```
bd admin cleanup [flags]
```

**플래그:**

```
      --cascade          모든 종속 이슈를 재귀적으로 삭제
      --dry-run          변경하지 않고 삭제될 항목 미리 보기
      --ephemeral        닫힌 wisp(일시적 molecule)만 삭제
  -f, --force            실제 삭제(이 플래그가 없으면 오류 표시)
      --older-than int   N일보다 오래전에 닫힌 이슈만 삭제(0 = 닫힌 이슈 모두)
```

## bd admin compact

의미 기반 요약을 사용해 오래된 닫힌 이슈를 압축합니다.

더 이상 활발히 참조되지 않는 닫힌 이슈를 요약하여 데이터베이스 크기를 줄입니다.
영구적인 점진적 소멸이며 원본 콘텐츠는 폐기됩니다.

모드:
  - Analyze: 에이전트 검토용 후보 내보내기(API 키 불필요)
  - Apply: 에이전트 제공 요약 수락(API 키 불필요)
  - Auto: AI 기반 압축(ANTHROPIC_API_KEY 또는 ai.api_key 필요, 레거시)
  - Dolt: Dolt 가비지 컬렉션 실행(Dolt 백엔드 저장소용)

계층:
  - Tier 1: 의미 압축(닫힌 지 30일, 70% 감소)
  - Tier 2: 초고압축(닫힌 지 90일) - 계획됨, 아직 구현되지 않음

Dolt 가비지 컬렉션:
  변경마다 자동 커밋하므로 Dolt 커밋 이력은 시간이 지날수록 증가합니다. --dolt로
  Dolt 가비지 컬렉션을 실행하고 디스크 공간을 회수하세요.

  --dolt: 디스크 공간을 확보하도록 .beads/dolt 디렉터리에 Dolt GC 실행.
          도달할 수 없는 커밋을 제거하고 저장소를 압축합니다.

예시:
  # Dolt 가비지 컬렉션
  bd compact --dolt                        # Dolt GC 실행
  bd compact --dolt --dry-run              # GC를 실행하지 않고 미리 보기

  # 에이전트 주도 워크플로(권장)
  bd compact --analyze --json              # 전체 콘텐츠가 포함된 후보 가져오기
  bd compact --apply --id bd-42 --summary summary.txt
  bd compact --apply --id bd-42 --summary - &lt; summary.txt

  # 레거시 AI 기반 워크플로
  bd compact --auto --dry-run              # 후보 미리 보기
  bd compact --auto --all                  # 대상 이슈 모두 압축
  bd compact --auto --id bd-42             # 특정 이슈 압축

  # 통계
  bd compact --stats                       # 통계 표시


```
bd admin compact [flags]
```

**플래그:**

```
      --actor string     감사 추적용 행위자 이름(기본값 "agent")
      --all              모든 후보 처리
      --analyze          Analyze 모드: 에이전트 검토용 후보 내보내기
      --apply            Apply 모드: 에이전트 제공 요약 수락
      --auto             Auto 모드: AI 기반 압축(레거시)
      --batch-size int   일괄 처리당 이슈 수(기본값 10)
      --dolt             Dolt 모드: .beads/dolt에 Dolt 가비지 컬렉션 실행
      --dry-run          압축하지 않고 미리 보기
      --force            강제 압축(검사 우회, --id 필요)
      --id string        특정 이슈 압축
      --json             JSON 형식 출력
      --limit int        후보 수 제한(0 = 제한 없음)
      --stats            압축 통계 표시
      --summary string   요약 파일 경로(stdin은 '-' 사용)
      --tier int         압축 계층(tier 1만 구현됨)(기본값 1)
      --workers int      병렬 워커(기본값 5)
```

## bd admin reset

모든 로컬 데이터를 제거하고 beads를 초기화되지 않은 상태로 되돌립니다.

이 명령은 다음을 제거합니다:
  - .beads 디렉터리(데이터베이스, JSONL, 구성)
  - bd가 설치한 Git 훅
  - 동기화 브랜치 워크트리

기본적으로 삭제될 항목을 표시합니다(dry-run 모드).
실제로 초기화하려면 --force를 사용하세요.

예시:
  bd reset              # 삭제될 항목 표시
  bd reset --force      # 실제로 모두 삭제

```
bd admin reset [flags]
```

**플래그:**

```
      --force   실제 초기화 수행(필수)
```
