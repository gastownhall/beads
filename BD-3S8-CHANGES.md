# BD-3S8: Multi-Clone Sync Fix

## Problem

When multiple clones of a repository both commit to the `beads-sync` branch and one tries to pull, git's merge would fail due to diverged histories. This made multi-clone workflows unreliable.

## Solution

Replace git's commit-level merge with a content-based merge that handles divergence gracefully:

1. **Fetch** (not pull) from remote
2. **Detect divergence** using `git rev-list --left-right --count`
3. **Extract JSONL** from merge base, local HEAD, and remote
4. **Merge content** using bd's 3-way merge algorithm
5. **Reset to remote's history** (adopt their commit graph)
6. **Commit merged content** on top

This ensures sync never fails due to git merge conflicts - we handle merging at the JSONL content level where we have semantic understanding of the data.

## Changes

### `internal/syncbranch/worktree.go`

**New functions:**
- `getDivergence()` - Detects how many commits local/remote are ahead/behind
- `performContentMerge()` - Extracts and merges JSONL content from base/local/remote
- `performDeletionsMerge()` - Merges deletions.jsonl by union (keeps all deletions)
- `extractJSONLFromCommit()` - Extracts file content from a specific git commit
- `copyJSONLToMainRepo()` - Refactored helper for copying JSONL files
- `preemptiveFetchAndFastForward()` - Reduces divergence by fetching before commit

**Modified functions:**
- `PullFromSyncBranch()` - Now handles three cases:
  - Already up-to-date: Remote has nothing new
  - Fast-forward: Simple `--ff-only` merge
  - **Diverged**: Content-based merge (the fix)
- `CommitToSyncBranch()` - Now fetches and fast-forwards before committing

**Enhanced structs:**
- `PullResult` - Added `Merged` and `FastForwarded` fields

### `cmd/bd/sync.go`

- Updated output messages to show merge type (fast-forward vs merged divergent histories)

### `internal/syncbranch/worktree_divergence_test.go` (new file)

Test coverage for:
- `getDivergence()` - 4 scenarios
- `extractJSONLFromCommit()` - 3 scenarios
- `performContentMerge()` - 2 scenarios
- `performDeletionsMerge()` - 2 scenarios

## How It Works

```
Clone A commits and pushes: origin/beads-sync = A -- B -- C
Clone B commits locally:     local beads-sync = A -- B -- D

When Clone B syncs:
1. Fetch: gets C from origin
2. Detect divergence: local ahead 1, remote ahead 1
3. Find merge base: B
4. Extract: base=B's JSONL, local=D's JSONL, remote=C's JSONL
5. Content merge: merge JSONL using 3-way algorithm
6. Reset to origin: beads-sync = A -- B -- C
7. Commit merged: beads-sync = A -- B -- C -- M (merged content)
8. Push: no conflict, linear history
```

## Merge Rules

The 3-way merge uses these rules (from `internal/merge/merge.go`):

- **New issues**: Added from both sides
- **Deleted issues**: Deletion wins over modification
- **Modified issues**: Field-level merge
  - `status`: "closed" always wins over "open"
  - `updated_at`: Takes the max (latest)
  - `closed_at`: Only set if status is "closed"
  - `dependencies`: Union of both sides
  - Other fields: Standard 3-way merge

## Edge Cases Handled

1. **Remote branch doesn't exist** - Nothing to pull, return early
2. **No common ancestor** - Use empty base for merge
3. **File doesn't exist in commit** - Use empty content
4. **Deletions.jsonl missing** - Non-fatal, skip deletion merge
5. **True conflicts** - Currently fails with error (manual resolution required)

## Future Improvements

### 1. Auto-Resolve All Conflicts (No Manual Resolution Required)

Currently, true conflicts (both sides changed same field to different values) fail the sync. This should be changed to auto-resolve deterministically:

| Field | Auto-Resolution Strategy |
|-------|-------------------------|
| `updated_at` | Already handled - takes max (latest) |
| `closed_at` | Already handled - takes max (latest) |
| `status` | Already handled - "closed" wins |
| `Priority` | Take higher priority (lower number = more urgent) |
| `IssueType` | Take left (local wins) |
| `Notes` | **Concatenate both** with separator (preserves all contributions) |
| `Title` | Take from side with latest `updated_at` on the issue |
| `Description` | Take from side with latest `updated_at` on the issue |

With this strategy, **no conflicts ever require manual resolution** - there's always a deterministic auto-resolution. The merge driver becomes fully automatic.

### 2. Auto-Push After Merge (Default Behavior)

Users shouldn't need to review merge diffs on beads metadata. The goal is "one command that just works":

```
bd sync  # Should handle everything, including push
```

**Proposed behavior:**
- After successful content merge, auto-push by default
- Only hold off on push when unsafe conditions detected

**Safety checks before auto-push:**
1. No conflict markers in JSONL (shouldn't happen with full auto-resolve)
2. Issue count sanity check - didn't drop to zero unexpectedly
3. Reasonable deletion threshold - didn't delete > N% of issues in one sync

**The deletions manifest problem:**
- In multi-clone environments, deletions from one clone propagate to others
- This is correct behavior, but can feel like "corruption" when unexpected
- Swarms legitimately close/delete all issues sometimes
- Hard to distinguish "swarm finished all work" from "corruption"

**Proposed safeguards:**
- Track whether issues were *closed* (status change) vs *deleted* (removed from JSONL)
- Closing all issues = legitimate (swarm finished)
- Deleting all issues when there were many = suspicious, pause for confirmation
- Config option: `sync.auto_push` (default: true, can set to false for paranoid mode)

**Integration with bd doctor:**
- `bd doctor --fix` should also run this recovery logic
- But `bd doctor` is for daily/upgrade maintenance, not inner loop
- `bd sync` must handle divergence recovery itself

**The "one nuclear fix" philosophy:**
- `bd sync` should just work 99.9% of the time
- Auto-resolve all conflicts
- Auto-push when safe
- Only fail/pause when genuinely dangerous (mass deletion detected)

### 3. V1 Implementation Plan

Keep it simple for the first iteration:

**Auto-push behavior:**
1. After successful content merge, auto-push by default
2. One safety check: if issue count dropped by >50% AND there were >5 issues before, log a warning but still push
3. Config option `sync.require_confirmation_on_mass_delete` (default: false) for paranoid users who want to be prompted

**Rationale:**
- Logging gives forensics if something goes wrong
- Doesn't block the happy path (99.9% of syncs)
- Users who've been burned can enable confirmation mode
- We can tighten safeguards later based on real-world feedback

**What "mass deletion" means:**
- Issues that **vanished** from `issues.jsonl` (not just closed)
- `status=closed` is fine - swarm finished legitimately
- Issues disappearing entirely is suspicious

**Future safeguards (not v1):**
- Tombstone TTL: Ignore deletions older than N days
- Deletion rate limit: Pause if deletions.jsonl suddenly has 100+ new entries
- Protected issues: Certain issues can't be deleted via sync

---

## Summary of Work Items

1. **Already implemented (this PR):**
   - Content-based merge for diverged histories
   - Pre-emptive fetch before commit
   - Deletions.jsonl merge
   - Fast-forward detection

2. **Still to implement:**
   - Auto-resolve all field conflicts (no manual resolution)
   - Auto-push after merge with safety check
   - Mass deletion warning/logging
   - Config option for confirmation mode
