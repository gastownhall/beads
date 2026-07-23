---
title: 적응형 ID 길이
description: 충돌을 피하면서 짧게 유지되도록 데이터베이스 크기에 따라 해시 ID 길이가 확장되는 방식
---

Beads는 데이터베이스 크기에 따라 자동으로 확장되는 적응형 해시 ID 길이를 사용합니다. 작은 데이터베이스에서는 가독성을 최적화하고 데이터베이스가 커질 때는 충돌을 방지합니다.

## 도입 이유

- **작은 데이터베이스**(이슈 0~500개): `bd-a3f2` 같은 매우 짧고 읽기 쉬운 ID(4자)
- **중간 데이터베이스**(이슈 500~1500개): `bd-7f3a8` 같은 조금 더 긴 ID(5자)
- **큰 데이터베이스**(이슈 1500개 이상): `bd-7f3a86` 같은 표준 ID(6자)

오래된 이슈를 적극적으로 보관 처리하면 시간이 지나도 ID를 더 짧게 유지할 수 있습니다.

## 작동 방식

### 생일 역설 계산

충돌 확률은 다음 식으로 계산합니다.

```
P(collision) ≈ 1 - e^(-n²/2N)
```

각 항목의 의미는 다음과 같습니다.
- `n` = 데이터베이스의 이슈 수
- `N` = 가능한 전체 ID 수(소문자 영숫자의 경우 36^length)

### 기본 임곗값(최대 충돌 확률 25%)

| 데이터베이스 크기 | ID 길이 | 충돌 확률 |
|--------------|-----------|----------------------|
| 0~500        | 4자       | 500개일 때 약 7%     |
| 501~1500     | 5자       | 1500개일 때 약 2%    |
| 1501 이상    | 6자       | 계속 확장             |

### 충돌 해결

드물게 충돌이 발생하면 알고리즘이 다음을 자동으로 시도합니다.
1. 기본 길이(예: 4자)
2. 기본 길이 + 1(예: 5자)
3. 기본 길이 + 2(예: 6자)

길이마다 nonce 10개를 사용해 총 30번 시도합니다.

## 구성

`id_mode=hash`를 사용하면 적응형 ID 길이가 자동으로 활성화됩니다. 동작은 다음과 같이 사용자 지정할 수 있습니다.

### 최대 충돌 확률

기본값: 25%(0.25)

```bash
# 더 관대하게 설정(최대 50% 충돌 확률 허용)
bd config set max_collision_prob "0.50"

# 더 엄격하게 설정(충돌 확률 1%만 허용)
bd config set max_collision_prob "0.01"
```

### 최소 해시 길이

기본값: 4자

```bash
# 최소 5자 ID로 시작
bd config set min_hash_length "5"

# 매우 짧은 ID(주의해서 사용)
bd config set min_hash_length "3"
```

### 최대 해시 길이

기본값: 8자

```bash
# 매우 큰 데이터베이스에 더 긴 ID 허용
bd config set max_hash_length "10"
```

## 예시

### 기본 구성

```bash
# 해시 ID로 초기화
bd init --id-mode hash --prefix myproject

# 처음 500개 이슈에는 4자 ID 사용
bd create "버그 수정" -p 1
# → myproject-a3f2

# 이슈 1000개 이후 5자 ID로 전환
bd create "기능 추가" -p 1
# → myproject-7f3a8c

# 이슈 10,000개에서는 6자 ID 사용
bd create "리팩터링" -p 1
# → myproject-b9d1e4
```

### 사용자 지정 구성

```bash
# 매우 엄격한 충돌 허용치
bd config set max_collision_prob "0.01"

# 임곗값 1%, 이슈 100개일 때 4자 ID 사용
# (4자일 때 충돌 확률은 약 0.3%)

# 일관성을 위해 최소 5자 ID 강제
bd config set min_hash_length "5"

# 이제 모든 ID가 최소 5자
bd create "작업" -p 1
# → myproject-7f3a8
```

## 충돌 확률 표

`scripts/collision-calculator.go`로 충돌 확률을 살펴보세요.

```bash
go run scripts/collision-calculator.go
```

