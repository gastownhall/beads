package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// Lenient opens may tolerate a saved-cursor deferral, but must not lose a
// simultaneous lock-release failure returned by MigrateUpWithLock.
func TestLenientOpenCursorDeferralPreservesLockErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deferred restoration", schema.ErrIgnoredCursorRestoreDeferred, true},
		{"failed lock release", errors.Join(schema.ErrIgnoredCursorRestoreDeferred, schema.ErrMigrationLockRelease), false},
		{"unrelated failure", errors.New("connection lost"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := warnLenientOpenRefusal(tc.err); got != tc.want {
				t.Errorf("lenient open accepted=%v, want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}
