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
	// refusal is non-nil when the date partition is not safe to replay: a
	// preserved (recent) commit does not come after every squashed (old)
	// commit in commit order. Callers must refuse to compact instead of
	// replaying, which could silently change main's row data (#6645).
	refusal *compactRefusal
}

// compactRefusal names the commits that make a partition unsafe to replay:
// the lowest-order recent commit that is not after the highest-order old
// commit in Dolt's ancestry order.
type compactRefusal struct {
	recent, old storage.CommitInfo
}

// planCompaction partitions logEntries (as returned by Log: newest commit date
// first, non-empty) around cutoff. Old/recent membership stays date-based
// (Date.Before). Everything positional is by Dolt's commit_order, not by
// date: the replay order of the recent commits (a child dated before its
// parent would otherwise be cherry-picked first and conflict, #6516), and the
// boundary and initial selection — the last old commit and the first commit
// overall in commit order. With old commits dated out of history order, the
// newest-dated old commit can be an obsolete mid-chain state whose squash
// drops later old edits and keeps the superseded chain reachable (#6645).
// Ties keep the first entry in Log order.
//
// The partition itself must also be contiguous in commit order (#6645): when
// a preserved commit is an ANCESTOR of an old commit, the squashed base
// already contains it, and replaying it is a three-way merge onto whatever
// followed — an undo of its edit resurrects the edit with no conflict, and
// main is hard-reset to different data with a success message. Such a plan
// carries a refusal naming the straddling commits; only an actionable plan
// (more than one old commit to squash) can refuse.
func planCompaction(logEntries []storage.CommitInfo, cutoff time.Time) compactPlan {
	var plan compactPlan
	recent := make([]storage.CommitInfo, 0, len(logEntries))
	var initial, maxOld *storage.CommitInfo
	for _, entry := range logEntries {
		if initial == nil || entry.CommitOrder < initial.CommitOrder {
			e := entry
			initial = &e
		}
		if entry.Date.Before(cutoff) {
			plan.oldCommits++
			if maxOld == nil || entry.CommitOrder > maxOld.CommitOrder {
				e := entry
				maxOld = &e
			}
		} else {
			recent = append(recent, entry)
		}
	}
	plan.initialHash = initial.Hash
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
	// After the sort, recent[0] is the lowest-order preserved commit. The
	// partition is safe to replay only if every old commit precedes every
	// recent one in commit order; equality is conservatively unsafe.
	if plan.oldCommits > 1 && len(recent) > 0 && maxOld != nil && recent[0].CommitOrder <= maxOld.CommitOrder {
		plan.refusal = &compactRefusal{recent: recent[0], old: *maxOld}
	}
	return plan
}

// compactRefusalDiagnostic returns the refusal message and remediation hint
// shared by every compact front door (#6645), so embedded, server and
// proxied-server workspaces refuse identically.
func compactRefusalDiagnostic(cutoff time.Time, r *compactRefusal) (message, hint string) {
	message = fmt.Sprintf(
		"cannot compact: commit dates are out of history order across cutoff %s: "+
			"recent commit %s (dated %s) is not after old commit %s (dated %s) "+
			"in commit order; replay could change data",
		cutoff.UTC().Format(time.RFC3339),
		shortCompactHash(r.recent.Hash), r.recent.Date.UTC().Format(time.RFC3339),
		shortCompactHash(r.old.Hash), r.old.Date.UTC().Format(time.RFC3339))
	hint = "No compaction changes were made. Try a larger --days so the cutoff precedes " +
		"the conflicting old history, or wait until the conflicting recent commits are " +
		"older than the cutoff; use --dry-run to recheck."
	return message, hint
}

func shortCompactHash(hash string) string {
	return hash[:min(8, len(hash))]
}
