---
title: "bd cook"
description: "Cook은 .formula.json 파일을 proto로 변환합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc cook`에서 생성되었습니다.

Cook은 .formula.json 파일을 proto로 변환합니다.

기본적으로 cook은 임시 사용을 위해 해석된 formula를 JSON으로 stdout에 출력합니다.
출력을 검사하거나 파이프로 전달하거나 파일에 저장할 수 있습니다.

두 가지 cook 모드를 사용할 수 있습니다:

  컴파일 시간(기본값, --mode=compile):
    &#123;&#123;variable&#125;&#125; 자리표시자를 그대로 유지한 proto를 생성합니다.
    용도: 모델링, 추정, 외주 인계, 계획.
    변수를 치환하지 않으며 출력에 템플릿 구조가 표시됩니다.

  런타임(--mode=runtime 또는 --var 플래그 제공 시):
    변수가 치환된 완전히 해석된 proto를 생성합니다.
    용도: pour 전 최종 검증, 정확한 출력 확인.
    모든 변수에 값이 필요합니다(--var 또는 기본값 사용).

formula는 다음을 지원하는 상위 수준 워크플로 템플릿입니다:
  - 기본값과 검증이 있는 변수 정의
  - 이슈 계층이 되는 단계 정의
  - formula를 결합하기 위한 구성 규칙
  - extends를 통한 상속

--persist 플래그는 proto를 데이터베이스에 쓰는 레거시 동작을 활성화합니다.
다시 cook하지 않고 같은 proto를 여러 번 재사용하려는 경우 유용합니다.

대부분의 워크플로에서는 임시 proto를 선호합니다. pour와 wisp 명령은 formula 이름을
직접 받고 인라인으로 cook합니다.

예시:
  bd cook mol-feature.formula.json                    # 컴파일 시간: &#123;&#123;vars&#125;&#125; 유지
  bd cook mol-feature --var name=auth                 # 런타임: 변수 치환
  bd cook mol-feature --mode=runtime --var name=auth  # 명시적 런타임 모드
  bd cook mol-feature --dry-run                       # 단계 미리 보기
  bd cook mol-release.formula.json --persist          # 데이터베이스에 쓰기
  bd cook mol-release.formula.json --persist --force  # 기존 항목 교체

출력(기본값):
  모든 단계를 포함한 해석된 formula의 JSON 표현.

출력(--persist):
  데이터베이스에 다음을 갖는 proto bead를 생성합니다:
  - formula 이름과 일치하는 ID(예: mol-feature)
  - proto 식별용 "template" 레이블
  - 각 단계의 하위 이슈
  - depends_on 관계와 일치하는 의존성

```
bd cook <formula-file> [flags]
```

**플래그:**

```
      --dry-run               생성될 항목 미리 보기
      --force                 기존 proto가 있으면 교체(--persist 필요)
      --mode string           cook 모드: compile(자리표시자 유지) 또는 runtime(변수 치환)
      --persist               proto를 데이터베이스에 영구 저장(레거시 동작)
      --prefix string         proto ID 앞에 붙일 접두사(예: 'gt-'는 'gt-mol-feature' 생성)
      --search-path strings   formula 상속을 검색할 추가 경로
      --var stringArray       변수 치환(key=value), 런타임 모드 활성화
```
