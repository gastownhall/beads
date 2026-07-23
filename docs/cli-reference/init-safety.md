---
title: "bd init-safety"
description: "bd init 플래그 안전 계약입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc init-safety`에서 생성되었습니다.

bd init 플래그 안전 계약입니다.

모든 bd init 호출은 명시적으로 지정된 소스 하나(로컬 재초기화, 원격 채택 또는 새로
발급)에서 project_id를 해석합니다. 소스가 모호하면 bd init이 거부합니다.

플래그 개요

  bd init                       새 신원을 발급합니다. origin에 refs/dolt/data가
                                있으면 여기에서 bootstrap합니다.

  bd init --reinit-local        기존 로컬 데이터 위에 로컬 .beads/를 다시 초기화합니다.
                                원격 이력 폐기를 승인하지 않습니다. origin에 Dolt 데이터가
                                있으면 거부합니다. 재정의하려면 --discard-remote와 함께 사용하세요.

  bd init --reinit-local \      원격의 Dolt 이력을 폐기하고 로컬 재초기화로 교체합니다.
      --discard-remote          이후 첫 bd dolt push는 이력을 교체하는 force-push가 됩니다.

  bd init --force               --reinit-local의 사용 중단된 별칭입니다.
                                2개 이상의 릴리스 동안 계속 작동합니다.

  bd init --from-jsonl          구성된 import.path에서 가져옵니다. origin에 Dolt 데이터가
                                있으면 --discard-remote가 해당 원격 이력 교체를 승인하지 않는 한 거부합니다.

원격 채택

  원격의 기존 이력을 사용하려면 다음을 사용하세요:

      bd bootstrap

  원격이 감지되면 bd init이 이를 자동으로 제안합니다.

DESTROY-TOKEN(비대화형 전용)

  TTY 없이 실행할 때(CI, 에이전트, 파이프 입력) --discard-remote에는 명시적인
  --destroy-token 값이 필요합니다. 토큰 형식은 다음과 같습니다:

      DESTROY-&lt;issue-prefix&gt;

  예를 들어 이슈 접두사가 "bd"이면 토큰은 "DESTROY-bd"입니다:

      bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd

  대화형(TTY) 모드에서는 대신 입력 프롬프트로 확인합니다. 토큰은 bd 런타임 오류
  메시지에 표시되지 않습니다. 패턴 일치 한 줄 명령을 막기 위한 의도적인 보호 장치입니다
  (docs/adr/0002-init-safety-invariants.md 참조).

종료 코드

  10    거부: 원격에 Dolt 이력이 있고 --discard-remote 없이 로컬 이력을 선택함
  11    거부: 기존 로컬 데이터가 있고 삭제 확인을 거부함
  12    거부: 유효한 --destroy-token 없이 --discard-remote를 전달함
        (비대화형 모드)

복구

  거부된 경우 각 종료 코드별 단계별 복구 플레이북은 docs/RECOVERY.md를 참조하세요.


```
bd init-safety [flags]
```
