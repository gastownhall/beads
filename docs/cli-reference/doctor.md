---
title: "bd doctor"
description: "현재 디렉터리 또는 지정 경로의 beads 설치를 건전성 검사합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc doctor`에서 생성되었습니다.

현재 디렉터리 또는 지정 경로의 beads 설치를 건전성 검사합니다.

이 명령은 다음을 검사합니다:
  - .beads/ 디렉터리 존재 여부
  - 데이터베이스 버전과 마이그레이션 상태
  - 스키마 호환성(필요한 모든 테이블과 열 존재)
  - 해시 기반 ID와 순차 ID 중 어느 것을 사용하는지
  - CLI 버전이 최신인지(GitHub 릴리스 확인)
  - Claude 플러그인이 최신인지(Claude Code에서 실행 시)
  - 파일 권한
  - 순환 의존성
  - Git 훅(pre-commit, post-merge, pre-push)
  - .beads/.gitignore가 최신인지
  - Metadata.json 버전 추적(LastBdVersion 필드)

성능 모드(--perf):
  데이터베이스에서 성능 진단을 실행합니다:
  - 주요 작업의 소요 시간 측정(bd ready, bd list, bd show 등)
  - 시스템 정보 수집(OS, 아키텍처, SQLite 버전, 데이터베이스 통계)
  - 분석용 CPU 프로필 생성
  - 버그 보고서에 첨부할 수 있는 보고서 출력

내보내기 모드(--output):
  이력 분석 및 버그 보고를 위해 진단을 JSON 파일에 저장합니다.
  간헐적 문제 추적을 위한 타임스탬프와 플랫폼 정보를 포함합니다.

특정 검사 모드(--check):
  특정 검사를 상세히 실행합니다. 사용 가능한 검사:
  - artifacts: beads classic 아티팩트를 감지하고 선택적으로 정리
    (오래된 JSONL, SQLite 파일, 불필요한 .beads 디렉터리). --clean과 함께 사용.
  - conventions: 규칙 드리프트 검사(린트 경고, 오래된 이슈, 고립 이슈).
    권고 전용이며 경고만 하고 차단하지 않음.
  - pollution: 데이터베이스의 테스트 이슈를 감지하고 선택적으로 정리
  - validate: 집중 데이터 무결성 검사 실행(중복, 고립된 의존성, 테스트 오염,
    git 충돌). 자동 복구에는 --fix와 함께 사용.

심층 검증 모드(--deep):
  전체 그래프 무결성을 검증합니다. 대규모 데이터베이스에서는 느릴 수 있습니다.
  추가 검사:
  - 상위 항목 일관성: 모든 parent-child 의존성이 기존 이슈를 가리킴
  - 의존성 무결성: 모든 의존성이 유효한 이슈를 참조
  - epic 완전성: 닫을 준비가 된 epic 찾기(모든 하위 이슈가 닫힘)
  - agent bead 무결성: agent bead에 유효한 상태 값이 있음
  - 메일 스레드 무결성: 스레드 ID가 기존 이슈를 참조
  - molecule 무결성: molecule에 유효한 parent-child 구조가 있음

서버 모드(--server):
  Dolt 서버 모드 연결의 상태 검사를 실행합니다(bd-dolt.2.3):
  - 서버 접근 가능: 구성된 host:port에 연결할 수 있는가?
  - Dolt 버전: 일반 MySQL이 아닌 Dolt 서버인가?
  - 데이터베이스 존재: 'beads' 데이터베이스가 있는가?
  - 스키마 호환: beads 테이블을 쿼리할 수 있는가?
  - 연결 풀: 풀 상태 메트릭

마이그레이션 검증 모드(--migration):
  기계 판독 가능 출력으로 Dolt 마이그레이션 검증 검사를 실행합니다.
  마이그레이션 전 준비 상태를 확인하려면 --migration=pre를 사용하세요:
  - JSONL 파일이 존재하고 유효함(구문 분석 가능, 손상 없음)
  - 모든 JSONL 이슈가 SQLite에 있음(또는 불일치 설명)
  - 마이그레이션을 막는 차단 이슈가 없음
  마이그레이션 후 완료 상태를 확인하려면 --migration=post를 사용하세요:
  - Dolt 데이터베이스가 존재하고 정상임
  - JSONL의 모든 이슈가 Dolt에 있음
  - 마이그레이션 중 데이터 손실 없음
  - Dolt 데이터베이스에 잠금 또는 커밋되지 않은 변경 사항이 없음
  자동화용 기계 판독 가능 출력에는 --json과 함께 사용하세요.

