---
id: mail
title: bd mail
slug: /cli-reference/mail
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc mail`

## bd mail

Delegates mail operations to an external mail provider.

Agents often type 'bd mail' when working with beads, but mail functionality
is typically provided by the orchestrator. This command bridges that gap
by delegating to the configured mail provider.

Configuration (checked in order):
  1. BEADS_MAIL_DELEGATE or BD_MAIL_DELEGATE environment variable
  2. 'mail.delegate' config setting (bd config set mail.delegate "gt mail")

Examples:
  # Configure delegation (one-time setup)
  export BEADS_MAIL_DELEGATE="gt mail"
  # or
  bd config set mail.delegate "gt mail"

  # Then use bd mail as if it were gt mail
  bd mail inbox                    # Lists inbox
  bd mail send mayor/ -s "Hi"      # Sends mail
  bd mail read msg-123             # Reads a message

```
bd mail [subcommand] [args...]
```
