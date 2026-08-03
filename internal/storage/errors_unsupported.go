package storage

import "fmt"

// ErrUnsupported reports an operation unavailable on a storage backend.
type ErrUnsupported struct {
	Op      string // method name, e.g. "AddLabel" or "Transaction.CreateIssues"
	Backend string // e.g. "sqlite"
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("operation %q not supported by the %s backend", e.Op, e.Backend)
}
