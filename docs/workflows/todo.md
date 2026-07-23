---
title: TODO 명령
description: add, list, done 단축 명령으로 가벼운 TODO 항목을 일반 task 유형 이슈로 관리하는 bd todo 명령입니다.
---

`bd todo` 명령은 TODO 항목을 task 유형 이슈로 관리하는 가벼운 인터페이스를 제공합니다.

## 철학

`bd`의 TODO는 별도 추적 시스템이 아니라 편리한 단축 명령이 있는 일반 task 유형
이슈입니다. 따라서 다음 특성이 있습니다.

- **병렬 시스템 없음**: TODO는 다른 모든 이슈와 같은 저장소와 동기화를 사용합니다.
- **승격 가능**: 필요할 때 TODO를 bug/feature로 쉽게 변환합니다.
- **전체 기능 지원**: TODO는 모든 `bd` 기능(의존성, 레이블, routing)을 지원합니다.
- **간단한 인터페이스**: 일반적인 TODO 워크플로를 빠른 명령으로 처리합니다.

## 빠른 시작

```bash
# TODO 추가
bd todo add "로그인 버그 수정" -p 1

# TODO 목록 보기
bd todo

# TODO 완료 표시
bd todo done <id>
```

## 명령

### `bd todo`(또는 `bd todo list`)

열린 task 유형 이슈를 모두 나열합니다.

```bash
bd todo                  # 열린 TODO 나열
bd todo list             # 위와 동일
bd todo list --all       # 완료된 TODO도 표시
bd todo list --json      # JSON 출력
```

**출력:**
```
  ○ test-yxg  Fix the login bug                         ● P1  open
  ○ test-ryl  Update documentation                      ● P3  open

Total: 2 TODOs
```

### `bd todo add <title>`

새 TODO 항목(task 유형 이슈)을 생성합니다.

```bash
bd todo add "로그인 버그 수정"                                     # 기본 P2
bd todo add "문서 업데이트" -p 3 -d "예제 추가"                    # 우선순위와 설명 지정
bd todo add "긴급 수정" --priority 0 --description "최대한 빨리"    # P0 작업
```

**플래그:**

- `-p, --priority <0-4>`: 우선순위(기본값: 2)
- `-d, --description <text>`: 설명

### `bd todo done <id> [<id>...]`

하나 이상의 TODO를 완료로 표시합니다.

```bash
bd todo done test-abc              # TODO 하나 종료
bd todo done test-abc test-def     # 여러 TODO 종료
bd todo done test-abc --reason "PR #42에서 수정"  # 사유 지정
```

**플래그:**

- `--reason <text>`: 종료 사유(기본값: `Completed`)

## TODO 변환

TODO는 일반 task 이슈이므로 다른 유형으로 변환할 수 있습니다.

```bash
# TODO를 bug로 승격
bd update test-abc --type bug --priority 0

# 의존성 추가
bd dep add test-abc test-def

# 레이블 추가
bd update test-abc --set-labels "urgent,frontend"
```

## TODO 상세 정보 보기

일반 `bd` 명령을 사용하세요.

```bash
bd show test-abc        # TODO 상세 정보 보기
bd list --type task     # 모든 task 나열(TODO 포함)
bd ready                # 작업 큐에서 준비된 TODO 확인
```

## 예제

### 일일 TODO 워크플로

```bash
# 아침: 작업 추가
bd todo add "PR 검토"
bd todo add "CI 파이프라인 수정" -p 1
bd todo add "changelog 업데이트" -p 3

# 할 일 확인
bd todo

# 작업 완료
bd todo done <id>
bd todo done <id>

# 하루 종료: 남은 작업 확인
bd todo
```

### TODO를 정식 이슈로 변환

```bash
# 간단한 TODO로 시작
bd todo add "로그인 실패"

# 나중에 더 심각한 문제임을 확인
bd update <id> --type bug --priority 0 --description "여러 사용자가 로그인할 수 없다고 보고함"
bd update <id> --acceptance "모든 사용자 유형에서 로그인 성공"

# 이제 정식으로 추적되는 bug
bd show <id>
```

## 자주 묻는 질문

**Q: TODO와 task는 다른가요?**
A: 아니요. TODO는 task 유형 이슈일 뿐입니다. `bd todo` 명령은 일반적인 task 작업의 단축 명령을 제공합니다.

**Q: TODO에 의존성을 추가할 수 있나요?**
A: 예. 다른 이슈와 마찬가지로 `bd dep add <todo-id> <blocks-id>`를 사용하세요.

**Q: TODO는 머신 간에 동기화되나요?**
A: 예. 다른 모든 이슈처럼 Dolt 데이터베이스에 저장되고 Dolt 원격을 통해 동기화됩니다.

**Q: bd ready에서 TODO를 사용할 수 있나요?**
A: 예. `bd ready`는 task 유형 TODO를 포함하여 차단되지 않은 모든 이슈를 보여 줍니다.

**Q: TODO와 일반 task 중 무엇을 사용해야 하나요?**
A: 빠르고 비공식적인 작업에는 `bd todo`를 사용하세요. 더 많은 컨텍스트가 필요하거나 큰 계획의 일부인 작업에는 `bd create -t task`를 사용하세요.

## 설계 근거

TODO 명령은 Beads의 **최소 surface area** 철학을 따릅니다.

1. **새 유형 없음**: TODO는 task 유형 이슈입니다.
2. **특수 저장소 없음**: 다른 모든 항목과 같은 Dolt 데이터베이스를 사용합니다.
3. **편의 계층**: 일반 작업을 위한 단축 명령만 제공합니다.
4. **완전한 호환성**: 모든 `bd` 기능 및 명령과 함께 작동합니다.

따라서 다음을 보장합니다.

- 중복 추적 시스템이 없습니다.
- TODO와 task 사이에 마이그레이션이 필요 없습니다.
- 기존의 모든 `bd` 도구(federation, compaction, routing)와 함께 작동합니다.
- 이해하고 유지보수하기 쉽습니다.
