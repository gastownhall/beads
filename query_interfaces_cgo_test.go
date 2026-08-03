//go:build cgo

package beads_test

import (
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Compile-time proof that the embedded Dolt store also satisfies each narrow
// public interface (the server Dolt store is asserted in query_interfaces_test.go).
var (
	_ beads.IssueClaimer     = (*embeddeddolt.EmbeddedDoltStore)(nil)
	_ beads.EventQuerier     = (*embeddeddolt.EmbeddedDoltStore)(nil)
	_ beads.DependentQuerier = (*embeddeddolt.EmbeddedDoltStore)(nil)
	_ beads.BlockedQuerier   = (*embeddeddolt.EmbeddedDoltStore)(nil)
)
