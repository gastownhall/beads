package dolt

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestIsMissingFederationPeer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain not found", storage.ErrNotFound, true},
		{"wrapped not found", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound), true},
		{"double wrap", fmt.Errorf("failed to get peer credentials: %w", fmt.Errorf("%w: federation peer origin", storage.ErrNotFound)), true},
		{"other error", errors.New("decrypt failed"), false},
		{"sql-ish other", fmt.Errorf("failed to get federation peer: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingFederationPeer(tc.err); got != tc.want {
				t.Fatalf("isMissingFederationPeer(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
