# Attribution and Credits

## beads-merge 3-Way Merge Algorithm

The 3-way merge functionality in `internal/merge/` is based on **beads-merge** by **@neongreen**.

- **Original Repository**: https://github.com/neongreen/mono/tree/main/beads-merge
- **Author**: @neongreen (https://github.com/neongreen)
- **Integration Discussion**: https://github.com/neongreen/mono/issues/240

### What We Vendored

The core merge algorithm from beads-merge has been adapted and integrated into bd:
- Field-level 3-way merge logic
- Issue identity matching (id + created_at + created_by)
- Dependency and label merging with deduplication
- Timestamp handling (max wins)
- Deletion detection
- Conflict marker generation

### Changes Made

- Adapted to use bd's `internal/types.Issue` instead of custom types
- Integrated with bd's JSONL export/import system
- Added support for bd-specific fields (Design, AcceptanceCriteria, etc.)
- Exposed as `bd merge` CLI command and library API

### License

The original beads-merge code is used with permission from @neongreen. We are grateful for their contribution to the beads ecosystem.

### Thank You

Special thanks to @neongreen for building beads-merge and graciously allowing us to integrate it into bd. This solves critical multi-workspace sync issues and makes beads much more robust for collaborative workflows.
