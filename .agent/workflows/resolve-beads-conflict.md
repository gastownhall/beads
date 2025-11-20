---
description: How to resolve merge conflicts in .beads/beads.jsonl
---

# Resolving `beads.jsonl` Merge Conflicts

If you encounter a merge conflict in `.beads/beads.jsonl` that doesn't have standard git conflict markers (or if `bd merge` failed automatically), follow this procedure.

## 1. Identify the Conflict
Check if `beads.jsonl` is in conflict:
```powershell
git status
```

## 2. Extract the 3 Versions
Git stores three versions of conflicted files in its index:
1. Base (common ancestor)
2. Ours (current branch)
3. Theirs (incoming branch)

Extract them to temporary files:
```powershell
git show :1:.beads/beads.jsonl > beads.base.jsonl
git show :2:.beads/beads.jsonl > beads.ours.jsonl
git show :3:.beads/beads.jsonl > beads.theirs.jsonl
```

## 3. Run `bd merge` Manually
Run the `bd merge` tool manually with the `--debug` flag to see what's happening.
Syntax: `bd merge <output> <base> <ours> <theirs>`

```powershell
bd merge beads.merged.jsonl beads.base.jsonl beads.ours.jsonl beads.theirs.jsonl --debug
```

## 4. Verify the Result
Check the output of the command.
- **Exit Code 0**: Success. `beads.merged.jsonl` contains the clean merge.
- **Exit Code 1**: Conflicts remain. `beads.merged.jsonl` will contain conflict markers. You must edit it manually to resolve them.

Optionally, verify the content (e.g., check for missing IDs if you suspect data loss).

## 5. Apply the Merge
Overwrite the conflicted file with the resolved version:
```powershell
cp beads.merged.jsonl .beads/beads.jsonl
```

## 6. Cleanup and Continue
Stage the resolved file and continue the merge:
```powershell
git add .beads/beads.jsonl
git merge --continue
```

## 7. Cleanup Temporary Files
```powershell
rm beads.base.jsonl beads.ours.jsonl beads.theirs.jsonl beads.merged.jsonl
```
