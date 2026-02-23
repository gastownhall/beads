package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_DeferPastDateWarns asserts that bd defer --until with a past
// date warns the user. The bd update --defer path already warns; bd defer
// should be consistent.
func TestProtocol_DeferPastDateWarns(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Defer target", "--type", "task")

	out, _ := w.tryRun("defer", id, "--until=2020-01-01")
	if !strings.Contains(out, "past") {
		t.Errorf("bd defer --until=<past date> should warn about past date:\n%s", out)
	}
}
