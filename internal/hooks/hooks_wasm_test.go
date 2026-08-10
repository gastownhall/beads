//go:build js && wasm

package hooks

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRunHookReportsUnsupportedExecution(t *testing.T) {
	runner := NewRunner(t.TempDir())
	err := runner.runHook(
		"not-executable-on-wasm",
		EventCreate,
		&types.Issue{ID: "wasm-test"},
	)
	if !errors.Is(err, errHookExecutionUnsupported) {
		t.Fatalf("runHook error = %v, want %v", err, errHookExecutionUnsupported)
	}
}
