---
title: "bd formula"
description: "molecule 템플릿의 소스 계층인 워크플로 formula를 관리합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc formula`에서 생성되었습니다.

molecule 템플릿의 소스 계층인 워크플로 formula를 관리합니다.

formula는 구성 규칙으로 워크플로를 정의하는 TOML/JSON 파일입니다. formula를 정의하고
proto로 cook한 뒤 작업으로 pour하거나 wisp로 만드세요.

검색 경로(순서대로):
  1. &lt;resolved-beads-dir&gt;/formulas/(활성 프로젝트)
  2. &lt;checkout-root&gt;/.beads/formulas/(저장소 로컬 formula)
  3. ~/.beads/formulas/(사용자)
  4. $GT_ROOT/.beads/formulas/(공유 워크스페이스 루트, GT_ROOT가 설정된 경우)

명령:
  list   모든 검색 경로에서 사용 가능한 formula 나열
  show   formula 세부 정보, 단계, 구성 규칙 표시

```
bd formula [flags]
```

## bd formula convert

formula 파일을 JSON에서 TOML 형식으로 변환합니다.

TOML 형식은 더 나은 사용성을 제공합니다:
  - \n 이스케이프가 필요 없는 여러 줄 문자열
  - 사람이 읽을 수 있는 diff
  - 댓글 허용

convert 명령은 .formula.json 파일을 읽고 .formula.toml을 출력합니다.
원본 JSON 파일은 보존됩니다(제거하려면 --delete 사용).

예시:
  bd formula convert shiny              # shiny.formula.json을 .toml로 변환
  bd formula convert ./my.formula.json  # 특정 파일 변환
  bd formula convert --all              # 모든 JSON formula 변환
  bd formula convert shiny --delete     # 변환 후 JSON 파일 제거
  bd formula convert shiny --stdout     # TOML을 stdout에 출력

```
bd formula convert <formula-name|path> [--all] [flags]
```

**플래그:**

```
      --all      모든 JSON formula 변환
      --delete   변환 후 JSON 파일 삭제
      --stdout   파일 대신 stdout에 TOML 출력
```

## bd formula list

검색 경로의 모든 formula를 나열합니다.

검색 경로(우선순위순):
  1. &lt;resolved-beads-dir&gt;/formulas/(활성 프로젝트 - 가장 높은 우선순위)
  2. &lt;checkout-root&gt;/.beads/formulas/(저장소 로컬 formula)
  3. ~/.beads/formulas/(사용자)
  4. $GT_ROOT/.beads/formulas/(공유 워크스페이스 루트, GT_ROOT가 설정된 경우)

앞선 경로의 formula가 뒤 경로의 같은 이름 formula를 가립니다.

예시:
  bd formula list
  bd formula list --json
  bd formula list --type workflow
  bd formula list --type convoy

```
bd formula list [flags]
```

**플래그:**

```
      --type string   유형으로 필터링(workflow, expansion, aspect, convoy)
```

## bd formula show

formula의 상세 정보를 표시합니다.

표시 항목:
  - formula 메타데이터(name, type, description)
  - 기본값과 제약 조건이 있는 변수
  - 의존성이 있는 단계
  - 구성 규칙(extends, aspects, expansions)
  - 외부 구성용 bond 지점

예시:
  bd formula show shiny
  bd formula show rule-of-five
  bd formula show security-audit --json

```
bd formula show <formula-name> [flags]
```
