---
description: View or manage comments on an issue
argument-hint: [issue-id]
---

View or add comments to a beads issue.

Comments are separate from issue properties (title, description, etc.) because they serve a different purpose: they're a **discussion thread** rather than **singular editable fields**. Use `bd comments` for threaded conversations and `bd edit` for core issue metadata.

## View Comments

To view all comments on an issue:
- $1: Issue ID (e.g., bd-123)

Use the beads CLI `bd comments <issue-id>` to list all comments. Show them to the user with timestamps and authors.

## Add Comment

To add a comment:
- $1: "add"
- $2: Issue ID
- $3: Comment text (or use -f flag for file input)

Use `bd comments add <issue-id> "comment text"` to add a comment. Confirm the comment was added successfully.

Comments are useful for:
- Progress updates during work
- Design notes or technical decisions
- Links to related resources
- Questions or blockers
