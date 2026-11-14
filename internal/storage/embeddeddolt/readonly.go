package embeddeddolt

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

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
// error, when the embedded engine refuses to open a store because the process
// may not write its files: permission denied (EACCES/EPERM, Windows
// ERROR_ACCESS_DENIED) or a read-only filesystem (EROFS). The embedded engine
// opens its files for writing on every open, including OpenReadOnly and
// OpenForPreviewCommand (GH#7445), so that refusal is permanent until the
// process's access to the store changes: a caller must not retry it as lock
// contention.
//
// Exported, and declared here without a cgo build tag, for the same reason as
// ErrReadOnly: callers outside this package have to tell it apart from a
// transient open failure.
var ErrStoreRequiresWriteAccess = errors.New("embeddeddolt: store requires write access to open")

// writeAccessGuidance is the remedy classifyOpenError attaches to a refused
// engine open.
const writeAccessGuidance = "embedded stores require write access even for --readonly; " +
	"retrying will not help until access changes; " +
	"run where the process has read/write access to the store"

// classifyOpenError returns err, from the embedded engine's initial connect
// to the data directory dir, wrapped with ErrStoreRequiresWriteAccess and
// writeAccessGuidance when it is a permission or read-only-filesystem refusal,
// and err unchanged otherwise (nil included). The original error stays
// reachable through errors.Is and errors.As. Classification reads only the
// error chain: no filesystem probe and no match on the error's text.
func classifyOpenError(dir string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.EROFS) {
		return err
	}
	return fmt.Errorf("%w at %s (%s): %w",
		ErrStoreRequiresWriteAccess, remedy.PrintedValue(dir), writeAccessGuidance, remedy.PrintedError(err))
}
