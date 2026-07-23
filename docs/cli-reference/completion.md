---
title: "bd completion"
description: "지정한 셸용 bd 자동 완성 스크립트를 생성합니다."
---

{/* AUTO-GENERATED: do not edit manually */}

`bd help --doc completion`에서 생성되었습니다.

지정한 셸용 bd 자동 완성 스크립트를 생성합니다.
생성된 스크립트의 자세한 사용법은 각 하위 명령의 도움말을 참조하세요.


```
bd completion [flags]
```

## bd completion bash

bash 셸용 자동 완성 스크립트를 생성합니다.

이 스크립트는 'bash-completion' 패키지에 의존합니다.
아직 설치되지 않았다면 OS 패키지 관리자로 설치할 수 있습니다.

현재 셸 세션에 자동 완성을 로드하려면:

	source &lt;(bd completion bash)

모든 새 세션에 자동 완성을 로드하려면 한 번 실행하세요:

### Linux:

	bd completion bash &gt; /etc/bash_completion.d/bd

### macOS:

	bd completion bash &gt; $(brew --prefix)/etc/bash_completion.d/bd

이 설정을 적용하려면 새 셸을 시작해야 합니다.


```
bd completion bash
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

## bd completion fish

fish 셸용 자동 완성 스크립트를 생성합니다.

현재 셸 세션에 자동 완성을 로드하려면:

	bd completion fish | source

모든 새 세션에 자동 완성을 로드하려면 한 번 실행하세요:

	bd completion fish &gt; ~/.config/fish/completions/bd.fish

이 설정을 적용하려면 새 셸을 시작해야 합니다.


```
bd completion fish [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

## bd completion powershell

powershell용 자동 완성 스크립트를 생성합니다.

현재 셸 세션에 자동 완성을 로드하려면:

	bd completion powershell | Out-String | Invoke-Expression

모든 새 세션에 자동 완성을 로드하려면 위 명령의 출력을 powershell 프로필에 추가하세요.


```
bd completion powershell [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```

## bd completion zsh

zsh 셸용 자동 완성 스크립트를 생성합니다.

환경에서 셸 자동 완성이 아직 활성화되지 않았다면 활성화해야 합니다.
다음을 한 번 실행하세요:

	echo "autoload -U compinit; compinit" &gt;&gt; ~/.zshrc

현재 셸 세션에 자동 완성을 로드하려면:

	source &lt;(bd completion zsh)

모든 새 세션에 자동 완성을 로드하려면 한 번 실행하세요:

### Linux:

	bd completion zsh &gt; "$&#123;fpath[1]&#125;/_bd"

### macOS:

	bd completion zsh &gt; $(brew --prefix)/share/zsh/site-functions/_bd

이 설정을 적용하려면 새 셸을 시작해야 합니다.


```
bd completion zsh [flags]
```

**플래그:**

```
      --no-descriptions   자동 완성 설명 비활성화
```
