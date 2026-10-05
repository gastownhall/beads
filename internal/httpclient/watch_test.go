// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/watch_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"strings"
	"testing"
)

// TestTheWatchRefusalReadsExactlyAsTheSpecWroteIt is the byte pin D10 asks for.
//
// The text is the decision, not a description of one: it names the flag, the
// workspace, the cost that made watch unservable, and BOTH recoveries. A
// refusal that said only "not supported" would send an operator to a bug
// tracker, and one that dropped the local-workspace recovery would leave them
// with no way to get the behavior at all.
func TestTheWatchRefusalReadsExactlyAsTheSpecWroteIt(t *testing.T) {
	s := New(testTarget(t), nil, nil)

	const want = "--watch is not supported against HTTP workspace http://127.0.0.1:7777: " +
		"polling a shared server every 2s amplifies load without change detection; " +
		"run bd list without --watch, or watch in a local workspace"

	err := s.RefuseWatch("bd list")
	if got := err.Error(); got != want {
		t.Errorf("refusal text:\n got %q\nwant %q", got, want)
	}
	if !errors.Is(err, ErrWatchUnsupported) {
		t.Error("the refusal does not classify as ErrWatchUnsupported")
	}

	// The command is the user's spelling, so the second front door names itself.
	if got := s.RefuseWatch("bd show").Error(); !strings.Contains(got, "run bd show without --watch") {
		t.Errorf("bd show's refusal names the wrong command: %q", got)
	}
}

// TestTheWatchRefusalNamesNoStoreMethod is D7's choke point applied to this
// text: a refusal a user reads must speak flags and commands, never the Go
// method the dispatch happened to land on.
func TestTheWatchRefusalNamesNoStoreMethod(t *testing.T) {
	text := New(testTarget(t), nil, nil).RefuseWatch("bd list").Error()
	for name := range legitimatelyUnsupported {
		if strings.Contains(text, name) {
			t.Errorf("the watch refusal names the store method %q", name)
		}
	}
}
