---
title: Molecule
description: Molecule은 하위 항목이 순서가 있는 단계로 bd ready를 통해 흐르는 epic입니다. 생성, 실행, bonding, 수명 주기를 설명합니다.
---

Molecule은 하위 항목이 의존성 순서가 있는 단계로 `bd ready`를 통해 흐르는 작업
그래프, 즉 epic입니다. 일반적으로 Formula에서 인스턴스화하지만 Formula는 선택
사항입니다. 하위 항목이 있는 모든 epic이 Molecule입니다.

## Molecule이란?

Molecule은 Proto(조리된 Formula)의 영구 인스턴스입니다.

- 의존성이 있는 단계를 포함합니다.
- 이슈 데이터베이스에 영구 Beads로 저장되고 다른 Bead처럼 동기화됩니다.
- 단계는 parent-child 관계가 있는 이슈에 대응합니다.

내부적으로 **Molecule은 하위 항목이 있는 상위 Bead인 epic**에 워크플로 의미를 더한
것입니다.

| 용어 | 의미 | 사용 시점 |
|------|---------|-------------|
| **Epic** | 하위 항목이 있는 상위 이슈 | 계층형 작업의 일반 용어 |
| **Molecule** | 실행 의도가 있는 epic | 워크플로 순회를 설명할 때 |
| **Proto** | `template` 레이블이 있는 epic | 재사용 가능한 패턴(선택 사항) |

Proto와 Formula는 재사용 가능한 패턴과 복잡한 조합을 위한 선택적 계층입니다. 대부분의
작업에는 epic과 의존성만 필요합니다.

## Molecule 생성

### Formula에서 생성

```bash
# Formula를 Proto로 조리한 뒤 Proto를 Molecule로 pour
bd cook release.formula.toml
bd mol pour release --var version=1.0.0
```

다음 항목이 생성됩니다.

- 상위 이슈: `bd-xyz`(Molecule root)
- 하위 이슈: `bd-xyz.1`, `bd-xyz.2` 등(단계)

### Formula 없이 생성

epic을 만들고 의존성을 직접 연결합니다.

```bash
bd create "기능 X" -t epic
bd create "설계" -t task --parent <epic-id>
bd create "구현" -t task --parent <epic-id>
bd create "테스트" -t task --parent <epic-id>
bd dep add <implement-id> <design-id>   # 구현에 설계가 필요
bd dep add <test-id> <implement-id>     # 테스트에 구현이 필요
```

임시 epic을 반복해서 사용할 가치가 있다면 `bd mol distill <epic-id>
<formula-name>`으로 재사용 가능한 Formula를 추출하세요.

### Molecule 찾기

```bash
bd mol current           # 작업 중인 Molecule의 현재 위치
bd mol stale             # 완료되었지만 아직 열린 Molecule
bd mol wisp list         # 임시 Molecule(Wisp)
```

### Molecule 보기

```bash
bd mol show <molecule-id>             # 구조와 변수
bd mol show <molecule-id> --parallel  # 동시에 실행할 수 있는 단계 강조
bd dep tree <molecule-id>             # 전체 계층 구조 표시
```

## Molecule 작업

### 실행 모델

에이전트는 Molecule을 선택하고 모든 항목이 종료될 때까지 준비된 하위 항목을 병렬로
실행합니다.

```
epic-root (에이전트에 할당)
├── child.1 (의존성 없음 → 준비됨)      ← 병렬 실행
├── child.2 (의존성 없음 → 준비됨)      ← 병렬 실행
├── child.3 (child.1 필요) → child.1 종료까지 차단
└── child.4 (child.2, child.3 필요) → 둘 다 종료될 때까지 차단
```

**하위 항목은 기본적으로 병렬입니다.** 명시적 의존성만 순서를 만듭니다. 다중 세션
반복 절차는 다음과 같습니다.

1. 준비된 작업 가져오기: `bd ready --mol <molecule-id>`
2. 작업 맡기(claim): `bd update <id> --claim`
3. 작업 수행
4. 종료: `bd close <id>`
5. Molecule이 끝날 때까지 반복

### 의존성 유형

일부 의존성 유형만 실행을 차단합니다.

| 유형 | 의미 | 사용 사례 |
|------|-----------|----------|
| `blocks` | A가 종료될 때까지 B를 시작할 수 없음 | 작업 순서 지정 |
| `parent-child` | 상위 항목이 차단되면 하위 항목도 차단 | 계층 구조(하위 항목은 기본적으로 병렬) |
| `conditional-blocks` | A가 실패할 때만 B 실행 | 오류 처리 경로 |
| `waits-for` | B가 A의 동적 하위 항목을 모두 대기 | fan-in Gate. [Gate](/workflows/gates) 참고 |

차단하지 않는 유형(`related`, `discovered-from`, `replies-to`)은 실행에 영향을 주지
않고 이슈를 연결합니다.

### 단계 의존성

Formula에서 단계는 `needs`를 선언합니다.

```toml
[[steps]]
id = "implement"
title = "기능 구현"
needs = ["design"]  # 먼저 설계를 완료해야 함
```

실제 이슈에서는 edge를 직접 추가합니다. 의존하는 항목이 먼저 옵니다.

```bash
bd dep add <B-id> <A-id>   # B가 A에 의존(B에 A가 필요)
```

`bd ready` 명령은 이러한 의존성을 따릅니다.

```bash
bd ready --mol <molecule-id>  # 의존성이 완료된 단계만 표시
```

### 단계 진행

```bash
# 단계 시작
bd update bd-xyz.1 --claim

# 단계 완료
bd close bd-xyz.1 --reason "완료"

# 다음 준비 항목 확인
bd ready --mol bd-xyz
```

