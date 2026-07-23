---
title: Wisp
description: 완료 후 감사 가치가 없는 운영 작업을 위한 임시 Molecule입니다.
---

릴리스 체크리스트, 상태 patrol, 진단 같은 운영 워크플로는 종료 순간 가치가 없어지는
Beads를 만듭니다. **Wisp**는 *기체 단계*에서 인스턴스화한 Molecule입니다. 일반
Beads처럼 작업하지만 `Ephemeral=true`로 표시되어 동기화에서 제외되며 나중에 한꺼번에
삭제할 수 있습니다.

## Wisp란?

- 기본 데이터베이스에 ephemeral flag가 설정된 이슈이며 일반 `bd` 명령으로 작업합니다.
- 설계상 로컬입니다. 기본적으로 federation push에서 제외되고
  (`federation.exclude_types` 기본값은 `[wisp]`) 공유 감사 기록에 포함되지 않습니다.
- 종료 후 `bd purge` 또는 `bd mol wisp gc`로 일괄 삭제합니다.

## Wisp와 Pour 비교

| 측면 | Molecule(`bd mol pour`) | Wisp(`bd mol wisp`) |
|--------|--------------------------|----------------------|
| 지속성 | 영구적이며 기록에 포함 | 임시이며 완료 시 purge |
| 동기화 | 다른 Bead처럼 동기화 | federation push에서 제외 |
| 사용 사례 | 기능 작업, 나중에 참조할 가치가 있는 작업 | 릴리스 실행, 운영 반복 작업, 상태 검사 |

Formula는 `phase = "vapor"`를 선언하여 Wisp 인스턴스화를 권장할 수 있습니다. 기체
단계 Formula를 pour하면 경고가 표시됩니다.

## Wisp 수명 주기

```bash
# 1. 생성 — Proto 또는 임시 생성
bd mol wisp <proto-id> [--var key=value]
bd create "일회성 검사" --ephemeral

# 2. 실행 — 일반 bd 작업을 Wisp 이슈에 사용
bd ready --mol <wisp-id>
bd update <id> --claim
bd close <id>

# 3a. 보존: squash로 영구 상태로 승격(flag 해제)
bd mol squash <wisp-id>

# 3b. 또는 burn: digest를 만들지 않고 삭제
bd mol burn <wisp-id>
```

## Wisp 관리

```bash
bd mol wisp list      # 현재 컨텍스트의 모든 Wisp 나열
bd mol wisp gc        # 오래되거나 중단된 Wisp 가비지 컬렉션
bd purge --force      # 종료된 모든 임시 Bead 삭제
```

## 단계 강제 지정

`bd mol bond`는 작업을 결합할 때 단계 override를 허용합니다.

```bash
bd mol bond mol-critical-bug wisp-patrol --pour   # patrol 중 발견한 버그를 영구 보존
```

## 모범 사례

1. **운영 반복 작업에는 Wisp** — patrol, 릴리스 실행, 진단에 사용합니다.
2. **추적 작업에는 Molecule** — 감사 가치가 있는 작업은 Wisp가 아닌 pour로 만듭니다.
3. **삭제 전에 squash** — Wisp에서 영구 보존할 내용이 나왔다면 `bd mol squash`로
   승격하세요. burn은 되돌릴 수 없습니다.
4. **정기적인 가비지 컬렉션** — `bd mol wisp gc` 또는 `bd purge --force`를 실행하세요.
