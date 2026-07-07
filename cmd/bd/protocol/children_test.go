package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_ChildrenPrettyRendersTree asserts that the documented
// `bd children --pretty` example is accepted and uses the tree renderer.
func TestProtocol_ChildrenPrettyRendersTree(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	parent := w.create("--title", "Pretty parent", "--type", "epic")
	child := w.create("--title", "Pretty child", "--type", "task", "--parent", parent)

	out := w.run("children", parent, "--pretty")
	if !strings.Contains(out, parent) {
		t.Fatalf("bd children --pretty output should include parent %s:\n%s", parent, out)
	}
	if !strings.Contains(out, child) {
		t.Fatalf("bd children --pretty output should include child %s:\n%s", child, out)
	}
	if !strings.Contains(out, "Total:") {
		t.Fatalf("bd children --pretty should use the tree renderer summary:\n%s", out)
	}
}