### 진행 상황 보기

```bash
# 차단된 단계 확인
bd blocked

# 단계별 상태: [done] / [current] / [ready] / [blocked] / [pending]
bd mol current <molecule-id>

# 진행 요약: 완료/전체, 비율, ETA
bd mol progress <molecule-id>
```

## Molecule 수명 주기

```
Formula (템플릿 소스)
    ↓ bd cook
Proto (템플릿 epic)
    ↓ bd mol pour
Molecule (인스턴스)
    ↓ 작업 단계
완료된 Molecule
    ↓ 선택적 정리
종료 / squash / burn
```

마지막 하위 항목을 종료해도 Molecule root는 종료되지 않습니다. epic은 명시적으로
종료할 때까지 종료 가능한 작업으로 열린 상태를 유지합니다(`bd epic close-eligible`이
일괄 처리). Beads 자체를 정리하는 방법은 다음과 같습니다.

- `bd mol squash <id>`는 Molecule의 임시 하위 항목을 영구 digest 이슈로 압축합니다.
- `bd mol burn <id>`는 digest 없이 Molecule을 완전히 삭제하며 중단되었거나 테스트용인
  실행에 사용합니다.

이 명령이 주로 처리하는 임시 수명 주기는 [Wisp](/workflows/wisps)를 참고하세요.

## Bonding: 작업 그래프 연결

**Bond**는 두 작업 그래프 사이에 의존성을 만드는 것입니다. Molecule A가 Molecule
B를 차단하면 A를 완료할 때 B의 차단이 해제되고 에이전트가 A에서 B로 계속 진행할 수
있습니다. 며칠에 걸쳐 실행될 수 있는 하나의 복합 워크플로가 됩니다.

```bash
bd mol bond A B                    # B가 A에 의존(기본적으로 순차)
bd mol bond A B --type parallel    # B가 A와 함께 실행
bd mol bond A B --type conditional # A가 실패할 때만 B 실행
```

이 명령은 operand에 따라 다형적으로 작동합니다.

| operand | 동작 |
|----------|--------------|
| proto + proto | 복합 Proto(재사용 가능한 템플릿) |
| proto + molecule | Proto를 새 이슈로 생성하여 Molecule에 연결 |
| molecule + molecule | 두 항목을 복합 Molecule로 결합 |
| formula + anything | Formula를 먼저 인라인으로 조리 |

생성된 이슈는 기본적으로 대상의 단계(영구 또는 임시)를 따릅니다. `--pour`로 영구
상태를, `--ephemeral`로 임시 상태를 강제할 수 있습니다. [Wisp](/workflows/wisps)를
참고하세요.

### 동적 Bonding

runtime까지 하위 항목 수를 알 수 없다면 loop에서 `--ref`로 bond하여 무작위 해시 대신
읽을 수 있는 하위 ID를 만드세요.

```bash
# 발견된 worker마다 arm 하나
bd mol bond mol-worker-arm bd-patrol --ref arm-{{name}} --var name=ace
# 생성: bd-patrol.arm-ace 및 bd-patrol.arm-ace.capture 같은 하위 항목
```

## 고급 기능

### Bond 지점

Formula는 조합을 위한 이름이 있는 연결 위치인 bond point를 정의할 수 있습니다. 각
point는 `before_step` 또는 `after_step`을 연결할 단계를 지정하며 선택적으로
`parallel = true`를 사용할 수 있습니다.

```toml
[[compose.bond_points]]
id = "entry"
description = "여기에 설정 작업 연결"
before_step = "design"
```

### 훅

단계 완료 hook은 현재 실행 가능한 Formula action으로 노출되지 않습니다. 과거의
`on_complete.run` 예제는 유효하지 않았습니다. `run`은 Formula 필드가 아니며,
`on_complete` runtime expansion은 전체 연결이 완료될 때까지 별도로 추적됩니다.

### Molecule 할당

pour할 때 Molecule root를 에이전트에 할당한 뒤 각 에이전트의 위치를 추적하세요.

```bash
bd mol pour mol-feature --assignee <agent>   # 생성 시 할당
bd mol current --for <agent>                 # 에이전트의 현재 위치
```

## 에이전트 주의 사항

1. **시간 순서 표현은 의존성을 뒤집게 만듭니다.** "1단계가 2단계보다 먼저다"라는
   표현은 반대 방향인 `bd dep add phase1 phase2`를 쓰게 만들 수 있습니다. 요구
   표현을 사용하세요. "2단계에 1단계가 필요하다"는 `bd dep add phase2 phase1`입니다.
   `bd blocked`로 확인하세요.
2. **단계 번호는 순서를 만들지 않습니다.** "1/2/3단계"라는 이름의 단계도 서로
   의존성을 추가할 때까지 병렬로 실행됩니다.
3. **작업 종료 누락.** 차단 요소를 종료하지 않으면 차단된 이슈는 계속 차단됩니다.
   `bd close <id> --reason "완료"`를 실행하세요.

## 워크플로 예제

```bash
# 1. Formula에서 Molecule 생성
bd cook feature-workflow.formula.toml
bd mol pour feature-workflow --var name="dark-mode"

# 2. 구조 보기
bd dep tree bd-xyz

# 3. 첫 단계 시작
bd update bd-xyz.1 --claim

# 4. 완료 후 진행
bd close bd-xyz.1
bd ready --mol bd-xyz  # 다음 단계 표시

# 5. 완료할 때까지 계속
```

## 관련 문서

- [Formula](/workflows/formulas) - 템플릿 생성
- [Gate](/workflows/gates) - 비동기 조정
- [Wisp](/workflows/wisps) - 임시 워크플로
