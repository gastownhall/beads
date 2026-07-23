---
title: "bd mol"
description: "에이전트 워크플로용 작업 템플릿인 molecule을 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc mol`에서 생성되었습니다.

에이전트 워크플로용 작업 템플릿인 molecule을 관리합니다.

proto는 "template" 레이블이 있는 템플릿 epic입니다. 실제 이슈(molecule)를 생성하도록
스폰할 수 있는 작업 DAG를 정의합니다.

다음은 molecule 비유입니다:
  - proto는 인스턴스화되지 않은 템플릿(재사용 가능한 작업 패턴)
  - 스폰은 proto에서 molecule(실제 이슈)을 생성
  - 스폰 중 변수(&#123;&#123;key&#125;&#125;) 치환
  - bond는 proto 또는 molecule을 compound로 결합
  - distill은 임시 epic에서 proto 추출

명령:
  show       proto/molecule 구조와 변수 표시
  pour       proto를 영구 mol로 인스턴스화(액체 단계)
  wisp       proto를 임시 wisp로 인스턴스화(기체 단계)
  bond       다형적 결합: proto+proto, proto+mol, mol+mol
  squash     molecule을 digest로 응축
  burn       wisp 폐기
  distill    임시 epic에서 proto 추출

사용 가능한 formula를 나열하려면 "bd formula list"를 사용하세요.

```
bd mol [flags]
```

**별칭:** protomolecule

## bd mol bond

두 proto 또는 molecule을 결합해 compound를 생성합니다.

bond 명령은 다형적이며 여러 피연산자 유형을 처리합니다:

  formula + formula → 둘 다 cook하여 compound proto 생성
  formula + proto   → formula를 cook하여 compound proto 생성
  formula + mol     → formula를 cook하고 스폰하여 연결
  proto + proto     → compound proto(재사용 가능한 템플릿)
  proto + mol       → proto를 스폰하여 molecule에 연결
  mol + proto       → proto를 스폰하여 molecule에 연결
  mol + mol         → compound molecule로 결합

formula 이름(예: mol-polecat-arm)은 임시 proto로 인라인 cook됩니다.
따라서 데이터베이스에 미리 cook한 proto bead가 필요하지 않습니다.

bond 유형:
  sequential (기본값) - A 완료 후 B 실행
  parallel            - A와 함께 B 실행
  conditional         - A 실패 시에만 B 실행

단계 제어:
  기본적으로 스폰된 proto는 대상의 단계를 따릅니다:
  - mol(Ephemeral=false)에 연결 → 영구로 스폰(Ephemeral=false)
  - 임시 이슈(Ephemeral=true)에 연결 → 임시로 스폰(Ephemeral=true)

  다음으로 재정의합니다:
  --pour  액체로 스폰 강제(영구, Ephemeral=false)
  --ephemeral  기체로 스폰 강제(임시, Ephemeral=true, dolt_ignore를 통해 Dolt 동기화에서 제외)

동적 bond(Christmas Ornament 패턴):
  --ref를 사용해 변수 치환이 있는 사용자 정의 하위 참조를 지정합니다.
  무작위 해시 대신 "parent.child-ref" 형태의 ID를 생성합니다.

  예시:
    bd mol bond mol-worker-arm bd-patrol --ref arm-&#123;&#123;worker_name&#125;&#125; --var worker_name=ace
    # 생성: bd-patrol.arm-ace(및 bd-patrol.arm-ace.capture 같은 하위 항목)

사용 사례:
  - patrol 중 중요한 버그를 찾았나요? --pour로 영구 보존
  - 영구 feature에 임시 진단이 필요한가요? --ephemeral 사용
  - patrol에서 워커별 arm을 스폰하나요? 읽기 쉬운 ID를 위해 --ref 사용

예시:
  bd mol bond mol-feature mol-deploy                    # 복합 proto
  bd mol bond mol-feature mol-deploy --type parallel    # 병렬 실행
  bd mol bond mol-feature bd-abc123                     # proto를 molecule에 연결
  bd mol bond bd-abc123 bd-def456                       # 두 molecule 결합
  bd mol bond mol-critical-bug wisp-patrol --pour       # 발견한 버그 영구 보존
  bd mol bond mol-temp-check bd-feature --ephemeral          # 임시 진단
  bd mol bond mol-arm bd-patrol --ref arm-&#123;&#123;name&#125;&#125; --var name=ace  # 동적 하위 ID

```
bd mol bond <A> <B> [flags]
```

**별칭:** fart

**플래그:**

```
      --as string         compound proto의 사용자 정의 제목(proto+proto 전용)
      --dry-run           생성될 항목 미리 보기
      --ephemeral         기체로 스폰 강제(임시, Ephemeral=true)
      --pour              액체로 스폰 강제(영구, Ephemeral=false)
      --ref string        {{var}} 치환이 있는 사용자 정의 하위 참조(예: arm-{{polecat_name}})
      --type string       bond 유형: sequential, parallel 또는 conditional(기본값 "sequential")
      --var stringArray   스폰된 proto의 변수 치환(key=value)
```

## bd mol burn

digest를 생성하지 않고 molecule을 삭제합니다.

삭제 전에 영구 digest를 생성하는 squash와 달리 burn은 molecule을 흔적 없이
완전히 제거합니다. 다음에 사용하세요:
  - 중단된 patrol 주기
  - 충돌하거나 실패한 워크플로
  - 보존하지 않을 테스트/디버그 molecule

burn 작업은 molecule 단계에 따라 다릅니다:
  - Wisp(임시): 직접 삭제
  - Mol(영구): 연쇄 삭제(원격과 동기화)

주의: 파괴적인 작업입니다. molecule 데이터가 영구 손실됩니다.
요약을 보존하려면 'bd mol squash'를 사용하세요.

예시:
  bd mol burn bd-abc123              # 흔적 없이 molecule 삭제
  bd mol burn bd-abc123 --dry-run    # 삭제될 항목 미리 보기
  bd mol burn bd-abc123 --force      # 확인 건너뛰기
  bd mol burn bd-a1 bd-b2 bd-c3      # 여러 wisp 일괄 삭제

```
bd mol burn <molecule-id> [molecule-id...] [flags]
```

**플래그:**

```
      --dry-run   삭제될 항목 미리 보기
      --force     확인 프롬프트 건너뛰기
```

## bd mol current

molecule 워크플로에서 현재 위치를 표시합니다.

molecule-id를 지정하면 해당 molecule의 상태를 표시합니다.
지정하지 않으면 현재 에이전트에게 할당된 in_progress 이슈에서 추론합니다.

출력은 상태 표시기와 함께 모든 단계를 보여 줍니다:
  [done]     - 단계 완료(closed)
  [current]  - 단계가 in_progress(현재 위치)
  [ready]    - 단계 시작 준비됨(차단되지 않음)
  [blocked]  - 의존성으로 단계가 차단됨
  [pending]  - 단계 대기 중

큰 molecule(100단계 초과)은 대신 요약을 표시합니다.
특정 단계를 보려면 --limit 또는 --range를 사용하세요:
  bd mol current &lt;id&gt; --limit 50       # 처음 50단계 표시
  bd mol current &lt;id&gt; --range 100-150  # 100~150단계 표시

```
bd mol current [molecule-id] [flags]
```

**플래그:**

```
      --for string     특정 에이전트/담당자의 molecule 표시
      --limit int      표시할 최대 단계 수(0 = 자동, 'all' 임곗값 사용)
      --range string   특정 단계 범위 표시(예: '1-50', '100-150')
```

## bd mol distill

기존 epic에서 재사용 가능한 formula를 추출하여 molecule을 distill합니다.

pour의 반대입니다. formula → molecule 대신 molecule → formula입니다.

distill 명령은 다음을 수행합니다:
  1. 기존 epic과 모든 하위 이슈 로드
  2. 구조를 .formula.json 파일로 변환
  3. 구체적 값을 &#123;&#123;variable&#125;&#125; 자리표시자로 교체(--var 플래그 사용)

사용 사례:
  - 팀이 자연스럽게 좋은 워크플로를 개발하고 재사용하려는 경우
  - 암묵지를 실행 가능한 템플릿으로 캡처
  - 향후 유사 작업의 시작점 생성

변수 구문(둘 다 작동하며 어느 쪽이 구체적 값인지 감지):
  --var branch=feature-auth    스폰 방식: variable=value(권장)
  --var feature-auth=branch    치환 방식: value=variable

출력 위치(처음 쓰기 가능한 위치 사용):
  1. &lt;resolved-beads-dir&gt;/formulas/(프로젝트 수준, 기본값)
  2. &lt;checkout-root&gt;/.beads/formulas/(저장소 로컬 formula)
  3. ~/.beads/formulas/(사용자 수준, 프로젝트에 쓸 수 없는 경우)

예시:
  bd mol distill bd-o5xe my-workflow
  bd mol distill bd-abc release-workflow --var feature_name=auth-refactor

```
bd mol distill <epic-id> [formula-name] [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --output string     formula 파일 출력 디렉터리
      --var stringArray   값을 {{variable}} 자리표시자로 교체(variable=value)
```

## bd mol last-activity

molecule의 가장 최근 활동 타임스탬프를 표시합니다.

해당 molecule 내 모든 단계 중 가장 최근 변경 타임스탬프를 반환하여
오래되거나 멈춘 molecule을 쉽게 감지할 수 있습니다.

활동 소스:
  step_closed      - 단계가 닫힘
  step_updated     - 단계가 업데이트됨(맡기, 편집 등)
  molecule_updated - molecule 루트 자체가 업데이트됨

예시:
  bd mol last-activity hq-wisp-0laki
  bd mol last-activity hq-wisp-0laki --json

```
bd mol last-activity <molecule-id> [flags]
```

## bd mol pour

녹은 금속을 주형에 붓듯 proto를 영구 mol에 붓습니다.

템플릿에서 영구 작업을 생성하는 화학 개념의 명령입니다.
결과 mol은 .beads/(영구 저장소)에 존재하며 git과 동기화됩니다.

단계 전이: Proto(고체) -&gt; pour -&gt; Mol(액체)

POUR와 WISP 사용 시점:
  pour(액체): 감사 추적이 필요한 영구 작업
    - 여러 세션에 걸친 기능 구현
    - 나중에 참조해야 할 수 있는 작업
    - git 이력에 보존할 가치가 있는 모든 항목

  wisp(기체): 자동 정리되는 임시 작업
    - 릴리스 워크플로(일회성 실행)
    - 운영 루프와 반복 주기
    - 상태 검사와 진단
    - 감사 가치가 없는 모든 운영 워크플로

팁: formula는 wisp 사용을 권장하도록 phase:"vapor"를 지정할 수 있습니다.
    기체 단계 formula를 pour하면 경고가 표시됩니다.

예시:
  bd mol pour mol-feature --var name=auth    # 영구 기능 작업
  bd mol pour mol-review --var pr=123        # 영구 코드 리뷰

```
bd mol pour <proto-id> [flags]
```

**플래그:**

```
      --assignee string      루트 이슈를 이 에이전트/사용자에게 할당
      --attach strings       스폰 후 연결할 proto(반복 가능)
      --attach-type string   연결할 bond 유형: sequential, parallel 또는 conditional(기본값 "sequential")
      --dry-run              생성될 항목 미리 보기
      --var stringArray      변수 치환(key=value)
```

## bd mol progress

molecule의 효율적인 진행 상황 요약을 표시합니다.

이 명령은 모든 단계를 로드하지 않고 인덱스 쿼리로 진행 상황을 계산하므로
수백만 단계의 매우 큰 molecule에 적합합니다.

molecule-id를 지정하지 않으면 현재 작업 중인 molecule의 진행 상황을 표시합니다.

출력 항목:
  - 진행 상황: 완료/전체(백분율)
  - 현재 단계: 진행 중인 단계(있는 경우)
  - 속도: 닫은 시간을 기준으로 한 단계/시간
  - ETA: 완료 예상 시간

예시:
  bd mol progress bd-hanoi-xyz

```
bd mol progress [molecule-id] [flags]
```

## bd mol ready

gate가 닫혀 워크플로를 재개할 준비가 된 molecule을 찾습니다.

이 명령은 다음 조건으로 gate 단계에서 대기 중인 molecule을 찾습니다:
1. molecule에 단계를 차단하는 gate bead가 있음
2. gate bead가 현재 닫힘(조건 충족)
3. 차단된 단계가 이제 진행할 준비가 됨
4. 현재 어떤 에이전트도 이 molecule을 hook하지 않음

명시적인 대기자 추적 없이 검색 기반 재개를 지원합니다.
patrol 시스템은 이를 사용해 gate 준비 molecule을 찾아 디스패치합니다.

예시:
  bd mol ready --gated           # 모든 gate 준비 molecule 찾기
  bd mol ready --gated --json    # 자동화용 JSON 출력

```
bd mol ready --gated [flags]
```

## bd mol seed

formula에 접근할 수 있고 cook할 수 있는지 확인합니다.

seed 명령은 formula 검색 경로를 검사해 formula가 존재하고 로드 가능한지 확인합니다.
formula에서 작업을 스폰하기 전에 시스템 상태를 확인하는 데 유용합니다.

formula 검색 경로(순서대로 검사):
  1. &lt;resolved-beads-dir&gt;/formulas/(활성 프로젝트)
  2. &lt;checkout-root&gt;/.beads/formulas/(저장소 로컬 formula)
  3. ~/.beads/formulas/(사용자 수준)
  4. $GT_ROOT/.beads/formulas/(공유 워크스페이스 루트, GT_ROOT가 설정된 경우)

예시:
  bd mol seed mol-feature                 # 특정 formula 확인
  bd mol seed mol-review --var name=test  # 변수 치환을 사용해 확인

```
bd mol seed <formula-name> [flags]
```

**플래그:**

```
      --var stringArray   조건 필터링용 변수 치환(key=value)
```

## bd mol show

molecule 구조와 세부 정보를 표시합니다.

--parallel 플래그는 병렬화 가능한 단계를 강조합니다:
  - 차단 의존성이 없는 단계는 병렬로 실행 가능
  - 지금 시작할 준비가 된 단계 표시
  - 병렬 그룹(동시에 실행할 수 있는 단계) 식별

예시:
  bd mol show bd-patrol --parallel

```
bd mol show <molecule-id> [flags]
```

**플래그:**

```
  -p, --parallel   병렬 단계 분석 표시
```

## bd mol squash

molecule의 임시 하위 이슈를 단일 digest 이슈로 squash합니다.

이 명령은 molecule의 모든 임시 하위 이슈(Ephemeral=true)를 수집하고 요약 digest를
생성하며 Wisp 플래그를 지워 wisp를 영구로 승격합니다(또는 선택적으로 삭제).

squash 작업:
  1. molecule과 모든 하위 이슈 로드
  2. wisp(Ephemeral=true인 임시 이슈)만 필터링
  3. digest(완료한 작업 요약) 생성
  4. 영구 digest 이슈 생성(Ephemeral=false)
  5. 하위 이슈의 Wisp 플래그 지우기(영구로 승격)
     또는 --keep-children으로 유지(기본값: 삭제)

에이전트 통합:
--summary로 AI 생성 요약을 제공하세요. 이렇게 하면 bd를 순수 도구로 유지하고
호출 에이전트(오케스트레이터 워커, Claude Code 등)가 지능적인 요약 생성을 담당합니다.
--summary가 없으면 하위 이슈 콘텐츠를 단순 연결하여 사용합니다.

wisp 워크플로의 일부입니다. spawn이 wisp를 생성하고 실행이 진행된 뒤
squash가 추적을 결과(digest)로 압축합니다.

예시:
  bd mol squash bd-abc123                    # 하위 이슈 squash 및 승격
  bd mol squash bd-abc123 --dry-run          # squash될 항목 미리 보기
  bd mol squash bd-abc123 --keep-children    # digest 후 wisp 유지
  bd mol squash bd-abc123 --summary "에이전트가 생성한 작업 요약"

```
bd mol squash <molecule-id> [flags]
```

**플래그:**

```
      --dry-run          squash될 항목 미리 보기
      --keep-children    squash 후 임시 하위 이슈를 삭제하지 않음
      --summary string   에이전트 제공 요약(자동 생성 우회)
```

## bd mol stale

완료되었지만 아직 열린 molecule(하위 이슈가 있는 epic)을 감지합니다.

molecule은 다음 조건에서 오래된 것으로 간주됩니다:
  1. 모든 하위 이슈가 닫힘(Completed == Total)
  2. 루트 이슈는 아직 열림
  3. 누구에게도 할당되지 않음(선택 사항, --unassigned 사용)
  4. 다른 작업을 차단 중(선택 사항, --blocking 사용)

기본적으로 완료되었지만 닫히지 않은 모든 molecule을 표시합니다.

예시:
  bd mol stale              # 오래된 모든 molecule 나열
  bd mol stale --json       # 기계 판독 가능 출력
  bd mol stale --blocking   # 다른 작업을 차단하는 항목만 표시
  bd mol stale --unassigned # 할당되지 않은 molecule만 표시
  bd mol stale --all        # 하위 이슈가 0개인 molecule 포함

```
bd mol stale [flags]
```

**플래그:**

```
      --all          하위 이슈가 0개인 molecule 포함
      --blocking     다른 작업을 차단하는 molecule만 표시
      --unassigned   할당되지 않은 molecule만 표시
```

## bd mol wisp

운영 워크플로용 임시 molecule인 wisp를 생성하거나 관리합니다.

proto-id 인수로 호출하면 해당 proto에서 wisp를 생성합니다.
하위 명령(list, gc)으로 호출하면 기존 wisp를 관리합니다.

wisp는 기본 데이터베이스에서 Ephemeral=true인 이슈입니다. 로컬에 저장되지만
git으로 동기화되지 않습니다.

WISP와 POUR 사용 시점:
  wisp(기체): 자동 정리되는 임시 작업
    - 릴리스 워크플로(일회성 실행)
    - 운영 루프와 반복 주기
    - 상태 검사와 진단
    - 감사 가치가 없는 모든 운영 워크플로

  pour(액체): 감사 추적이 필요한 영구 작업
    - 여러 세션에 걸친 기능 구현
    - 나중에 참조해야 할 수 있는 작업
    - git 이력에 보존할 가치가 있는 모든 항목

팁: formula는 wisp 사용을 권장하도록 phase:"vapor"를 지정할 수 있습니다.
    기체 단계 formula에 pour를 사용하면 경고가 표시됩니다.

wisp 수명 주기:
  1. 생성: bd mol wisp &lt;proto&gt; 또는 bd create --ephemeral
  2. 실행: 일반 bd 작업이 wisp 이슈에서 작동
  3. squash: bd mol squash &lt;id&gt;(Ephemeral 플래그를 지우고 영구로 승격)
  4. 또는 burn: bd mol burn &lt;id&gt;(digest를 생성하지 않고 삭제)

예시:
  bd mol wisp beads-release --var version=1.0  # 릴리스 워크플로
  bd mol wisp mol-my-workflow                  # 임시 운영 주기
  bd mol wisp list                             # 모든 wisp 나열
  bd mol wisp gc                               # 오래된 wisp 가비지 컬렉션

하위 명령:
  list  현재 컨텍스트의 모든 wisp 나열
  gc    고립된 wisp 가비지 컬렉션

```
bd mol wisp [proto-id] [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --root-only         루트 이슈만 생성(하위 단계 이슈 없음)
      --var stringArray   변수 치환(key=value)
```

### bd mol wisp create

proto에서 wisp를 생성합니다. 고체에서 기체로 승화하는 과정입니다.

템플릿에서 임시 작업을 생성하는 화학 개념의 명령입니다.
결과 wisp는 Ephemeral=true로 기본 데이터베이스에 저장되며 git으로 동기화되지 않습니다.

단계 전이: Proto(고체) -&gt; Wisp(기체)

wisp 사용 사례:
  - 운영 루프와 반복 주기
  - 상태 검사와 모니터링
  - 일회성 오케스트레이션 실행
  - 감사 가치가 없는 일상 작업

wisp는 다음과 같이 처리됩니다:
  - Ephemeral=true 플래그로 기본 데이터베이스에 저장
  - git으로 동기화되지 않음
  - 증발(burn)하거나 digest로 응축(squash)

예시:
  bd mol wisp create mol-patrol                    # 임시 patrol 주기
  bd mol wisp create mol-health-check              # 일회성 상태 검사
  bd mol wisp create mol-diagnostics --var target=db  # 진단 실행

```
bd mol wisp create <proto-id> [flags]
```

**플래그:**

```
      --dry-run           생성될 항목 미리 보기
      --root-only         루트 이슈만 생성(하위 단계 이슈 없음)
      --var stringArray   변수 치환(key=value)
```

### bd mol wisp gc

데이터베이스에서 오래되거나 중단된 wisp를 가비지 컬렉션합니다.

wisp는 다음 조건에서 중단된 것으로 간주됩니다:
  - --age 기간 동안 업데이트되지 않았고 닫히지 않음

중단된 wisp는 digest를 생성하지 않고 삭제됩니다. 가비지 컬렉션 전에 요약을
보존하려면 'bd mol squash'를 사용하세요.

--closed를 사용하면 나이에 관계없이 닫힌 모든 wisp를 제거합니다. 쌓인 wisp로
비대해진 공간을 회수하는 가장 빠른 방법입니다. 기본적으로 안전하며 실제 삭제에는
--force가 필요합니다.

참고: 임시 wisp에 적합한 시간 기반 정리를 사용합니다.
그래프 압력 기반 오래됨 감지(다른 작업 차단)는 'bd mol stale'을 참조하세요.

예시:
  bd mol wisp gc                                    # 중단된 wisp 정리(기본값: 1h 임곗값)
  bd mol wisp gc --dry-run                          # 정리될 항목 미리 보기
  bd mol wisp gc --age 24h                          # 사용자 정의 나이 임곗값
  bd mol wisp gc --all                              # 임곗값보다 오래된 닫힌 wisp도 정리
  bd mol wisp gc --closed                           # 닫힌 wisp 삭제 미리 보기
  bd mol wisp gc --closed --force                   # 닫힌 wisp 모두 삭제
  bd mol wisp gc --closed --dry-run                 # 명시적 dry-run(--force 없음과 동일)
  bd mol wisp gc --exclude-type agent,rig           # agent 및 rig wisp를 GC에서 보호
  bd mol wisp gc --closed --force --exclude-type mol # mol 유형을 제외한 닫힌 wisp 삭제

```
bd mol wisp gc [flags]
```

**플래그:**

```
      --age string             중단된 wisp 감지용 나이 임곗값(기본값 "1h")
      --all                    임곗값보다 오래된 닫힌 wisp도 정리
      --closed                 닫힌 모든 wisp 삭제(--age 임곗값 무시)
      --dry-run                정리될 항목 미리 보기
      --exclude-type strings   해당 유형의 wisp를 GC에서 제외(쉼표로 구분, 예: agent,rig)
  -f, --force                  실제 삭제(기본값: 미리 보기만)
```

### bd mol wisp list

현재 컨텍스트의 모든 wisp(임시 molecule)를 나열합니다.

wisp는 기본 데이터베이스에서 Ephemeral=true인 이슈입니다. 로컬에 저장되지만
git으로 동기화되지 않습니다.

목록 표시 항목:
  - ID: wisp의 이슈 ID
  - Title: wisp 제목
  - Status: 현재 상태(open, in_progress, closed)
  - Started: wisp 생성 시점
  - Updated: 마지막 수정 시간

오래된 wisp 감지:
  - 오래된 wisp는 24시간 이상 업데이트되지 않음
  - 오래되거나 중단된 wisp를 정리하려면 'bd mol wisp gc' 사용

예시:
  bd mol wisp list              # 모든 wisp 나열
  bd mol wisp list --json       # 프로그래밍용 JSON 출력
  bd mol wisp list --all        # 닫힌 wisp 포함

```
bd mol wisp list [flags]
```

**플래그:**

```
      --all           닫힌 wisp 포함
      --type string   이슈 유형으로 필터링(예: agent, task, patrol)
```
