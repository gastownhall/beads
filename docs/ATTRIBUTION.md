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

The original beads-merge code is licensed under the MIT License:

```
MIT License

Copyright (c) 2025 Emily (@neongreen)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Thank You

Special thanks to @neongreen for building beads-merge and graciously allowing us to integrate it into bd. This solves critical multi-workspace sync issues and makes beads much more robust for collaborative workflows.
