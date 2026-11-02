package schema

import (
	"context"
	"testing"
)

// stubUpdatedAtGuard turns the runner's updated_at snapshot into a no-op for
// runner fakes (mockDB and kin) that cannot answer its queries, the way those
// tests stub issueRowCounter. Tests of the guard itself never use it.
func stubUpdatedAtGuard(t *testing.T) {
	t.Helper()
	orig := snapshotUpdatedAtForStep
	snapshotUpdatedAtForStep = func(context.Context, DBConn, migrationSource, int) (*updatedAtSnapshot, error) {
		return nil, nil
	}
	t.Cleanup(func() { snapshotUpdatedAtForStep = orig })
}
