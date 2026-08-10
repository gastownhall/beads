//go:build js && wasm

package hooks

import (
	"errors"

	"github.com/steveyegge/beads/internal/types"
)

var errHookExecutionUnsupported = errors.New("hook execution is not supported on js/wasm")

func (*Runner) runHook(_ string, _ string, _ *types.Issue) error {
	return errHookExecutionUnsupported
}
