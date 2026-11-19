package proxiedopen

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// ErrProxyUnavailable reports that a library open found no bd proxy it can
// use for the workspace's server root: none is running and no bd executable
// is there to start one, or the one running was started by a bd older than
// 1.4, which the library cannot talk to and does not stop
// (proxy.ErrPreUpgradeProxyRunning). The refused open creates no embedded
// store and starts or stops no proxy. The bd CLI never returns it: bd starts
// the proxy from its own executable and replaces an older bd's proxy.
var ErrProxyUnavailable = errors.New("no bd proxy this library can use is running for this workspace, and none can be started")

// BDExecutableOnPath is the Options.ProxyExecutable a library open uses: it
// resolves `bd` on PATH, so an embedder that needs a particular build puts it
// first on PATH. It is consulted only when no proxy is running (a running one
// is adopted without it). When PATH has no bd it fails with an error wrapping
// both ErrProxyUnavailable and proxy.ErrNoProxyExecutable, which makes the
// open fail at once; it never falls back to the host executable, which is not
// bd and has no db-proxy-child command.
func BDExecutableOnPath(beadsDir string) func() (string, error) {
	return func() (string, error) {
		path, err := exec.LookPath("bd")
		if err == nil {
			return path, nil
		}
		return "", fmt.Errorf("%w: %w: looking up bd on PATH: %w; run any bd command in %s to start the proxy, or install bd on PATH",
			ErrProxyUnavailable, proxy.ErrNoProxyExecutable, err, filepath.Dir(beadsDir))
	}
}

// asProxyUnavailable makes a library host's refusal of a pre-upgrade proxy
// (proxy.ErrPreUpgradeProxyRunning, raised only when Options.ProxyExecutable
// is set) match ErrProxyUnavailable too, so an embedder matches one sentinel
// for every open that found no proxy it can use. Other errors pass through
// unchanged.
func asProxyUnavailable(err error) error {
	if errors.Is(err, proxy.ErrPreUpgradeProxyRunning) && !errors.Is(err, ErrProxyUnavailable) {
		return fmt.Errorf("%w: %w", ErrProxyUnavailable, err)
	}
	return err
}
