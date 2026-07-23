---
title: 워크플로
description: 여러 단계의 작업을 Formula로 한 번 선언한 뒤 의존성 순서가 있는 실제 Beads의 Molecule로 생성합니다.
---

릴리스 체크리스트, 기능 파이프라인, 검토 절차와 같이 반복되는 다단계 작업을 매번
수동으로 다시 계획할 필요는 없습니다. Beads에서는 구조를 한 번 선언하고 필요할 때
인스턴스화할 수 있습니다. **Formula**(TOML 소스)를 조리하여 **Proto**(템플릿)로
만들고, Proto를 부어 **Molecule**을 만듭니다. Molecule은 다른 작업처럼 각 단계가
`bd ready`를 따라 흐르는 실제 Beads입니다. 전체 파이프라인은
[Beads 작동 방식](/core-concepts/index)의 다이어그램을 참고하세요.

```bash
bd formula list                    # 검색 경로에서 보이는 Formula
bd cook release.formula.toml       # Formula를 Proto로 컴파일
bd mol pour release --var version=1.2.0   # 실제 작업 인스턴스화
bd ready --mol <mol-id>            # 지금 실행할 수 있는 단계
```

CLI에서 사용하는 화학 비유에 따른 세 단계는 다음과 같습니다.

| 단계 | 정의 | 수명 주기 |
|-------|------------|-----------|
| **Proto**(고체) | `{{variables}}`가 있고 `template` 레이블을 지닌 템플릿 epic | 재사용할 수 있으며 실제 작업은 아님 |
| **Molecule**(액체) | Proto에서 부어 만든 영구 Beads(`bd mol pour`) | 다른 Bead처럼 동기화됨 |
| **Wisp**(기체) | 임시 인스턴스(`bd mol wisp`) | 기본적으로 federation push에서 제외되며 `bd purge`로 삭제됨 |

## 이 섹션의 문서

- [Molecule](/workflows/molecules) — 인스턴스화된 작업 그래프의 생성, 검사, 결합,
  squash 방법을 설명합니다.
- [Formula](/workflows/formulas) — 단계, `needs` 의존성, 변수, 조합 규칙을 정의하는
  TOML/JSON 소스 형식입니다.
- [Gate](/workflows/gates) — 외부 조건이 충족될 때까지 단계를 대기시키는 비동기 조건
  (사람, 타이머, GitHub run/PR, rig 간 Bead)입니다.
- [Wisp](/workflows/wisps) — 기록을 복잡하게 만들 필요가 없는 일시적 운영 작업용
  임시 Molecule입니다.
- [TODO 명령](/workflows/todo) — TODO 항목을 작업 Bead로 관리하는 가벼운 인터페이스
  `bd todo`입니다.
