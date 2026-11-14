package dolt

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMigratedSQLCommitFailure pins the other half of the post-commit
// publication contract (GH#5752): publication follows a SUCCESSFUL SQL
// commit only. When the mutation's SQL statement fails, or its COMMIT's
// response is lost (ErrCommitIndeterminate), every migrated write returns
// that error, attempts its mutation once — no replay — and issues no
// DOLT_ADD or DOLT_COMMIT; an indeterminate commit is never converted into
// success.
func TestMigratedSQLCommitFailure(t *testing.T) {
	failures := []struct {
		name  string
		setup func(*publicationOrderDriver, migratedWrite) error
		check func(*testing.T, string, error, error)
	}{
		{
			name: "MutationStatementFails",
			setup: func(d *publicationOrderDriver, w migratedWrite) error {
				refused := errors.New("mutation refused")
				d.execErr = func(query string) error {
					if w.mutation(pubEvent{query: query, inTx: true}) {
						return refused
					}
					return nil
				}
				return refused
			},
			check: func(t *testing.T, name string, err, want error) {
				if !errors.Is(err, want) {
					t.Errorf("%s: error = %v, want the mutation's failure %v", name, err, want)
				}
			},
		},
		{
			name: "SQLCommitIndeterminate",
			setup: func(d *publicationOrderDriver, w migratedWrite) error {
				d.mutationCommitErr = testConnectionLoss
				d.failCommitOf = w.mutation
				return testConnectionLoss
			},
			check: func(t *testing.T, name string, err, want error) {
				if !errors.Is(err, ErrCommitIndeterminate) {
					t.Errorf("%s: error = %v, want ErrCommitIndeterminate", name, err)
				}
				if !errors.Is(err, want) {
					t.Errorf("%s: error = %v, want cause %v", name, err, want)
				}
			},
		},
	}
	for _, failure := range failures {
		for _, w := range migratedWrites() {
			t.Run(failure.name+"/"+w.name, func(t *testing.T) {
				d := newPublicationOrderDriver()
				s := d.store()
				t.Cleanup(func() { _ = s.db.Close() })
				if w.setup != nil {
					w.setup(d, s)
				}
				want := failure.setup(d, w)

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				err := w.run(ctx, s)
				evs := d.snapshot()
				if err == nil {
					t.Fatalf("%s: error = nil, want the SQL failure reported; events:%s", w.name, formatPubEvents(evs))
				}
				failure.check(t, w.name, err, want)
				if n := mutationTxCount(evs, w.mutation); n != 1 {
					t.Errorf("%s: mutation transactions = %d, want 1 (no replay); events:%s", w.name, n, formatPubEvents(evs))
				}
				requireNoPublication(t, w.name, evs)
			})
		}
	}
}
