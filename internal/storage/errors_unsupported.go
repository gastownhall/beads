package storage

import "github.com/steveyegge/beads/issueops"

// ErrUnsupported reports an operation unavailable on a storage backend. It is
// declared and documented by the public contract package,
// github.com/steveyegge/beads/issueops, so that a caller holding only a role
// interface can classify a refusal without importing this package. This is the
// SAME type, so every storage.ErrUnsupported reference and every errors.As site
// keeps matching.
type ErrUnsupported = issueops.ErrUnsupported
