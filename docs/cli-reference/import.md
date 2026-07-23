---
title: "bd import"
description: "JSONL 파일(줄바꿈으로 구분한 JSON)의 이슈를 데이터베이스로 가져옵니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc import`에서 생성되었습니다.

JSONL 파일(줄바꿈으로 구분한 JSON)의 이슈를 데이터베이스로 가져옵니다.

파일을 지정하지 않으면 .beads/ 아래 구성된 import.path(기본값: issues.jsonl)에서
가져옵니다. stdin에서 읽으려면 "-"를 사용하세요. 'bd export'의 증분 대응 명령으로,
새 이슈를 생성하고 기존 이슈를 업데이트합니다(upsert 의미론).

메모리 레코드("_type":"memory"가 있는 줄)는 자동 감지되어 영구 메모리로
가져옵니다('bd remember'와 동일). 따라서 'bd export | bd import'는 이슈와
메모리 모두의 완전한 왕복을 지원합니다.

각 JSONL 줄은 이슈 하나에 대응해야 합니다. 가져오기 도구는 'bd export'가 내보내는
모든 필드를 허용합니다. 표준 스키마는 'bd export' 출력을 참조하세요.
"title"만 필수이고 나머지는 선택 사항입니다.

일반 필드:
  title                  필수. 짧은 요약.
  description            긴 형식 본문.
  design, notes,         추가 콘텐츠 섹션.
    acceptance_criteria
  issue_type             bug | feature | task | epic | chore | ...
  priority               0-4(0 = critical). 0은 보존됨(omitempty 없음).
  status                 open | in_progress | blocked | closed | ...
                         (status가 "tombstone"인 행은 건너뜀)
  assignee, owner,       소유권 메타데이터.
    created_by
  labels                 문자열 배열.
  dependencies           &#123;issue_id, depends_on_id, type, ...&#125; 배열.
  comments               댓글 객체 배열.
  external_ref,          시스템 간 식별자(예: "gh-9").
    source_system
  due_at, defer_until    일정용 RFC3339 타임스탬프.
  metadata               그대로 보존되는 임의 JSON 객체.

타임스탬프(created_at, updated_at, started_at, closed_at)가 JSONL에 있으면 보존되고
없으면 가져오기 도구가 채웁니다. 레거시 "wisp" 불리언은 "ephemeral"의 별칭으로
허용됩니다.

기본적으로 행의 updated_at이 엄격히 더 최신일 때만 기존 로컬 이슈를 다시 씁니다.
오래된 행은 건너뛰고(stale_skipped_ids로 보고), updated_at이 같은 행은 모든 로컬 열을
유지합니다. updated_at은 초 단위 정밀도이므로 타임스탬프가 같아도 같은 초에 발생한
서로 다른 업데이트일 수 있으며 로컬 행이 우선합니다(tie_kept_local_ids로 보고,
행의 labels/comments/dependencies는 계속 병합). 보호 장치는 upsert 자체 내부에서도
적용되므로 가져오기 실행 중 반영된 로컬 업데이트가 덮어써지지 않고 보존됩니다.
가져오기가 다시 쓴 기존 이슈는 필드 수준 요약(updated_issues)과 함께 나열되어
가져오기로 변경된 로컬 상태를 확인할 수 있습니다. 오래된 스냅샷을 의도적으로
복원하려면 --allow-stale을 전달하세요. 새 로컬 상태를 덮어쓰더라도 모든 행을 가져옵니다.

예시:
  bd import                        # 구성된 import.path에서 가져오기
  bd import backup.jsonl           # 특정 파일에서 가져오기
  bd import -i backup.jsonl        # 특정 파일용 레거시 별칭
  bd import -                      # stdin에서 JSONL 읽기
  cat issues.jsonl | bd import -   # 다른 도구의 JSONL 파이프
  bd import --dry-run              # 가져올 항목 표시
  bd import --dedup                # 제목이 중복된 이슈 건너뛰기
  bd import --allow-stale old.jsonl # 오래된 스냅샷 복원(새 로컬 행 덮어쓰기)
  bd import --json                 # 생성 및 건너뛴 ID가 포함된 구조화 출력

```
bd import [file|-] [flags]
```

**플래그:**

```
      --allow-stale    로컬 이슈보다 오래된 행도 가져오기(오래된 스냅샷 복원에 필요)
      --dedup          제목이 기존 열린 이슈와 일치하는 줄 건너뛰기
      --dry-run        실제로 가져오지 않고 가져올 항목 표시
  -i, --input string   특정 파일에서 JSONL 읽기
```
