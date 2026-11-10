package issueops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// applyBatchCommitResult builds a result from a (kind, changed) list, which is
// all the message rule reads.
func applyBatchCommitResult(items ...publicops.ItemResult) publicops.ApplyBatchResult {
	return publicops.ApplyBatchResult{Items: items}
}

func applyBatchItem(kind publicops.ItemKind, changed bool) publicops.ItemResult {
	return publicops.ItemResult{Kind: kind, Changed: changed}
}

// TestApplyBatchCommitMessageNamesWhatLanded pins the entry a request records.
// It is a pure function over the result and the write, so the whole rule is
// pinned here without a database and the contract is left to assert that the
// entry actually appears.
func TestApplyBatchCommitMessageNamesWhatLanded(t *testing.T) {
	for _, test := range []struct {
		name       string
		provenance string
		result     publicops.ApplyBatchResult
		write      BatchApplyWrite
		want       string
	}{
		{
			name:   "one of each kind",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemCreate, true), applyBatchItem(publicops.ItemUpdate, true), applyBatchItem(publicops.ItemClose, true), applyBatchItem(publicops.ItemDepAdd, true)),
			write:  BatchApplyWrite{Changed: true},
			want:   "bd: apply 1 create, 1 update, 1 close, 1 edge",
		},
		{
			name:   "counts are pluralized and ordered by kind, never by request position",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemDepAdd, true), applyBatchItem(publicops.ItemCreate, true), applyBatchItem(publicops.ItemDepAdd, true), applyBatchItem(publicops.ItemCreate, true)),
			write:  BatchApplyWrite{Changed: true},
			want:   "bd: apply 2 creates, 2 edges",
		},
		{
			name:   "an item that changed nothing is not counted",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemCreate, true), applyBatchItem(publicops.ItemClose, false)),
			write:  BatchApplyWrite{Changed: true},
			want:   "bd: apply 1 create",
		},
		{
			// The counts are ItemResult.Changed, which is what a CALLER can
			// observe. A same-type edge re-add rewrites that edge row's metadata
			// and a re-close can still settle blocked state, so the transaction
			// has something to commit while every count is zero. An empty
			// message here would be an unlabelled Dolt commit on the store legs
			// and a rollback of a real write on the unit-of-work leg.
			name:   "a request that wrote without landing anything still names an entry",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemDepAdd, false)),
			write:  BatchApplyWrite{Changed: true, Tables: ChangedTables{"dependencies": true}},
			want:   "bd: apply batch",
		},
		{
			name:   "nothing changed and nothing was staged is the empty message",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemUpdate, false), applyBatchItem(publicops.ItemClose, false)),
			write:  BatchApplyWrite{},
			want:   "",
		},
		{
			// The wisp trap, generalized: the store legs stage nothing for an
			// all-ephemeral batch and record no entry whatever this returns, but
			// the unit-of-work leg reads "" as "roll this attempt back". A
			// durable-only count would delete an ephemeral batch's work there.
			name:   "an all-ephemeral batch still gets a message",
			result: applyBatchCommitResult(applyBatchItem(publicops.ItemCreate, true), applyBatchItem(publicops.ItemCreate, true)),
			write:  BatchApplyWrite{Changed: true},
			want:   "bd: apply 2 creates",
		},
		{
			name:       "provenance replaces the default",
			provenance: "bd: apply plan.md",
			result:     applyBatchCommitResult(applyBatchItem(publicops.ItemCreate, true)),
			write:      BatchApplyWrite{Changed: true},
			want:       "bd: apply plan.md",
		},
		{
			// Provenance changes how the entry READS, never WHETHER one is
			// recorded — UpdateRequest.Provenance's rule, inherited whole.
			name:       "provenance does not conjure an entry out of a request that wrote nothing",
			provenance: "bd: apply plan.md",
			result:     applyBatchCommitResult(applyBatchItem(publicops.ItemUpdate, false)),
			write:      BatchApplyWrite{},
			want:       "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := storage.ApplyBatchPlan{Provenance: test.provenance}
			if got := ApplyBatchCommitMessage(plan, test.result, test.write); got != test.want {
				t.Fatalf("ApplyBatchCommitMessage = %q, want %q", got, test.want)
			}
		})
	}
}

// TestApplyBatchCommitMessageNamesNoIDs pins the rule CreateBatchCommitMessage
// states for its own default: a request can carry a hundred items and mint a
// hundred ids, and an entry naming them all is the diff written twice.
func TestApplyBatchCommitMessageNamesNoIDs(t *testing.T) {
	items := make([]publicops.ItemResult, 0, 3)
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		items = append(items, publicops.ItemResult{Kind: publicops.ItemCreate, IssueID: id, Changed: true})
	}
	message := ApplyBatchCommitMessage(storage.ApplyBatchPlan{}, publicops.ApplyBatchResult{Items: items},
		BatchApplyWrite{Changed: true})
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		if strings.Contains(message, id) {
			t.Fatalf("commit message %q names %s; the default names a COUNT and never an id", message, id)
		}
	}
	if message != "bd: apply 3 creates" {
		t.Fatalf("commit message = %q, want the count spelling", message)
	}
}
