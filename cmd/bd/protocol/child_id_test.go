package protocol

import (
	"fmt"
	"strings"
	"testing"
)

// TestProtocol_ChildIDCounterNeverReusesNumbers pins protocol clause D2.3: the
// per-parent child-id counter is monotonic — numbers are never reused, even
// after the child holding a number is closed or deleted. Reuse would resurrect
// a retired id: a stale reference (a commit message, an archived JSONL line, a
// worker's claim) would silently point at a different issue.
func TestProtocol_ChildIDCounterNeverReusesNumbers(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	parent := w.create("--title", "Counter epic", "--type", "epic")

	c1 := w.create("--title", "child one", "--parent", parent)
	c2 := w.create("--title", "child two", "--parent", parent)
	c3 := w.create("--title", "child three", "--parent", parent)

	for i, got := range []string{c1, c2, c3} {
		want := fmt.Sprintf("%s.%d", parent, i+1)
		if got != want {
			t.Fatalf("D2.3: child %d minted id %q, want %q", i+1, got, want)
		}
	}

	// Delete the highest child: the counter must NOT rewind to it.
	w.run("delete", c3, "--force")
	if _, err := w.tryRun("show", c3, "--json"); err == nil {
		t.Fatalf("D2.3 setup: %s still exists after `bd delete --force`", c3)
	}

	c4 := w.create("--title", "child four", "--parent", parent)
	if c4 != parent+".4" {
		t.Errorf("D2.3: after deleting %s, the next child minted %q — want %s.4 (the counter must never reuse a number)", c3, c4, parent)
	}

	// Closing a child must not rewind the counter either.
	w.run("close", c4, "--reason", "done")
	c5 := w.create("--title", "child five", "--parent", parent)
	if c5 != parent+".5" {
		t.Errorf("D2.3: after closing %s, the next child minted %q — want %s.5", c4, c5, parent)
	}

	// Deleting the whole live set must still not rewind: the counter is per
	// parent and persists independently of which children currently exist.
	w.run("delete", c1, c2, c5, "--force")
	c6 := w.create("--title", "child six", "--parent", parent)
	if c6 != parent+".6" {
		t.Errorf("D2.3: after deleting every remaining child, the next child minted %q — want %s.6", c6, parent)
	}

	// Sanity: a dotted child id is <parent>.<N>, not a fresh hash id (J7.2).
	if !strings.HasPrefix(c6, parent+".") {
		t.Errorf("D2.3/J7.2: child id %q is not a dotted child of %q", c6, parent)
	}
}
