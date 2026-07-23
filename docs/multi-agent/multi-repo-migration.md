---
title: 다중 저장소 마이그레이션 가이드
description: 별도 계획 저장소를 사용하는 OSS contributor, 팀, 다중 단계, 다중 persona 워크플로에 다중 저장소 routing 도입
---

이 가이드는 OSS 기여, 팀 협업, 다중 단계 개발에 Beads의 다중 저장소 워크플로를
도입하도록 안내합니다.

## 빠른 시작

**Beads가 이미 설치되어 있나요?** 해당 상황으로 이동하세요.

- [OSS contributor](#oss-contributor-workflow) - 계획을 upstream PR에서 분리
- [팀원](#team-workflow) - 브랜치에서 계획 공유
- [다중 단계 개발](#multi-phase-development) - 단계별 저장소 분리
- [여러 persona](#multiple-personas) - architect와 implementer 분리

**Beads가 처음인가요?** 먼저 [빠른 시작](/getting-started/quickstart)을 참고하세요.

## 다중 저장소 모드란?

기본적으로 Beads는 현재 저장소의 `.beads/` 아래 Dolt 데이터베이스에 이슈를
저장합니다. 기본 embedded 모드에서는 `.beads/embeddeddolt/`입니다. 다중 저장소
모드는 다음 기능을 제공합니다.

- 역할(maintainer 또는 contributor)에 따라 **이슈를 다른 저장소로 routing**
- **여러 저장소의 이슈를 집계**하여 통합 보기 제공
- contributor 계획을 upstream 프로젝트와 **분리**
- 모든 저장소에서 Dolt 버전 관리로 **데이터 무결성 유지**

## 다중 저장소가 필요한 경우

### 다중 저장소가 필요하지 않은 경우:

- ✅ 자신의 프로젝트에서 혼자 작업
- ✅ 공유 저장소와 신뢰 모델을 사용하는 팀
- ✅ 모든 이슈가 프로젝트 Git 기록에 포함되어야 함

### 다중 저장소가 필요한 경우:

- 🔴 OSS 기여 - 계획으로 upstream을 오염시키지 않아야 함
- 🔴 fork 워크플로 - 계획이 PR에 표시되면 안 됨
- 🔴 여러 작업 단계 - 설계와 구현 저장소 분리
- 🔴 여러 persona - architect 계획과 implementer 작업 분리

## 핵심 개념

### 1. 소스 저장소(`source_repo`)

모든 이슈에는 소유 저장소를 나타내는 `source_repo` 필드가 있습니다.

```jsonl
{"id":"bd-abc","source_repo":".","title":"핵심 이슈"}
{"id":"bd-xyz","source_repo":"~/.beads-planning","title":"계획 이슈"}
```

- `.` = 현재 저장소(기본값)
- `~/.beads-planning` = contributor 계획 저장소
- `/path/to/repo` = 다른 저장소의 절대 경로

### 2. 자동 routing

Beads는 역할에 따라 새 이슈를 올바른 저장소로 자동 routing합니다.

```bash
# maintainer(SSH push 권한 보유)
bd create "버그 수정" -p 1
# 현재 저장소에 생성(source_repo = ".")

# contributor(HTTPS 또는 push 권한 없음)
bd create "버그 수정" -p 1
# ~/.beads-planning에 생성(source_repo = "~/.beads-planning")
```

### 3. 다중 저장소 hydration

Beads는 여러 저장소의 이슈를 통합 데이터베이스로 집계할 수 있습니다.

```bash
bd list --json
# 다음 위치의 이슈 표시:
# - 현재 저장소(.)
# - 계획 저장소(~/.beads-planning)
# - 설정된 모든 추가 저장소
```

<a id="oss-contributor-workflow"></a>

## OSS contributor 워크플로

**문제:** OSS 프로젝트에 기여하지만 실험적 계획이 PR에 표시되는 것을 원하지 않습니다.

**해결 방법:** upstream에 commit하지 않는 별도 계획 저장소를 사용합니다.

### 설정(최초 한 번)

```bash
# 1. upstream 프로젝트 fork 및 clone
git clone https://github.com/you/project.git
cd project

# 2. 아직 하지 않았다면 Beads 초기화
bd init

# 3. contributor 설정 wizard 실행
bd init --contributor

# wizard의 작업:
# - fork 여부 감지('upstream' 원격 확인)
# - 계획 저장소 생성 요청(기본값 ~/.beads-planning)
# - 자동 routing 설정(contributor에서 계획 저장소)
# - 다중 저장소 hydration 설정
```

### 수동 설정

수동 설정을 원한다면 다음을 실행하세요.

```bash
# 1. 계획 저장소 생성
mkdir -p ~/.beads-planning
cd ~/.beads-planning
git init
bd init --prefix plan

# 2. fork에서 routing 설정
cd ~/projects/project
bd config set routing.mode auto
bd config set routing.contributor "~/.beads-planning"

# 3. hydration 소스에 계획 저장소 추가
bd config set repos.additional "~/.beads-planning"
```

### 일일 워크플로

```bash
# fork에서 작업
cd ~/projects/project

# 계획 이슈 생성(~/.beads-planning으로 자동 routing)
bd create "인증 구현 조사" -p 1
bd create "새 기능 RFC 초안" -p 2

# 모든 이슈 보기(현재 저장소와 계획 저장소)
bd ready
bd list --json

# 이슈 작업
bd update plan-42 --claim

# 작업 완료
bd close plan-42 --reason "완료"

# PR 생성 - 계획 이슈는 표시되지 않음
git add .
git commit -m "Fix authentication bug"
git push origin my-feature-branch
# ✅ PR에는 코드 변경만 포함되고 .beads/ 오염 없음
```

### upstream에 이슈 제안

계획 이슈를 upstream과 공유하려면 다음 방법을 사용하세요.

```bash
# 옵션 1: 이슈를 upstream 저장소로 수동 복사
bd show plan-42 --json > /tmp/issue.json
# (maintainer에게 보내거나 GitHub 이슈 생성)

# 옵션 2: 이슈 마이그레이션(향후 기능, bd-mlcz 참고)
bd migrate plan-42 --to . --dry-run
bd migrate plan-42 --to .
```

<a id="team-workflow"></a>

## 팀 워크플로

**문제:** 팀원이 브랜치가 있는 공유 저장소에서 작업하지만 계획의 상세 수준이 서로 다릅니다.

**해결 방법:** 선택적 개인 계획 저장소와 브랜치 기반 워크플로를 사용합니다.

### 설정(팀 리드)

```bash
# 1. 기본 저장소에서 Beads 초기화
cd ~/projects/team-project
bd init --prefix team

# 2. 팀 설정 wizard 실행
bd init --team

# wizard의 작업:
# - 공유 저장소 감지(SSH push 권한)
# - 자동 routing 설정(maintainer는 현재 저장소 사용)
# - 보호 브랜치 워크플로 설정(GitHub/GitLab 사용 시)
# - 예제 워크플로 생성
```

### 설정(팀원)

```bash
# 1. 팀 저장소 clone
git clone git@github.com:team/project.git
cd project

# 2. Beads가 SSH 권한으로 maintainer 역할 자동 감지
bd create "기능 X 구현" -p 1
# 현재 저장소에 생성(team-123)

# 3. 선택 사항: 실험용 개인 계획 저장소 생성
mkdir -p ~/.beads-planning-personal
cd ~/.beads-planning-personal
git init
bd init --prefix exp

# 4. 팀 프로젝트에서 다중 저장소 설정
cd ~/projects/project
bd config set repos.additional "~/.beads-planning-personal"
```

### 일일 워크플로

```bash
# 공유 팀 계획(저장소에 commit)
bd create "인증 구현" -p 1 --repo .
# team-42(팀 전체에 표시)

# 개인 실험(팀 저장소에 commit하지 않음)
bd create "대안 접근법 시도" -p 2 --repo ~/.beads-planning-personal
# exp-99(개인 계획)

# 모든 작업 보기
bd ready
bd list --json

# 팀 작업 완료 및 동기화
bd dolt push
```

<a id="multi-phase-development"></a>

## 다중 단계 개발

**문제:** 프로젝트의 계획, 구현, 유지보수 단계마다 별도 이슈 공간이 필요합니다.

**해결 방법:** 단계마다 별도 저장소를 사용합니다.

### 설정

```bash
# 1. 단계별 저장소 생성
mkdir -p ~/projects/myapp-planning
mkdir -p ~/projects/myapp-implementation
mkdir -p ~/projects/myapp-maintenance

# 2. 각 단계 초기화
cd ~/projects/myapp-planning
git init
bd init --prefix plan

cd ~/projects/myapp-implementation  
git init
bd init --prefix impl

cd ~/projects/myapp-maintenance
git init
bd init --prefix maint

# 3. 기본 작업 공간에서 집계 설정
cd ~/projects/myapp-implementation
bd config set repos.additional "~/projects/myapp-planning,~/projects/myapp-maintenance"
```

### 워크플로

```bash
# 1단계: 계획
cd ~/projects/myapp-planning
bd create "인증 시스템 설계" -p 1 -t epic
bd create "OAuth 제공자 조사" -p 1

# 2단계: 구현(계획 및 구현 이슈 확인)
cd ~/projects/myapp-implementation
bd ready  # 두 저장소의 이슈 표시
bd create "인증 backend 구현" -p 1
bd dep add impl-42 plan-10 --type blocks  # 저장소 간 연결

# 3단계: 유지보수
cd ~/projects/myapp-maintenance
bd create "인증 보안 patch" -p 0 -t bug
```

<a id="multiple-personas"></a>

## 여러 persona

**문제:** architect(상위 수준 계획)와 implementer(세부 작업) 역할을 모두 수행합니다.

**해결 방법:** persona별 작업 저장소를 분리합니다.

### 설정

```bash
# 1. persona 저장소 생성
mkdir -p ~/architect-planning
mkdir -p ~/implementer-tasks

cd ~/architect-planning
git init
bd init --prefix arch

cd ~/implementer-tasks
git init  
bd init --prefix impl

# 2. 집계 설정
cd ~/implementer-tasks
bd config set repos.additional "~/architect-planning"
```

### 워크플로

```bash
# architect 모드
cd ~/architect-planning
bd create "기능 X 시스템 아키텍처" -p 1 -t epic
bd create "데이터베이스 schema 설계" -p 1

# implementer 모드(architect와 구현 작업 모두 표시)
cd ~/implementer-tasks
bd ready
bd create "사용자 table 구현" -p 1
bd dep add impl-10 arch-42 --type blocks

# 구현 완료
bd close impl-10 --reason "완료"
```

## 설정 레퍼런스

### Routing 설정

```bash
# 역할을 자동 감지하고 그에 따라 routing
bd config set routing.mode auto

# 항상 기본 저장소 사용(역할 감지 무시)
bd config set routing.mode explicit  
bd config set routing.default "."

# 역할별 저장소 설정
bd config set routing.maintainer "."
bd config set routing.contributor "~/.beads-planning"
```

### 다중 저장소 hydration

```bash
# 집계할 추가 저장소 등록
bd config set repos.additional "~/repo1,~/repo2,~/repo3"

# 기본 저장소 설정(선택 사항)
bd config set repos.primary "."
```

### 자동 routing 재정의

```bash
# 이슈를 특정 저장소에 강제로 생성(자동 routing 무시)
bd create "이슈" -p 1 --repo /path/to/repo
```

## 문제 해결

### 이슈가 잘못된 저장소에 표시됨

**문제:** `bd create`가 예상하지 않은 저장소로 이슈를 routing합니다.

**해결 방법:**
```bash
# 현재 routing 설정 확인
bd config get routing.mode
bd config get routing.maintainer
bd config get routing.contributor

# 감지된 역할 확인
bd config get beads.role

# 명시적 flag로 재정의
bd create "이슈" -p 1 --repo .
```

### 다른 저장소의 이슈가 보이지 않음

**문제:** `bd list`에 현재 저장소의 이슈만 표시됩니다.

**해결 방법:**
```bash
# 다중 저장소 설정 확인
bd config get repos.additional

# 누락된 저장소 추가
bd config set repos.additional "~/repo1,~/repo2"

# hydration 확인
bd dolt push
bd list --json
```

### Merge 충돌

**문제:** 여러 저장소의 변경 사항이 충돌합니다.

**해결 방법:** Dolt는 cell 단위 merge로 merge 충돌을 직접 처리합니다. 자세한 내용은
[문제 해결](/reference/troubleshooting#merge-conflicts)을 참고하세요.

### 발견한 이슈가 잘못된 저장소에 생성됨

**문제:** `discovered-from` 의존성으로 생성한 이슈가 잘못된 저장소에 표시됩니다.

**해결 방법:** 발견한 이슈는 상위 이슈의 `source_repo`를 자동으로 상속합니다.
의도된 동작이며, 재정의하려면 다음을 실행하세요.
```bash
bd create "이슈" -p 1 --deps discovered-from:bd-42 --repo /different/repo
```

### 계획 저장소가 PR을 오염시킴

**문제:** `~/.beads-planning` 변경 사항이 upstream PR에 표시됩니다.

**해결 방법:** 올바르게 설정했다면 발생하지 않아야 합니다. 다음을 확인하세요.
```bash
# 계획 저장소가 fork와 분리되었는지 확인
ls -la ~/.beads-planning/.git  # 존재해야 함
ls -la ~/projects/fork/.beads/  # 계획 이슈가 없어야 함

# routing 확인
bd config get routing.contributor  # ~/.beads-planning이어야 함
```

## 이전 버전 호환성

### 단일 저장소에서 마이그레이션

마이그레이션이 필요하지 않습니다. 다중 저장소 모드는 선택 사항입니다.

```bash
# 이전(단일 저장소)
bd create "이슈" -p 1
# 로컬 Dolt 데이터베이스에 생성

# 이후(다중 저장소 설정)
bd create "이슈" -p 1
# 역할에 따라 자동 routing
# 로컬 데이터베이스의 기존 이슈도 계속 작동
```

### 다중 저장소 비활성화

```bash
# routing 설정 제거
bd config unset routing.mode
bd config unset repos.additional

# 모든 이슈를 다시 현재 저장소에 생성
bd create "이슈" -p 1
# 단일 저장소 모드로 복귀
```

## 권장 사항

### OSS 기여자

- ✅ 개인 계획에는 항상 `~/.beads-planning` 또는 유사한 저장소 사용
- ✅ `.beads/` 변경 사항을 upstream PR에 commit하지 않음
- ✅ 명확한 prefix(`plan-`, `exp-`) 사용
- ❌ 같은 저장소에서 계획과 구현을 혼합하지 않음

### 팀

- ✅ 공유 Dolt 데이터베이스 동기화에 `bd dolt push` 사용
- ✅ main/master에 보호 브랜치 워크플로 사용
- ✅ 코드 변경과 마찬가지로 PR에서 이슈 변경 사항 검토
- ❌ `.beads/`를 삭제하지 않음. 모든 이슈 데이터를 잃게 됨

### 다중 단계 프로젝트

- ✅ 명확한 단계 이름(`planning`, `impl`, `maint`) 사용
- ✅ 의존성으로 단계 간 이슈 연결
- ✅ 완료된 단계를 주기적으로 보관
- ❌ 여러 단계에 이슈를 중복 생성하지 않음

## 다음 단계

- **CLI 레퍼런스:** 명령 상세 정보는 [README.md](https://github.com/gastownhall/beads/blob/main/README.md) 참고
- **설정 가이드:** 모든 설정 옵션은 [설정](/reference/configuration) 참고
- **문제 해결:** [문제 해결](/reference/troubleshooting) 참고
- **다중 저장소 내부 구조:** [ROUTING.md의 다중 저장소 hydration](/multi-agent/routing#multi-repo-hydration) 참고

## 관련 이슈

- `bd-8rd` - 마이그레이션 및 onboarding epic
- `bd-mlcz` - `bd migrate` 명령(계획됨)
- `bd-kla1` - `bd init --contributor` wizard(구현됨)
- `bd-twlr` - `bd init --team` wizard(구현됨)
