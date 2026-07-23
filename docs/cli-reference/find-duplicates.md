---
title: "bd find-duplicates"
description: "의미상 유사하지만 정확히 중복되지는 않는 이슈를 찾습니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc find-duplicates`에서 생성되었습니다.

의미상 유사하지만 정확히 중복되지는 않는 이슈를 찾습니다.

정확히 일치하는 콘텐츠를 찾는 'bd duplicates'와 달리 find-duplicates는 텍스트
유사도 또는 AI를 사용해 표현은 다르지만 같은 주제를 다루는 이슈를 찾습니다.

접근 방식:
  mechanical  토큰 기반 텍스트 유사도(기본값, API 키 불필요)
  ai          LLM 기반 의미 비교(ANTHROPIC_API_KEY 또는 ai.api_key 필요)

mechanical 접근 방식은 제목과 설명을 토큰화한 뒤 모든 이슈 쌍의 Jaccard 유사도를
계산합니다. 빠르고 무료이지만 표현이 매우 다른 의미상 유사한 이슈를 놓칠 수 있습니다.

AI 접근 방식은 의미 비교를 위해 후보 쌍을 Claude에 보냅니다. 먼저 mechanical 사전
필터링으로 API 호출 수를 줄인 뒤 LLM에 남은 쌍이 실제 중복인지 판단하도록 요청합니다.

예시:
  bd find-duplicates                       # mechanical 유사도(기본값)
  bd find-duplicates --threshold 0.4       # 낮은 임곗값 = 더 많은 결과
  bd find-duplicates --method ai           # 의미 비교에 AI 사용
  bd find-duplicates --status open         # 열린 이슈만 검사
  bd find-duplicates --limit 20            # 상위 20개 쌍 표시
  bd find-duplicates --json                # JSON 출력

```
bd find-duplicates [flags]
```

**별칭:** find-dups

**플래그:**

```
  -n, --limit int         표시할 최대 쌍 수(기본값 50)
      --method string     감지 방법: mechanical, ai(기본값 "mechanical")
      --model string      사용할 AI 모델(--method ai에서만 사용, 기본값은 ai.model 구성)
  -s, --status string     상태로 필터링(기본값: 닫히지 않음)
      --threshold float   유사도 임곗값(0.0-1.0, 낮을수록 결과 증가)(기본값 0.5)
```
