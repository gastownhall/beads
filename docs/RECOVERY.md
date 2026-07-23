# 복구 플레이북

복구 플레이북은 [docs/recovery/](recovery/index.md)로 이동했으며, 문서 사이트의
[https://beads.gascity.com/recovery/](https://beads.gascity.com/recovery/)에서
볼 수 있습니다.

> **이 파일이 존재하는 이유:** 출시된 `bd` 바이너리는 이 파일을 가리키는 URL을
> 출력합니다. v1.1.0의 Dolt 병합 거부 안내(`printAncestorPKMismatchGuidance`,
> `v1.1.0` 태그의 `cmd/bd/dolt.go`)는 `docs/RECOVERY.md#pk-fork-refused`를
> 출력합니다. 이 스텁은 출력된 링크가 실제 플레이북을 안내하는 페이지로
> 연결되도록 유지합니다. 출시된 바이너리가 이 링크를 출력하는 동안 삭제하지
> 마세요. `test/docsync`에 등록된, 포인터 스텁 금지 규칙의 의도적인 예외입니다.
