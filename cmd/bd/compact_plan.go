package main

import (
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
}

// planCompaction partitions logEntries (as returned by Log: newest commit date
// first, non-empty) around cutoff. Old/recent membership, initialHash and
// boundaryHash are selected by commit date as before. The replay order of the
// recent commits is by Dolt's commit_order, not by date: commit dates are not
// required to be monotonic along the parent chain, so a child dated before its
// parent would otherwise be cherry-picked first and conflict (#6516).
func planCompaction(logEntries []storage.CommitInfo, cutoff time.Time) compactPlan {
	var plan compactPlan
	recent := make([]storage.CommitInfo, 0, len(logEntries))
	for _, entry := range logEntries {
		if entry.Date.Before(cutoff) {
			plan.oldCommits++
			if plan.boundaryHash == "" {
				plan.boundaryHash = entry.Hash
			}
		} else {
			recent = append(recent, entry)
		}
	}
	plan.initialHash = logEntries[len(logEntries)-1].Hash

	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CommitOrder < recent[j].CommitOrder
	})
	plan.recentHashes = make([]string, 0, len(recent))
	for _, entry := range recent {
		plan.recentHashes = append(plan.recentHashes, entry.Hash)
	}
	return plan
}