에이전트 모드(--agent):
  AI 에이전트가 사용하도록 설계된 진단을 출력합니다. 간결한 통과/실패 메시지 대신
  각 이슈에 다음을 포함합니다:
  - 관찰된 상태: 시스템의 실제 모습
  - 예상 상태: 시스템이 보여야 하는 모습
  - 설명: 이슈와 중요성에 대한 전체 산문 컨텍스트
  - 명령: 실행할 정확한 복구 명령
  - 소스 파일: 코드베이스에서 추가 조사할 위치
  - 심각도: blocking(작업 방지), degraded(일부 기능), advisory(정보 전용)
  ZFC 준수: Go는 관찰하고 보고하며 에이전트가 결정하고 행동합니다.
  구조화된 에이전트용 출력에는 --json과 함께 사용하세요.

경고 숨기기:
  doctor.suppress.&lt;check-slug&gt; 구성을 설정해 특정 경고를 숨깁니다:
    bd config set doctor.suppress.pending-migrations true
    bd config set doctor.suppress.git-hooks true
  검사 이름은 slug로 변환됩니다: "Git Hooks" → "git-hooks".
  경고만 숨겨지며 오류와 통과한 검사는 항상 표시됩니다.
  숨기기 해제: bd config unset doctor.suppress.&lt;slug&gt;

예시:
  bd doctor              # 현재 디렉터리 검사
  bd doctor /path/to/repo # 특정 저장소 검사
  bd doctor --json       # 기계 판독 가능 출력
  bd doctor --agent      # 에이전트용 진단 출력
  bd doctor --agent --json  # 구조화된 에이전트 진단(JSON)
  bd doctor --fix        # 이슈 자동 수정(확인 포함)
  bd doctor --fix --yes  # 이슈 자동 수정(확인 없음)
  bd doctor --fix -i     # 각 수정 개별 확인
  bd doctor --fix --fix-child-parent  # child→parent 의존성도 수정(선택 적용)
  bd doctor --fix --force # 데이터베이스를 열 수 없어도 강제 복구
  bd doctor --fix --source=jsonl # JSONL 내보내기에서 데이터베이스 재구축
  bd doctor --dry-run    # 변경하지 않고 --fix 수행 내용 미리 보기
  bd doctor --perf       # 성능 진단
  bd doctor --output diagnostics.json  # 진단을 파일로 내보내기
  bd doctor --check=artifacts           # classic 아티팩트 표시(JSONL, SQLite, 불필요한 디렉터리)
  bd doctor --check=artifacts --clean  # 안전하게 삭제할 수 있는 아티팩트 삭제(확인 포함)
  bd doctor --check=conventions        # 규칙 드리프트 검사(린트, 오래됨, 고립 항목)
  bd doctor --check=pollution          # 잠재적 테스트 이슈 표시
  bd doctor --check=pollution --clean  # 테스트 이슈 삭제(확인 포함)
  bd doctor --check=validate         # 데이터 무결성 검사만 수행
  bd doctor --check=validate --fix   # 데이터 무결성 이슈 자동 수정
  bd doctor --deep             # 전체 그래프 무결성 검증
  bd doctor --server           # Dolt 서버 모드 상태 검사
  bd doctor --migration=pre    # Dolt 마이그레이션 준비 상태 검증
  bd doctor --migration=post   # Dolt 마이그레이션 완료 검증
  bd doctor --migration=pre --json  # 기계 판독 가능 마이그레이션 검증

```
bd doctor [path] [flags]
```

**플래그:**

```
      --agent                                   에이전트용 진단 모드: AI 에이전트를 위한 풍부한 컨텍스트(ZFC 준수)
      --check string                            특정 검사를 상세히 실행(예: 'pollution')
      --check-health                            Git 훅 빠른 상태 검사(성공 시 출력 없음)
      --clean                                   pollution 검사에서 감지된 테스트 이슈 삭제
      --deep                                    전체 그래프 무결성 검증
      --dry-run                                 변경하지 않고 수정 사항 미리 보기
      --fix                                     가능한 경우 이슈 자동 수정
      --fix-child-parent                        child→parent 의존성 제거(선택 적용)
  -i, --interactive                             각 수정 개별 확인
      --migration string                        Dolt 마이그레이션 검증 실행: 'pre'(마이그레이션 전) 또는 'post'(마이그레이션 후)
      --orchestrator                            오케스트레이터 다중 워크스페이스 모드에서 실행(routes.jsonl 예상, 더 높은 중복 허용치)
      --orchestrator-duplicates-threshold int   오케스트레이터 모드의 중복 허용 임곗값(wisp는 임시)(기본값 1000)
  -o, --output string                           진단을 JSON 파일로 내보내기
      --perf                                    성능 진단 실행 및 CPU 프로필 생성
      --server                                  Dolt 서버 모드 상태 검사 실행(연결, 버전, 스키마)
  -v, --verbose                                 모든 검사 표시(기본값은 경고/오류만 표시)
  -y, --yes                                     확인 프롬프트 건너뛰기(비대화형 사용)
```
