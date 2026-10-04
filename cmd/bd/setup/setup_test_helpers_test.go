package setup

import (
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

// stubDetectRenderOpts overrides detectRenderOptsImpl to return
// DefaultRenderOpts (HasRemote=true), matching what agents.RenderSection()
// produces. This prevents hash mismatches in tests where no beads config exists.
func stubDetectRenderOpts(t *testing.T) {
	t.Helper()
	orig := detectRenderOptsImpl
	detectRenderOptsImpl = func() agents.RenderOpts { return agents.DefaultRenderOpts() }
	t.Cleanup(func() { detectRenderOptsImpl = orig })
}