출력에는 다음이 표시됩니다.
- 다양한 데이터베이스 크기와 ID 길이의 충돌 확률
- 다양한 임곗값에 권장되는 ID 길이
- 예상 충돌 횟수
- 적응형 확장 전략

## 구현 세부 정보

### 위치

- 알고리즘: `internal/storage/dolt/adaptive_length.go`
- ID 생성: `internal/storage/dolt/dolt.go` (`generateHashID`)
- 테스트: `internal/storage/dolt/adaptive_length_test.go`
- E2E 테스트: `internal/storage/dolt/adaptive_e2e_test.go`

### 데이터베이스 스키마

구성은 `config` 테이블에 저장됩니다.

```sql
INSERT INTO config (key, value) VALUES ('max_collision_prob', '0.25');
INSERT INTO config (key, value) VALUES ('min_hash_length', '4');
INSERT INTO config (key, value) VALUES ('max_hash_length', '8');
```

### 성능

- 충돌 확률 계산: 호출당 약 10ns
- 적응형 길이를 사용한 ID 생성: 약 300ns(이전과 동일)
- 이슈 수를 세는 데이터베이스 쿼리: 약 100μs

## 마이그레이션

### 기존 데이터베이스

6자 ID를 사용하는 기존 데이터베이스는 다음과 같이 동작합니다.
1. 기본적으로 6자 ID를 계속 사용합니다.
2. 구성을 설정해 적응형 모드를 선택할 수 있습니다(새 ID는 적응형 길이 사용).
3. 이전 ID는 변경되지 않습니다.

### 순차 ID에서 해시 ID로 마이그레이션

`bd migrate --to-hash-ids`로 순차 ID에서 해시 ID로 마이그레이션할 때는 다음과 같이 동작합니다.
- 새 ID에 적응형 길이 알고리즘을 사용합니다.
- 기존 순차 ID를 보존합니다.
- 참조가 자동으로 업데이트됩니다.

## 모범 사례

1. **기본값 권장**: 임곗값 25%는 대부분의 사용 사례에서 잘 작동합니다.
2. **적극적인 보관**: 닫힌 이슈를 삭제해 데이터베이스를 작게, ID를 짧게 유지합니다.
3. **일관성**: 모든 ID를 같은 길이로 만들려면 `min_hash_length`를 설정합니다.
4. **모니터링**: 충돌 계산기를 주기적으로 실행해 상태를 확인합니다.

## 향후 개선 사항

가능한 개선 사항(아직 구현되지 않음):

- **자동 확장 알림**: 임곗값에 가까워지면 경고합니다.
- **워크스페이스별 임곗값**: 프로젝트마다 다른 구성을 사용합니다.
- **동적 조정**: 관찰된 충돌률에 따라 임곗값을 자동 조정합니다.
- **압축 인식**: 충돌 계산에서 압축된 이슈를 세지 않습니다.

## 대안: 순차 카운터 ID

적응형 해시 ID가 기본값이지만, 사람이 읽기 쉬운 번호 체계를 선호하는 프로젝트를 위해
beads는 순차 정수 ID(`bd-1`, `bd-2`, ...)도 지원합니다.

카운터 모드는 `issue_id_mode` 구성 키로 제어합니다.

```bash
# 순차 ID로 전환
bd config set issue_id_mode counter

# 해시 ID로 복귀(기본값)
bd config set issue_id_mode hash
```

**절충점:**

- **해시 ID**(이 문서): 병렬 브랜치와 에이전트 간 충돌이 없습니다. ID의 예측 가능성은 낮지만 항상 고유합니다.
- **카운터 ID**: 사람이 이해하기 쉽고 순차적입니다. 카운터가 갈라질 수 있는 다중 브랜치 워크플로에서는 주의해야 합니다.

마이그레이션 안내와 접두사별 카운터 격리를 포함한 `issue_id_mode=counter` 전체 문서는
[구성](/reference/configuration)을 참조하세요.

## 관련 자료

- [마이그레이션 가이드](https://github.com/gastownhall/beads/blob/main/README.md#migration) - 순차 ID를 해시 ID로 변환하기
- [구성](/reference/configuration) - 모든 구성 옵션
