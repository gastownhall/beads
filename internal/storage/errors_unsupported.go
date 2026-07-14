package storage

import "fmt"

// ErrUnsupported reports that a storage backend does not implement an
// operation. Typed per PROPOSAL-pluggable-storage-backends.md §4.0 error
// taxonomy; this is the embryo of the seam's ErrUnsupported{Op, Backend}.
type ErrUnsupported struct {
	Op      string // method name, e.g. "AddLabel" or "Transaction.CreateIssues"
	Backend string // e.g. "postgres"
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("operation %q not supported by the %s backend", e.Op, e.Backend)
}
