---
title: "bd close"
description: "하나 이상의 이슈를 닫습니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc close`에서 생성되었습니다.

하나 이상의 이슈를 닫습니다.

이슈 ID를 지정하지 않으면 마지막으로 접근한 이슈(가장 최근 create, update, show
또는 close 작업)를 닫습니다.

여러 이슈를 닫을 때는 모든 ID에 하나의 --reason을 제공하거나 ID마다 --reason을
한 번씩 반복하세요. 사유는 위치에 따라 대응됩니다. 명령줄에서 플래그가 나타나는
위치와 관계없이 첫 번째 --reason은 첫 번째 ID에, 두 번째 --reason은 두 번째 ID에
적용됩니다.

```
bd close [id...] [flags]
```

**별칭:** done

**플래그:**

```
      --claim-next           사용 가능한 이슈 중 다음으로 우선순위가 높은 이슈를 자동으로 맡기
      --continue             molecule의 다음 단계로 자동 진행
  -f, --force                고정된 이슈 또는 충족되지 않은 gate를 강제로 닫기
      --no-auto              --continue와 함께 다음 단계를 표시하되 맡지 않음
  -r, --reason string        닫는 사유
      --reason-file string   파일에서 닫는 사유 읽기(stdin은 - 사용)
      --session string       Claude Code 세션 ID(또는 CLAUDE_SESSION_ID 환경 변수 설정)
      --suggest-next         닫은 후 새로 차단 해제된 이슈 표시
```
