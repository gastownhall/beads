---
description: Delete issues and clean up references
argument-hint: [issue-ids...] [--force]
---

Delete one or more issues and clean up all references.

## Safety Features

- **Preview mode**: Default shows what would be deleted
- **--force**: Required to actually delete
- **--dry-run**: Preview collision detection
- **Dependency checks**: Fails if issue has dependents (unless --cascade or --force)

## Batch Deletion

- Delete multiple: `bd delete bd-1 bd-2 bd-3 --force`
- Delete from file: `bd delete --from-file deletions.txt --force`

## Dependency Handling

- **Default**: Fails if issue has dependents not in deletion set
- **--cascade**: Recursively delete all dependent issues
- **--force**: Delete and orphan dependents

## What Gets Deleted

1. All dependency links (any type, both directions)
2. Text references updated to "[deleted:ID]" in connected issues
3. Issue removed from database

This operation cannot be undone. Use with caution!
