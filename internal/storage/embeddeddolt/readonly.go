package embeddeddolt

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/steveyegge/beads/internal/remedy"
)

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

// ErrNoEmbeddedDatabase is wrapped, together with the failed stat's own error,
// by OpenReadOnly and OpenForPreviewCommand when beadsDir has no embedded data
// directory.
//
// Exported, and declared here without a cgo build tag, for the same reason as
// ErrReadOnly: the CLI's pre-open workspace identity check has to tell "no
// local database yet" — a first run, which it skips — apart from every other
// failed preview open, which it refuses. errors.Is(err, os.ErrNotExist) alone
// cannot: a dial to a unix socket nothing listens on satisfies it too.
var ErrNoEmbeddedDatabase = errors.New("embeddeddolt: no embedded database")

// ErrStoreRequiresWriteAccess is wrapped, together with the engine's own
// access error, into the open failure every embedded open returns when the
// filesystem refuses the engine write access to its files — read-only opens
// included: the embedded Dolt engine opens its lock and chunk-journal files
// for writing on every open, read command or not, so a store the process
// cannot write cannot be opened at all, not even for --readonly (GH#7445).
//
// Exported, and declared here without a cgo build tag, for the same reason
// as ErrReadOnly and ErrNoEmbeddedDatabase: callers have to tell "this
// store can never be opened here until access to it changes" apart from a
// transient open failure, and stop retrying it. errors.Is still reaches the
// permission-denied cause through the wrap, for diagnosis.
var ErrStoreRequiresWriteAccess = errors.New("embeddeddolt: store requires write access")

// classifyOpenError explains an embedded engine-open failure the filesystem
// refused with a permission error: that refusal is permanent — no retry can
// succeed until the process's access to dir changes, so the error says so.
// Matching goes by errno (errors.Is against fs.ErrPermission, which covers
// Unix EACCES/EPERM and Windows ERROR_ACCESS_DENIED alike), never by message
// text, and the original error is preserved for errors.Is/errors.As: a
// permission errno does not prove which particular file operation was
// refused — it can also reflect an unreadable path or an ACL — only that
// access was denied, so the cause is kept for diagnosis rather than guessed
// at.
//
// The error is returned unchanged when it is nil or carries no permission
// errno, so only the engine-open boundary classifies (OpenSQL's initial
// ping): cleanup failures and later SQL errors keep their own meaning.
func classifyOpenError(dir string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return fmt.Errorf("%w: embedded stores require write access even for --readonly; retrying will not help until access changes; run where the process has read/write access to the store (%s): %w",
		ErrStoreRequiresWriteAccess,
		remedy.PrintedValue(dir),
		remedy.PrintedError(err),
	)
}
