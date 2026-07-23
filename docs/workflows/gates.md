---
title: Gate
description: 사람의 결정, 타이머, GitHub run 또는 PR처럼 외부 조건이 충족될 때까지 워크플로 단계를 대기시키는 비동기 조건입니다.
---

일부 워크플로 단계는 코드만으로 진행할 수 없습니다. 릴리스는 CI 성공을 기다려야 하고,
배포는 사람의 승인이 필요하며, 정리는 24시간을 기다려야 할 수 있습니다. **Gate**는
이런 대기를 나타내는 이슈입니다. 다른 차단 요소와 같은 방식으로 단계를 차단하므로
Gate가 종료될 때까지 단계가 준비 frontier에서 빠집니다. 따라서 에이전트가 polling하거나
반복 대기할 필요가 없습니다.

## Gate 작동 방식

Gate는 다른 Bead와 같습니다. 열린 상태로 생성되고 일반 의존성 edge를 통해 대기 중인
항목을 차단하며, Gate가 종료되는 순간 단계가 준비됩니다. Gate는 다음 두 방법 중
하나로 종료됩니다.

- **수동** — `bd gate resolve <gate-id>`를 사용합니다. human Gate는 항상 이 방식으로
  종료됩니다.
- **`bd gate check` 사용** — 열린 timer 및 GitHub Gate를 실제 상태와 비교하고 조건을
  충족한 Gate를 종료합니다.

```bash
bd gate list                 # 열린 Gate
bd gate list --all           # 종료된 Gate 포함
bd gate show <gate-id>       # 상세 정보와 대기 항목
bd gate check                # 열린 Gate 평가 후 조건을 충족한 항목 종료
bd gate check --dry-run      # 종료하지 않고 보고
bd gate resolve <gate-id>    # Gate 수동 종료
```

## Gate 유형

| 유형 | 대기 조건 | 종료 방법 |
|------|-----------|-----------|
| `human` | 사람의 결정 | `bd gate resolve`만 사용 |
| `timer` | Gate 생성 후 지정 시간 | timeout 경과 후 `bd gate check` |
| `gh:run` | GitHub Actions 워크플로의 성공적 완료 | `bd gate check`(`gh run view` 사용) |
| `gh:pr` | PR 병합 | `bd gate check`(`gh pr view` 사용) |
| `bead` | 다른 rig의 Bead 종료 | 현재 해결 불가. multi-rig routing이 제거되어 `bd gate check`가 검사 불가능으로 보고 |

timeout은 Go duration 문법인 `30m`, `1h`, `24h`를 사용합니다. `d` 단위는 없으므로
`1d`가 아닌 `24h`를 쓰세요.

## Formula의 Gate

Formula 단계는 `[steps.gate]` block으로 Gate를 선언합니다. Formula를 인스턴스화하면
`bd`가 Gate 이슈를 생성하여 해당 단계의 차단 요소로 연결합니다. schema에는 `type`,
`id`, `await_id`, `timeout` 네 필드가 있습니다.

다음은 Beads 자체 release Formula의 release Gate로, GitHub release 워크플로를 기다리는
단계입니다.

```toml
[[steps]]
id = "wait-for-ci"
title = "릴리스 워크플로 대기"

[steps.gate]
type = "gh:run"
id = "release.yml"       # 감시할 워크플로
timeout = "30m"          # 더 오래 걸리면 에스컬레이션
```

사람의 승인 Gate:

```toml
[[steps]]
id = "approve-deploy"
title = "사람이 배포 승인"

[steps.gate]
type = "human"
```

대기 시간을 두는 timer:

```toml
[[steps]]
id = "wait-24h"
title = "릴리스 안정화 대기"

[steps.gate]
type = "timer"
timeout = "24h"
```

pour하기 전에 parser가 실제로 이해한 내용을 확인하세요. TOML의 알 수 없는 key는
조용히 삭제됩니다.

```bash
bd formula show <formula> --json   # 파싱된 Gate 블록 검사
```

## Formula 외부에서 Gate 생성

`bd gate create`는 기존 작업에 Gate를 연결합니다.

```bash
# PR이 merge될 때까지 bd-abc 차단
bd gate create --type=gh:pr --blocks bd-abc --await-id=42

# 사람이 Gate를 해결할 때까지 bd-abc 차단
bd gate create --type=human --blocks bd-abc --reason "설계 승인"

# 기존 Gate에 다른 대기 항목 추가
bd gate add-waiter <gate-id> <issue-id>
```

## Fan-in: 다른 단계 대기

*다른 단계*를 기다리는 것은 Gate가 아니라 의존성입니다. 이름이 있는 단계의 fan-in에는
`needs`를 사용하고, 동적으로 생성된 하위 항목을 기다려야 하는 단계에는 `waits_for`를
사용하세요.

```toml
[[steps]]
id = "merge-results"
title = "결과 merge"
needs = ["test-a", "test-b"]     # 이름이 있는 단계의 fan-in

[[steps]]
id = "summarize"
title = "생성된 모든 작업 요약"
waits_for = "all-children"       # 또는 "any-children", "children-of(step-id)"
```

## Gate가 있는 Molecule 작업

```bash
bd ready --gated        # Gate가 방금 종료되어 재개할 수 있는 Molecule
bd blocked              # 대기 중인 항목과 해당 Gate
```

자동화 패턴: timer와 GitHub Gate가 사람의 개입 없이 종료되도록 일정(cron, CI 또는
orchestrator loop)에 따라 `bd gate check`를 실행하세요. 자동 종료해서는 안 되는
결정에는 `human` Gate를 유지하세요.
