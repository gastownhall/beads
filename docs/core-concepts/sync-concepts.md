---
title: 동기화 개념
description: Dolt가 동기화의 원본인 이유와 JSONL 내보내기가 bd dolt push 및 pull과 다른 점
---

Beads 이슈 데이터는 Dolt에 저장됩니다. 로컬 Dolt 데이터베이스가 `bd list`,
`bd show`, `bd ready`와 모든 쓰기 명령의 원본입니다.

## 전송 형식

머신 간 동기화에는 Dolt 원격을 사용합니다.

```bash
bd dolt push
bd dolt pull
```

일반적인 git 호스팅 프로젝트에서는 소스 코드에 사용하는 것과 같은 `origin` URL을
Dolt 원격으로 사용할 수 있습니다. Dolt는 `refs/heads/main` 같은 소스 브랜치와
별도로 `refs/dolt/data` 아래에 이슈 기록을 저장합니다.

새 프로젝트에서 `bd init`은 `git remote get-url origin`을 자동 감지하고 `origin`이라는
Dolt 원격을 구성합니다. 첫 `bd dolt push`가 `refs/dolt/data`를 게시합니다. 새 클론에서는
`bd bootstrap`을 실행해 해당 Dolt 기록을 클론해야 합니다. bootstrap이 git origin에서
`refs/dolt/data`를 찾으면 이후 `bd dolt push`와 `bd dolt pull`에 사용할 Dolt 원격으로도
해당 origin을 연결합니다.

## JSONL의 용도

`.beads/issues.jsonl`은 내보내기 파일입니다. 뷰어, 교환, 마이그레이션, 백업에 사용합니다.
정식 머신 간 동기화 채널은 아닙니다.

일상적으로 `bd import .beads/issues.jsonl`을 `bd dolt pull` 대신 사용하지 마세요.
JSONL 가져오기는 upsert만 수행하므로 내보내기에 없는 레코드가 삭제되었는지,
정리되었는지, 아니면 애초에 내보내지지 않았는지 추론할 수 없습니다.

## 훅

pre-commit 훅은 `export.auto=true`일 때 `.beads/issues.jsonl`을 새로 고칩니다.
도구에서 사용하는 내보내기를 최신 상태로 유지하지만 Dolt 기록을 푸시하지는 않습니다.

post-merge 및 post-checkout 훅은 `sync.remote`가 구성되어 있으면 JSONL 가져오기를
건너뜁니다. Dolt 원격이 없는 이전 프로젝트에서는 호환성 대체 수단으로 JSONL을
가져오고 이것이 지속 가능한 동기화가 아니라는 경고를 표시할 수 있습니다.

## 복구

git-origin 원격 자동 연결 기능이 생기기 전에 초기화한 프로젝트에서는 먼저 기준 로컬
Dolt 데이터베이스가 있는 머신을 선택합니다. 그런 다음 다음을 실행합니다.

```bash
bd dolt remote list
bd export -o .beads/issues.pre-remote.jsonl   # 선택적 이슈 감사 내보내기
bd dolt remote add origin <git-origin-url>
bd dolt push
```

필요한 경우 Dolt 호환 git URL 형식을 사용하세요. 예를 들면
`git+ssh://git@github.com/org/repo.git` 또는
`git+https://github.com/org/repo.git`입니다. `bd dolt remote add origin ...`은
`sync.remote`를 `.beads/config.yaml`에 영구 저장합니다. 새 클론에서 `bd bootstrap`을
실행할 수 있도록 이 구성 변경을 커밋하고 푸시하세요.

그런 다음 다른 머신에서는 다음을 실행해야 합니다.

```bash
bd dolt pull
# 또는 로컬 데이터베이스가 오래되었거나 없는 경우:
bd bootstrap
```
