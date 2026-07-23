---
title: "bd remember"
description: "세션과 계정 교체 후에도 유지되는 메모리를 저장합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc remember`에서 생성되었습니다.

세션과 계정 교체 후에도 유지되는 메모리를 저장합니다.

메모리는 prime 시점(bd prime)에 주입되므로 수동 로드 없이
모든 세션에서 사용할 수 있습니다.

위치 인수는 메모리 콘텐츠입니다(--key를 지정하지 않으면 키가 콘텐츠에서 자동 생성됨).
편의를 위해 인수가 기존 메모리를 가리키는 단순 키이면 저장하지 않고 불러옵니다
('bd recall'과 동일). 아무것도 가리키지 않는 단순 키는 거부됩니다. slug와 같은
콘텐츠를 저장하려면 --key를 사용하세요.

예시:
  bd remember "항상 -race 플래그로 테스트 실행"
  bd remember "Dolt 유령 DB는 세 곳에 숨음" --key dolt-phantoms
  bd remember "auth 모듈은 세션이 아닌 JWT 사용" --key auth-jwt
  bd remember dolt-phantoms        # 기존 단순 키: 읽기(= bd recall)

```
bd remember "<insight>" [flags]
```

**플래그:**

```
      --key string   메모리의 명시적 키(설정하지 않으면 콘텐츠에서 자동 생성). 이 키의 메모리가 이미 있으면 제자리에서 업데이트
```
