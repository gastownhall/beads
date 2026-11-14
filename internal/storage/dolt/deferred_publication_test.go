package dolt

import (
	"context"
	"testing"
	"time"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// TestMigratedWritesHonorDeferredVersionCommit pins GH#5752's deferral
// contract: under issueops.WithDeferredVersionCommit (--dolt-auto-commit
// batch/off) every migrated write — the role sweeper and deleter included —
// still commits its SQL transaction but issues no DOLT_ADD and no
// DOLT_COMMIT, leaving its changes in the working set for the explicit
// commit point (CommitPending / bd dolt commit).
func TestMigratedWritesHonorDeferredVersionCommit(t *testing.T) {
	for _, w := range migratedWrites() {
		t.Run(w.name, func(t *testing.T) {
			d := newPublicationOrderDriver()
			s := d.store()
			t.Cleanup(func() { _ = s.db.Close() })
			if w.setup != nil {
				w.setup(d, s)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := w.run(storeops.WithDeferredVersionCommit(ctx), s); err != nil {
				t.Fatalf("%s: %v; events:%s", w.name, err, formatPubEvents(d.snapshot()))
			}
			evs := d.snapshot()
			if mutationCommitIndex(evs, w.mutation) < 0 {
				t.Fatalf("%s: no committed SQL transaction carried the mutation; events:%s", w.name, formatPubEvents(evs))
			}
			requireNoPublication(t, w.name, evs)
		})
	}
}
