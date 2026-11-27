package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"

	publicops "github.com/steveyegge/beads/issueops"
)

// TestMigratedWritesSkipPublication pins GH#5752's no-publication cases.
// Dry runs keep their result and issue no publication statement at all.
// A write whose DOLT_ADDs leave nothing staged (the change landed only in
// dolt_ignore'd tables, or the tables already matched HEAD) creates no empty
// history commit: the staged-set guard stops it before DOLT_COMMIT.
func TestMigratedWritesSkipPublication(t *testing.T) {
	t.Run("DryRun", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(*publicationOrderDriver)
			run   func(context.Context, *DoltStore) error
		}{
			{
				name:  "DeleteIssues",
				setup: func(d *publicationOrderDriver) { d.rows = existingIssueRows },
				run: func(ctx context.Context, s *DoltStore) error {
					res, err := s.DeleteIssues(ctx, []string{"pub-1"}, false, true, true)
					if err == nil && res == nil {
						return fmt.Errorf("DeleteIssues dry run returned no result")
					}
					return err
				},
			},
			{
				name:  "DeleterDelete",
				setup: func(d *publicationOrderDriver) { d.rows = existingIssueRows },
				run: func(ctx context.Context, s *DoltStore) error {
					del, err := s.Deleter()
					if err != nil {
						return err
					}
					res, err := del.Delete(ctx, publicops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true, DryRun: true})
					if err == nil && (!res.DryRun || res.Deleted != 1) {
						return fmt.Errorf("Delete dry run result = %+v, want DryRun and 1 would-delete", res)
					}
					return err
				},
			},
			{
				name:  "SweeperSweep",
				setup: func(d *publicationOrderDriver) { d.rows = sweepCandidateRows },
				run: func(ctx context.Context, s *DoltStore) error {
					sw, err := s.Sweeper()
					if err != nil {
						return err
					}
					res, err := sw.Sweep(ctx, publicops.SweepRequest{Actor: "alice", Tier: publicops.SweepDurable, IDPattern: "*", DryRun: true})
					if err == nil && (!res.DryRun || res.Swept != 1) {
						return fmt.Errorf("Sweep dry run result = %+v, want DryRun and 1 would-sweep", res)
					}
					return err
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				d := newPublicationOrderDriver()
				s := d.store()
				t.Cleanup(func() { _ = s.db.Close() })
				tc.setup(d)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := tc.run(ctx, s); err != nil {
					t.Fatalf("%s: %v; events:%s", tc.name, err, formatPubEvents(d.snapshot()))
				}
				evs := d.snapshot()
				if n := countEvents(evs, deletesFrom("issues")); n != 0 {
					t.Errorf("%s: dry run deleted issues rows %d times; events:%s", tc.name, n, formatPubEvents(evs))
				}
				requireNoPublication(t, tc.name, evs)
			})
		}
	})

	t.Run("EmptyStagedSet", func(t *testing.T) {
		for _, w := range migratedWrites() {
			t.Run(w.name, func(t *testing.T) {
				d := newPublicationOrderDriver()
				s := d.store()
				t.Cleanup(func() { _ = s.db.Close() })
				if w.setup != nil {
					w.setup(d, s)
				}
				d.stagedCount = 0

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := w.run(ctx, s); err != nil {
					t.Fatalf("%s: %v; events:%s", w.name, err, formatPubEvents(d.snapshot()))
				}
				evs := d.snapshot()
				if mutationCommitIndex(evs, w.mutation) < 0 {
					t.Fatalf("%s: no committed SQL transaction carried the mutation; events:%s", w.name, formatPubEvents(evs))
				}
				if n := countEvents(evs, isDoltCommit); n != 0 {
					t.Errorf("%s: DOLT_COMMIT ran %d times over an empty staged set, want 0 (no empty history commit); events:%s",
						w.name, n, formatPubEvents(evs))
				}
			})
		}
	})
}
