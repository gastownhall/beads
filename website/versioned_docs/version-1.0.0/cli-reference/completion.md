---
id: completion
title: bd completion
slug: /cli-reference/completion
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc completion`

## bd completion

Generate the autocompletion script for bd for the specified shell.
See each sub-command's help for details on how to use the generated script.


```
bd completion
```

### bd completion bash

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source &lt;(bd completion bash)

To load completions for every new session, execute once:

#### Linux:

	bd completion bash &gt; /etc/bash_completion.d/bd

#### macOS:

	bd completion bash &gt; $(brew --prefix)/etc/bash_completion.d/bd

You will need to start a new shell for this setup to take effect.


```
bd completion bash
```

**Flags:**

```
      --no-descriptions   disable completion descriptions
```

### bd completion fish

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	bd completion fish | source

To load completions for every new session, execute once:

	bd completion fish &gt; ~/.config/fish/completions/bd.fish

You will need to start a new shell for this setup to take effect.


```
bd completion fish [flags]
```

**Flags:**

```
      --no-descriptions   disable completion descriptions
```

### bd completion powershell

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	bd completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


```
bd completion powershell [flags]
```

**Flags:**

```
      --no-descriptions   disable completion descriptions
```

### bd completion zsh

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" &gt;&gt; ~/.zshrc

To load completions in your current shell session:

	source &lt;(bd completion zsh)

To load completions for every new session, execute once:

#### Linux:

	bd completion zsh &gt; "$&#123;fpath[1]&#125;/_bd"

#### macOS:

	bd completion zsh &gt; $(brew --prefix)/share/zsh/site-functions/_bd

You will need to start a new shell for this setup to take effect.


```
bd completion zsh [flags]
```

**Flags:**

```
      --no-descriptions   disable completion descriptions
```
