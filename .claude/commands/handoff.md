---
description: Hand off to fresh session, work continues from hook
allowed-tools: Bash(gt mail send:*),Bash(gt handoff:*)
argument-hint: [message]
---

Hand off to a fresh session.

User's handoff message (if any): $ARGUMENTS

Execute these steps in order:

1. If user provided a message, send handoff mail to yourself first.
   Construct your mail address from your identity (e.g., beads/crew/emma for crew).
   Example: `gt mail send beads/crew/emma -s "HANDOFF: Session cycling" -m "USER_MESSAGE_HERE"`

2. Run the handoff command (this will respawn your session with a fresh Claude):
   `gt handoff`

Note: The new session will auto-prime via the SessionStart hook and find your handoff mail.
End watch. A new session takes over, picking up any molecule on the hook.
