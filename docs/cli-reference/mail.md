---
title: "bd mail"
description: "메일 작업을 외부 메일 제공자에 위임합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc mail`에서 생성되었습니다.

메일 작업을 외부 메일 제공자에 위임합니다.

에이전트는 beads 작업 중 'bd mail'을 자주 입력하지만, 메일 기능은 일반적으로
오케스트레이터가 제공합니다. 이 명령은 구성된 메일 제공자에 위임하여
그 간극을 해소합니다.

구성(순서대로 확인):
  1. BEADS_MAIL_DELEGATE 또는 BD_MAIL_DELEGATE 환경 변수
  2. 'mail.delegate' 구성 설정(bd config set mail.delegate "gt mail")

예시:
  # 위임 구성(최초 한 번 설정)
  `export BEADS_MAIL_DELEGATE="gt mail"`
  # 또는
  bd config set mail.delegate "gt mail"

  # 이후 gt mail처럼 bd mail 사용
  bd mail inbox                    # 받은편지함 나열
  bd mail send mayor/ -s "안녕하세요" # 메일 보내기
  bd mail read msg-123             # 메시지 읽기

```
bd mail [subcommand] [args...] [flags]
```
