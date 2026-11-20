package httpclient

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// TestReadyExcludeParentsRefusesBeforeDialing pins GH#6817 over http: no ready
// operation publishes an exclude-parents parameter, so a list, a count and a
// claim that ask for it refuse with a typed unsupported error before any
// request leaves. Dropping the flag would answer, count or claim the very
// containers the caller asked to leave out.
func TestReadyExcludeParentsRefusesBeforeDialing(t *testing.T) {
	ctx := context.Background()
	const row = "E-ReadyRequest.ExcludeParentsWithOpenChildren"
	filter := issueops.ReadyRequest{ExcludeParentsWithOpenChildren: true}

	for _, tc := range []struct {
		name string
		call func(*Store) error
	}{
		{"Reader.Ready", func(s *Store) error {
			reader, err := s.IssueReader()
			if err != nil {
				t.Fatalf("IssueReader(): %v", err)
			}
			_, err = reader.Ready(ctx, filter)
			return err
		}},
		{"ReadyCounter.CountReady", func(s *Store) error {
			counter, err := s.ReadyCounter()
			if err != nil {
				t.Fatalf("ReadyCounter(): %v", err)
			}
			_, err = counter.CountReady(ctx, filter)
			return err
		}},
		{"ReadyClaimer.ClaimNext", func(s *Store) error {
			claimer, err := s.ReadyClaimer()
			if err != nil {
				t.Fatalf("ReadyClaimer(): %v", err)
			}
			_, err = claimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "ana", Filter: filter})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w := recordingStore(t)
			err := tc.call(s)
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) || unsup.Backend != Backend {
				t.Fatalf("%s with ExcludeParentsWithOpenChildren = %v, want *storage.ErrUnsupported for this backend", tc.name, err)
			}
			var inexpressible *InexpressibleError
			if !errors.As(err, &inexpressible) || inexpressible.Refused.Row.ID != row {
				t.Errorf("%s: the refusal does not cite %s: %v", tc.name, row, err)
			}
			if len(w.dispatched) != 0 {
				t.Errorf("%s: a refused request dialed %v", tc.name, w.dispatched)
			}
		})
	}
}
