---
title: Formula
description: 단계, 변수, 의존성, Gate, aspect가 있는 선언형 TOML 또는 JSON 워크플로 템플릿을 작성하고 Proto로 조리합니다.
---

Formula는 선언형 워크플로 템플릿입니다.

## Formula 형식

Formula는 TOML(권장) 또는 JSON으로 작성할 수 있습니다.

### TOML 형식

```toml
formula = "feature-workflow"
description = "표준 기능 개발 워크플로"
version = 1
type = "workflow"

[vars.feature_name]
description = "기능 이름"
required = true

[[steps]]
id = "design"
title = "{{feature_name}} 설계"
type = "human"
description = "설계 문서 생성"

[[steps]]
id = "implement"
title = "{{feature_name}} 구현"
needs = ["design"]

[[steps]]
id = "review"
title = "코드 검토"
needs = ["implement"]
type = "human"

[[steps]]
id = "merge"
title = "main에 merge"
needs = ["review"]
```

### JSON 형식

```json
{
  "formula": "feature-workflow",
  "description": "표준 기능 개발 워크플로",
  "version": 1,
  "type": "workflow",
  "vars": {
    "feature_name": {
      "description": "기능 이름",
      "required": true
    }
  },
  "steps": [
    {
      "id": "design",
      "title": "{{feature_name}} 설계",
      "type": "human"
    },
    {
      "id": "implement",
      "title": "{{feature_name}} 구현",
      "needs": ["design"]
    }
  ]
}
```

## Formula 유형

| 유형 | 설명 |
|------|-------------|
| `workflow` | 표준 단계 순서 |
| `expansion` | expansion operator용 템플릿 |
| `aspect` | 횡단 관심사 |

## 변수

기본값과 제약 조건이 있는 변수를 정의합니다.

```toml
[vars.version]
description = "릴리스 버전"
required = true
pattern = "^\\d+\\.\\d+\\.\\d+$"

[vars.environment]
description = "대상 환경"
default = "staging"
enum = ["staging", "production"]
```

단계에서 변수를 사용합니다.

```toml
[[steps]]
title = "{{version}}을 {{environment}}에 배포"
```

## 단계 유형

단계의 `type`은 생성할 Bead의 이슈 유형을 설정합니다. `task`(기본값), `bug`,
`feature`, `epic`, `chore`를 사용할 수 있으며 다른 값은 `task`로 대체됩니다. 사람의
승인과 비동기 대기는 단계 유형이 아니라 `[steps.gate]` block으로 표현합니다.
[Gate](/workflows/gates)를 참고하세요.

## 의존성

### 순차 실행

```toml
[[steps]]
id = "step1"
title = "첫 단계"

[[steps]]
id = "step2"
title = "두 번째 단계"
needs = ["step1"]
```

### 병렬 실행 후 합류

```toml
[[steps]]
id = "test-unit"
title = "단위 테스트"

[[steps]]
id = "test-integration"
title = "통합 테스트"

[[steps]]
id = "deploy"
title = "배포"
needs = ["test-unit", "test-integration"]  # 둘 다 대기
```

## Gate 워크플로

비동기 조정을 위한 Gate를 추가합니다.

```toml
[[steps]]
id = "approval"
title = "관리자 승인"
type = "human"

[steps.gate]
type = "human"
approvers = ["manager"]

[[steps]]
id = "deploy"
title = "production에 배포"
needs = ["approval"]
```

## Aspect(횡단 관심사)

일치하는 단계에 변환을 적용합니다.

```toml
formula = "security-scan"
type = "aspect"

[[advice]]
target = "*.deploy"  # 모든 deploy 단계와 일치

[advice.before]
id = "security-scan-{step.id}"
title = "{step.title} 전 보안 scan"
```

## Formula 위치

Formula는 다음 순서로 검색합니다.

1. `.beads/formulas/`(프로젝트 수준)
2. `~/.beads/formulas/`(사용자 수준)

`bd formula list`는 검색 경로에서 보이는 모든 Formula를 표시합니다.

## Formula 사용

```bash
# 사용 가능한 Formula 나열
bd formula list

# Formula를 Proto로 조리한 뒤 Molecule로 pour
bd cook <formula-file>
bd mol pour <proto-id> --var key=value

# 생성될 내용 미리 보기
bd mol pour <proto-id> --dry-run
```

## 커스텀 Formula 생성

1. 파일 생성: `.beads/formulas/my-workflow.formula.toml`
2. 구조 정의(위 예제 참고)
3. 사용: `bd cook my-workflow` 실행 후 `bd mol pour <proto-id>` 실행

## 예제: release Formula

```toml
formula = "release"
description = "표준 릴리스 워크플로"
version = 1

[vars.version]
required = true
pattern = "^\\d+\\.\\d+\\.\\d+$"

[[steps]]
id = "bump-version"
title = "버전을 {{version}}으로 올리기"

[[steps]]
id = "changelog"
title = "CHANGELOG 업데이트"
needs = ["bump-version"]

[[steps]]
id = "test"
title = "전체 테스트 suite 실행"
needs = ["changelog"]

[[steps]]
id = "build"
title = "릴리스 artifact build"
needs = ["test"]

[[steps]]
id = "tag"
title = "Git tag v{{version}} 생성"
needs = ["build"]

[[steps]]
id = "publish"
title = "릴리스 게시"
needs = ["tag"]
type = "human"
```
