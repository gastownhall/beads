---
title: 복구 플레이북
description: 기본 키 포크 플레이북을 포함한 bd init 및 bd dolt push/pull 거부의 단계별 복구
---

Last reviewed: 2026-06-09

Freshness source: `cmd/bd/init.go`, `cmd/bd/init_safety.go`,
`cmd/bd/init_safety_test.go`, `cmd/bd/dolt.go`.

이 문서는 ADR 옆에 있으며 `bd` 오류 메시지의 구조와 일치합니다. `bd init`과
`bd dolt push`/`pull`에서 이름이 붙은 각 거부는 단계별 복구 지침이 있는 이 문서의
레이블 지정 앵커를 가리킵니다.

함께 보기: `bd help init-safety`,
[ADR 0002 — `bd init` 안전 불변 조건](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md).

## 목차

- [init-force-refused — origin에 Dolt 기록이 있어 `bd init --force`/`--reinit-local` 거부](#init-force-refused)
- [init-token-missing — `--destroy-token`이 없거나 잘못되어 `--discard-remote` 거부](#init-token-missing)
- [init-local-exists — 로컬 데이터가 이미 있어 `bd init` 거부](#init-local-exists)
- [pk-fork-refused — 공통 조상의 테이블 기본 키가 달라 `bd dolt pull`/`push` 거부](#pk-fork-refused)

---

## init-force-refused

**종료 코드:** `10` (`ExitRemoteDivergenceRefused`)

**증상**

```
bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).
  Why: this init mode would create or reuse local history instead of
       adopting the remote. ...
```

**발생 이유**

`bd init --force`(또는 `--reinit-local`)는 로컬 데이터 안전 가드를 우회하도록
`bd`에 지시합니다. `bd init --from-jsonl`은 로컬 JSONL 내보내기를 소스로
선택합니다. 하지만 원격에는 이미 프로젝트 기록이 있습니다. 계속하면 origin에
공통 조상이 없는 고아 로컬 Dolt 브랜치가 생성됩니다. 다음 `bd dolt push`는
실패하거나(공통 조상 없음), 더 나쁘게는 강제 푸시할 경우 팀 데이터를 파괴합니다.

**복구 경로**

의도에 맞는 경로를 선택하세요.

### 1. 원격 기록을 채택하려는 경우(가장 일반적)

```
bd bootstrap
```

원격 Dolt 데이터베이스를 새로운 로컬 `.beads/`로 클론합니다.
로컬 상태는 무시되고 팀 기록이 로컬 기록이 됩니다.

### 2. 결정하기 전에 잘못된 점을 진단하려는 경우

```
bd doctor
bd dolt status
```

`bd doctor`는 로컬 및 원격 상태를 살펴보고 구체적인 문제를 알려 줍니다.
`bd dolt status`는 Dolt 수준의 보기를 표시합니다. 둘 다 아무것도 수정하지 않습니다.

### 3. 의도적으로 원격 기록을 덮어쓰려는 경우(파괴적)

모든 협업자에게 영향을 주는 경계 간 작업입니다. 로컬 소스 초기화
(`--reinit-local` 또는 `--from-jsonl`)와 `--discard-remote`를 함께 사용해야
합니다. 대화형 모드에서는 `bd`가 확인을 요청하며, 비대화형 모드에서는
`--destroy-token`을 제공해야 합니다. 토큰 형식은 `bd help init-safety`를
참조하세요.

`bd init --reinit-local --discard-remote` 후 다음 `bd dolt push`는 기록을
교체하는 푸시여야 합니다. 실행하기 전에 팀과 조율하세요.

---

## init-token-missing

**종료 코드:** `12` (`ExitDestroyTokenMissing`)

**증상**

```
bd init refuses: --discard-remote requires an explicit destroy-token in non-interactive mode.
```

**발생 이유**

비대화형 환경(CI, 에이전트, 파이프 입력)에서 `--discard-remote`를 전달했습니다.
파괴적인 경계 간 작업은 묵시적으로 승인할 수 없습니다.

**복구 경로**

### 1. 대화형으로 실행

TTY에서 다시 실행하세요. `bd init --reinit-local --discard-remote`가 확인 시점에
destroy-token 입력을 요청합니다.

### 2. 토큰을 명시적으로 제공(CI/자동화)

토큰 형식은 `DESTROY-<issue-prefix>`입니다. 이슈 접두사가 `bd`인 프로젝트에서는
다음과 같습니다.

```
bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd
```

자동화에서는 오류 출력이 아니라 프로젝트 상태를 바탕으로 토큰을 템플릿화해야 합니다.
`bd` 오류 메시지에 토큰을 절대 표시하지 않는 이유는
[ADR 0002 — 불변 조건 4](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md)를 참조하세요.

---

## init-local-exists

**종료 코드:** `11` (`ExitLocalExistsRefused`)

**증상**

```
Refusing to destroy N issues in non-interactive mode.
  See 'bd help init-safety' for the required --destroy-token format.
```

또는 대화형 모드에서 `destroy N issues`를 입력하는 확인 요청을 거부했습니다.

**발생 이유**

로컬 `.beads/`에 기존 이슈가 있습니다. `bd init --reinit-local`은 해당 이슈를
영구적으로 파괴합니다.

**복구 경로**

### 1. 먼저 내보낸 다음 진행

```
bd export > issue-export.jsonl
bd init --reinit-local
```

`issue-export.jsonl`을 사용하면 필요할 때 개별 이슈를 다시 가져올 수 있습니다. 이는
전체 데이터베이스 백업이 아닙니다. Dolt 데이터베이스 상태가 충분히 정상이라면 다시
초기화하기 전에 `bd backup`으로 복원 가능한 백업을 만드세요.

### 2. 이 상황이 발생한 이유 조사

여기서 `bd init`이 올바른 명령일 것으로 예상하지 않았다면 먼저 `bd doctor`를
실행하세요. 다시 초기화해도 해결되지 않는 서버 구성 문제일 수 있습니다.

---

## pk-fork-refused

**증상**

```
$ bd dolt pull
Error: ... cannot merge because table dependencies has different primary keys in its common ancestor
```

(또는 `in its common ancestor`가 없는 변형). `bd`는 오류 뒤에 아래 복구 절차의
짧은 버전을 표시합니다.

**발생 이유**

병합되는 두 기록은 행 내용이 아니라 테이블의 *기본 키 집합*에 대해 일치하지 않습니다.
Dolt는 행을 셀 단위로 병합할 수 있지만, 양쪽에서 기본 키가 서로 다르게 재구성된
테이블(또는 공통 조상의 기본 키가 양쪽 모두와 다른 테이블)은 병합을 즉시 거부합니다.
행 충돌이 구체화되기 전에 거부되므로 `bd dolt pull`의 자동 충돌 해결기는 실행될
기회를 얻지 못합니다. **재시도는 절대 도움이 되지 않습니다.** 두 기록은 영구적으로
병합할 수 없습니다.

일반적인 원인은 기본 키를 재구성하는 스키마 마이그레이션이 포함된 릴리스를 두 클론에서
독립적으로 업그레이드하는 동안 양쪽 모두에 동기화되지 않은 변경이 있었기 때문입니다.
구체적으로 [#4259](https://github.com/gastownhall/beads/issues/4259) 사고에서는
`dependencies`의 `0041`/`0043`/`0050` 재구성(v1.0.4에서 v1.0.6)을 걸쳐 있던
클론 양쪽에 푸시되지 않은 의존성 수정이 있을 때 업그레이드 후 첫 풀에서 정확히 이 문제가
발생했습니다.

원격 마이그레이션 방지 게이트(v1.0.6 이상)는 이 문제가 생성되지 않도록 합니다. 원격이
연결된 데이터베이스의 자동 마이그레이션을 거부하고 하나의 마이그레이터를 지정하도록
안내합니다. 이 플레이북은 포크가 이미 존재할 때 사용합니다.

**복구: 하나의 정식 클론에서 부트스트랩**

포크된 기록은 병합할 수 없으므로 한쪽을 정식 클론으로 선택하고 다른 모든 클론을
여기서 다시 클론해야 합니다. 이슈 *데이터*는 JSONL 내보내기/가져오기로 유지되며,
비정식 클론에서는 병합할 수 없는 Dolt *기록*만 폐기됩니다.

### 1. 정식 클론 선택

보통 가장 완전하거나 가장 최근에 활성화된 클론을 선택합니다. 비교하려면 각 클론에서
다음을 실행하세요(읽기 전용).

```
bd stats
bd dolt status
```

### 2. 정식 클론에서 업그레이드, 마이그레이션, 강제 푸시

```
bd version                 # 새 bd 바이너리 확인
bd doctor                  # 게시 전 정상 여부 확인
bd dolt push --force       # 원격을 기준으로 설정
```

(여기서 `bd`의 마이그레이션 게이트가 차단할 수 있습니다. 이는 게이트가 요구하는 바로 그
지정 마이그레이터 사례입니다. 정식 클론에 표시되는 안내를 따르세요.)

### 3. 다른 모든 클론에서 로컬 전용 작업 저장, 다시 클론, 재적용

```
bd export --all -o /tmp/beads-local.jsonl    # 동기화되지 않은 작업의 안전망
rm -rf .beads/dolt                           # 병합할 수 없는 기록 폐기
bd bootstrap                                 # 원격에서 다시 클론
bd import /tmp/beads-local.jsonl             # 로컬 전용 작업 재적용
```

`bd import`에는 upsert 의미 체계가 있습니다. 이 클론에만 있던 이슈는 다시 생성되고,
더 새로운 로컬 수정은 적용되며, 원격에 이미 있는 것보다 오래된 행은 건너뜁니다.
이후 `bd stats`로 표본을 확인하세요.

### 예방(기본 키 재구성 마이그레이션을 거치는 업그레이드)

- **업그레이드 전 동기화**: 모든 클론이 아직 *이전* 버전을 실행하는 동안 각 클론에서
  `bd dolt push` + `bd dolt pull`을 실행한 다음 수정을 중지합니다. 새 바이너리를
  설치하면 `bd dolt push`/`bd dolt pull`도 게이트로 차단되므로 먼저 수행해야 합니다.
- **하나의 지정 마이그레이터**: 한 머신을 업그레이드해 마이그레이션한 다음
  `bd dolt push`를 실행합니다.
- **다른 모든 클론은 풀하지 않고 채택**: 마이그레이터가 푸시한 뒤 다른 각 클론에서
  바이너리를 업그레이드하고 `bd bootstrap`을 실행해 마이그레이션된 데이터베이스를
  채택합니다. 클론에 대기 중인 마이그레이션이 있으면 `bd dolt pull`이 *거부*되므로
  여기에 의존하지 마세요. `bd bootstrap`은 로컬 데이터베이스를 교체하므로 위의
  "업그레이드 전 동기화" 단계가 이 클론들의 작업을 보존합니다.
