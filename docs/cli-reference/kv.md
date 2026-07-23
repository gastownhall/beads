---
title: "bd kv"
description: "beads 키-값 저장소 작업 명령입니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc kv`에서 생성되었습니다.

beads 키-값 저장소 작업 명령입니다.

키-값 저장소는 세션 간 유지되는 플래그, 환경 변수 또는 기타 사용자 정의 데이터를
저장하는 데 유용합니다.

예시:
  bd kv set mykey myvalue    # 값 설정
  bd kv get mykey            # 값 가져오기
  bd kv clear mykey          # 키 삭제
  bd kv list                 # 모든 키-값 쌍 나열

```
bd kv [flags]
```

## bd kv clear

beads 키-값 저장소에서 키를 삭제합니다.

예시:
  bd kv clear feature_flag
  bd kv clear api_endpoint

```
bd kv clear <key> [flags]
```

## bd kv get

beads 키-값 저장소에서 값을 가져옵니다.

예시:
  bd kv get feature_flag
  bd kv get api_endpoint

```
bd kv get <key> [flags]
```

## bd kv list

beads 키-값 저장소의 모든 키-값 쌍을 나열합니다.

예시:
  bd kv list
  bd kv list --json

```
bd kv list [flags]
```

## bd kv set

beads 키-값 저장소에 키-값 쌍을 설정합니다.

세션 간 유지되는 플래그, 환경 변수 또는 기타 사용자 정의 데이터를 저장하는 데 유용합니다.

예시:
  bd kv set feature_flag true
  bd kv set api_endpoint https://api.example.com
  bd kv set max_retries 3

```
bd kv set <key> <value> [flags]
```
