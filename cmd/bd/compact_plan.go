package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// compactPlan is the old/recent partition of a Dolt commit history used by
// `bd compact --days N`.
type compactPlan struct {
	// oldCommits is the number of commits dated before the cutoff.
	oldCommits int
	// recentHashes are the commits to preserve, in ascending graph
	// (commit_order) order so a cherry-pick replay applies every parent before
	// its children.
	recentHashes []string
	// initialHash and boundaryHash are the soft-reset target and the temp
	// branch point for the squashed base.
	initialHash, boundaryHash string
	// headHash is the highest-order commit: main's HEAD when the log was
	// read. Compact verifies the replay against it rather than against
	// main's HEAD at the end of the replay, which another writer may have
	// moved since.
	headHash string
	// refusal is set when the plan would squash history (more than one old
	// commit) but the date partition is not contiguous in commit order, so
	// replaying the recent commits onto the squashed base could change data
	// (#6645). Callers must not compact a refused plan.
	refusal *compactRefusal
}

// compactRefusal names the two commits that straddle the cutoff out of
// history order: the lowest-order recent commit is not after the
// highest-order old commit.
type compactRefusal struct {
	cutoff      time.Time
	recent, old storage.CommitInfo
}

// compactRefusalHint is the remediation shown with every compactRefusal.
const compactRefusalHint = "No compaction changes were made. Try a larger --days so the cutoff precedes the conflicting old history, or wait until the conflicting recent commits are older than the cutoff; use --dry-run to recheck."

func (r *compactRefusal) message() string {
	return fmt.Sprintf("cannot compact: commit dates are out of history order across cutoff %s: recent commit %s (dated %s) is not after old commit %s (dated %s) in commit order; replay could change data",
		r.cutoff.UTC().Format(time.RFC3339),
		shortCommitHash(r.recent.Hash), r.recent.Date.UTC().Format(time.RFC3339),
		shortCommitHash(r.old.Hash), r.old.Date.UTC().Format(time.RFC3339))
}

// jsonFields is the dry-run preview's "refusal" object.
func (r *compactRefusal) jsonFields() map[string]string {
	return map[string]string{"error": r.message(), "hint": compactRefusalHint}
}

func shortCommitHash(hash string) string {
	return hash[:min(8, len(hash))]
}

// planCompaction partitions logEntries (as returned by Log: newest commit date
// first, non-empty) around cutoff. Old/recent membership is by commit date;
// everything else is by Dolt's commit_order, because commit dates are not
// required to be monotonic along the parent chain:
//   - initialHash is the lowest-order commit and boundaryHash the
//     highest-order old commit (the first such entry on ties), so the squashed
//     base is the old window's final state even when its dates are shuffled;
//   - headHash is the highest-order commit overall (the first such entry on
//     ties), the HEAD the log was read from and so the tree the replay must
//     reproduce;
//   - the recent commits replay in ascending order, so a child dated before its
//     parent is not cherry-picked first and does not conflict (#6516).
//
// The squashed base holds everything up to the boundary, so on a linear
// history the replay is only safe when every old commit precedes every recent
// commit in commit order. A recent commit at or below the highest-order old
// commit is already in the base; cherry-picking it again can silently
// resurrect an edit a later old commit undid (#6645). Such plans carry a
// refusal. Equal orders are treated as unsafe; this rule does not prove
// arbitrary merge ancestry.
func planCompaction(logEntries []storage.CommitInfo, cutoff time.Time) compactPlan {
	var plan compactPlan
	recent := make([]storage.CommitInfo, 0, len(logEntries))
	var maxOld *storage.CommitInfo
	initial, head := &logEntries[0], &logEntries[0]
	for i, entry := range logEntries {
		if entry.CommitOrder < initial.CommitOrder {
			initial = &logEntries[i]
		}
		if entry.CommitOrder > head.CommitOrder {
			head = &logEntries[i]
		}
		if entry.Date.Before(cutoff) {
			plan.oldCommits++
			if maxOld == nil || entry.CommitOrder > maxOld.CommitOrder {
				maxOld = &logEntries[i]
			}
		} else {
			recent = append(recent, entry)
		}
	}
	plan.initialHash, plan.headHash = initial.Hash, head.Hash
	if maxOld != nil {
		plan.boundaryHash = maxOld.Hash
	}

	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CommitOrder < recent[j].CommitOrder
	})
	plan.recentHashes = make([]string, 0, len(recent))
	for _, entry := range recent {
		plan.recentHashes = append(plan.recentHashes, entry.Hash)
	}

	if plan.oldCommits > 1 && len(recent) > 0 && recent[0].CommitOrder <= maxOld.CommitOrder {
		plan.refusal = &compactRefusal{cutoff: cutoff, recent: recent[0], old: *maxOld}
	}
	return plan
}
