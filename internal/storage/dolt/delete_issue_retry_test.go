package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestDeleteIssueReplaysALostCommitTimeMerge pins that the store's two plain
// delete entry points, DeleteIssue and DeleteIssues, replay a delete that
// loses Dolt's commit-time merge, as every other issue write through
// withRetryTx does. They are the path of `bd gc`, of `bd doctor --fix`'s
// maintenance deletes and of the personal-store migration, none of which goes
// through the Deleter role.
//
// The race is held, not timed: the gate stops the delete at its first commit
// point, after it has removed the row in its own transaction, while a second
// session updates that same row and commits. Released, the delete's commit is
// a delete against a modified row, which the server refuses with a
// serialization failure and a guaranteed rollback. Single-attempt, that error
// reached the caller and the row stayed; `bd gc` reported it as a warning and
// exited 0 with the issue still there.
func TestDeleteIssueReplaysALostCommitTimeMerge(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)

	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(t *testing.T, id string) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: "delete retry " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	update := func(id string) func(context.Context) raceOp {
		return func(ctx context.Context) raceOp {
			op := raceOp{Op: "update"}
			decodeRaceError(&op, store.UpdateIssue(ctx, id, map[string]interface{}{"title": "updated under the delete"}, "racer"))
			return op
		}
	}

	cases := []struct {
		name   string
		id     string
		delete func(id string) func(context.Context) raceOp
	}{
		{
			name: "DeleteIssue",
			id:   "test-delretry1",
			delete: func(id string) func(context.Context) raceOp {
				return func(ctx context.Context) raceOp {
					op := raceOp{Op: "delete-issue"}
					decodeRaceError(&op, store.DeleteIssue(ctx, id))
					return op
				}
			},
		},
		{
			name: "DeleteIssues",
			id:   "test-delretry2",
			delete: func(id string) func(context.Context) raceOp {
				return func(ctx context.Context) raceOp {
					res, err := store.DeleteIssues(ctx, []string{id}, false, false, false)
					op := raceOp{Op: "delete-issues"}
					if res != nil {
						op.Deleted = res.DeletedCount
					}
					decodeRaceError(&op, err)
					return op
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, tc.id)
			loserLabel, winnerLabel := "delete/"+tc.name, "update/"+tc.name
			loser, winner := runHeldRace(ctx, t, gate, loserLabel, winnerLabel, tc.delete(tc.id), update(tc.id))

			if winner.err != nil {
				t.Fatalf("the update that commits under the held delete failed: %v", winner.err)
			}
			if loser.err != nil {
				t.Fatalf("%s = %v (mysql %d); a delete that loses the commit-time merge must be replayed, not surfaced",
					tc.name, loser.err, loser.MySQLCode)
			}
			// Two attempts prove the refusal happened and was replayed. One
			// attempt would mean the server merged the two writes and this
			// test no longer exercises the conflict it names.
			if loser.Attempts != 2 {
				t.Fatalf("%s began %d transaction(s), want 2: the refused attempt and its replay", tc.name, loser.Attempts)
			}
			if tc.name == "DeleteIssues" && loser.Deleted != 1 {
				t.Fatalf("DeleteIssues reported %d deleted, want 1: the replayed attempt's count alone", loser.Deleted)
			}
			if _, err := store.GetIssue(ctx, tc.id); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("GetIssue(%s) after the replayed delete = %v, want ErrNotFound", tc.id, err)
			}
		})
	}
}
