// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/watch.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"fmt"
)

// The flag-mode refusals this backend raises in the user's own vocabulary.
//
// D7 puts the three taxonomy texts at the CLI, and rightly: they name commands
// and flags, and a store method name must never reach a user. What lives HERE is
// the text itself plus the sentinel that carries it, for one reason — the
// decision is the backend's. `--watch` is refused because of what polling a
// shared server costs the server, which is a fact about this backend and not
// about cobra, so the wrapper that fires before RunE should read the sentence
// off the backend rather than keep a second copy of it.
//
// The early-refusal wrapper itself is cmd/bd wiring and lands with the
// activation bead; this is the half it calls.

// ErrWatchUnsupported reports that a watch loop was asked for against an http
// workspace. It is the class sentinel, so a call site can classify without
// matching text.
var ErrWatchUnsupported = errors.New("watch mode is not supported against an http workspace")

// WatchRefusedError is D10's refusal, carrying what its text names.
type WatchRefusedError struct {
	// Command is the user's spelling of the command whose watch loop was asked
	// for — "bd list", "bd show". It is the command, never a method.
	Command string
	// ServerURL is the workspace's server, named because the refusal is about
	// THAT server's load and an operator needs to know which one.
	ServerURL string
}

// Error is byte-pinned by TestTheWatchRefusalReadsExactlyAsTheSpecWroteIt. The
// standard error path prefixes "Error: ", so this is the sentence after it.
//
// It states the reason rather than only the refusal because the reason is the
// whole decision: the loops poll every two seconds, each `bd list` tick is a
// full cursor walk, per-tick errors either spam or vanish silently, the watch
// display's dependency decoration is refused anyway, and N watching agents
// against one server is a request storm its semaphore answers with 503s the loop
// does not back off from. Serving watch honestly needs a change-detection
// primitive on the wire, which is the revisit trigger.
func (e *WatchRefusedError) Error() string {
	return fmt.Sprintf(
		"--watch is not supported against HTTP workspace %s: polling a shared server every 2s amplifies load without change detection; run %s without --watch, or watch in a local workspace",
		e.ServerURL, e.Command)
}

func (e *WatchRefusedError) Unwrap() error { return ErrWatchUnsupported }

// RefuseWatch builds the refusal for one command against this store's server.
//
// It takes the command's user-facing spelling because the store cannot know it:
// `bd list --watch` and `bd show --watch` reach the same backend and the text
// names the one the user typed.
func (s *Store) RefuseWatch(command string) error {
	e := &WatchRefusedError{Command: command}
	if s != nil {
		e.ServerURL = s.target.String()
	}
	return e
}
