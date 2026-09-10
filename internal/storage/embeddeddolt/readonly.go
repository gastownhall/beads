package embeddeddolt

import "errors"

// ErrReadOnly is returned when a write is attempted on a store opened
// read-only — OpenReadOnly, or OpenForPreviewCommand for a --dry-run/--inspect
// command.
//
// It is exported (and declared here, without a cgo build tag) because callers
// outside this package have to be able to tell "this store refuses writes by
// construction" apart from a real failure. The CLI's post-command
// tip-metadata write is the case that forced it: that write is incidental
// bookkeeping which read-only opens have always tolerated because
// OpenForReadOnlyCommand is deliberately writable, and a store that genuinely
// refuses it must not turn an otherwise successful command into a non-zero
// exit after the fact.
var ErrReadOnly = errors.New("embeddeddolt: store is read-only")
