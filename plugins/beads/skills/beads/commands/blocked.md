---
description: Show blocked issues
argument-hint: []
---

Show all issues that are blocked by dependencies.

Use `bd blocked` to see which issues have blockers preventing them from being worked on. This is the inverse of `bd ready` - it shows what's NOT ready.

Blocked issues have one or more dependencies with type "blocks" that are still open. Once all blocking dependencies are closed, the issue becomes ready and will appear in `bd ready`.

Useful for:
- Understanding why work is stuck
- Identifying critical path items
- Planning dependency resolution
