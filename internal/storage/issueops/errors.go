package issueops

import "fmt"

// ErrTooManyRows is returned by SearchIssuesInTx (and equivalent paths in
// other backends) when a search would yield more rows than the caller's
// MaxRows cap allows. Callers can match it with errors.As to surface a
// structured "result set too large" condition instead of an opaque error
// string.
//
// Found is the row count observed when the cap fired. The storage layer
// issues LIMIT MaxRows+1 to detect overage, so Found equals MaxRows+1 in
// practice; the true row count in the underlying data may be larger.
//
// Source attributes which knob set MaxRows. Expected values: "--max-rows",
// "BEADS_MAX_ROWS", or "" (library users with no source attribution).
type ErrTooManyRows struct {
	Found  int
	Cap    int
	Source string
}

func (e *ErrTooManyRows) Error() string {
	if e.Source != "" {
		return fmt.Sprintf("search returned %d rows, exceeding %s cap of %d", e.Found, e.Source, e.Cap)
	}
	return fmt.Sprintf("search returned %d rows, exceeding cap of %d", e.Found, e.Cap)
}
