---
title: "bd federation"
description: "federation 명령에는 CGO와 Dolt 저장소 백엔드가 필요합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc federation`에서 생성되었습니다.

federation 명령에는 CGO와 Dolt 저장소 백엔드가 필요합니다.

이 바이너리는 CGO 지원 없이 빌드되었습니다. federation 기능을 사용하려면:
  1. GitHub 릴리스의 미리 빌드된 바이너리를 사용하거나
  2. CGO를 활성화하여 소스에서 빌드하세요

federation은 각자 Dolt 데이터베이스를 유지하면서 원격을 통해 업데이트를 공유하는
여러 워크스페이스 간 동기화된 이슈 추적을 지원합니다.

```
bd federation [flags]
```
