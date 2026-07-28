package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// epochAdvancingServer simulates the F4 race deterministically: `bd dolt
// stop` advancing the stop epoch while the proxy child is inside its (slow)
// backend Start, after the spawn marker has already been cleared under
// proxy.lock.
type epochAdvancingServer struct {
	*server.TestDatabaseServerImpl
	rootDir string
	t       *testing.T
}

func (s *epochAdvancingServer) Start(ctx context.Context) error {
	if err := s.TestDatabaseServerImpl.Start(ctx); err != nil {
		return err
	}
	require.NoError(s.t, advanceStopEpoch(s.rootDir))
	return nil
}

// TestListenAndServe_StopEpochAdvanceDuringStartupAbortsPublish asserts that
// a stop which begins after the child took proxy.lock (and cleared the spawn
// marker) but before proxy.pid is written makes the child abort instead of
// publishing a running proxy after the stop returned.
func TestListenAndServe_StopEpochAdvanceDuringStartupAbortsPublish(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)

	ts := server.New()
	backend := &epochAdvancingServer{TestDatabaseServerImpl: ts, rootDir: root, t: t}

	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    backend,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = p.ListenAndServe(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStartInterrupted)

	pf, readErr := pidfile.Read(root, PIDFileName)
	require.NoError(t, readErr)
	assert.Nil(t, pf, "proxy.pid must not be published after the stop epoch advanced")

	counters := ts.Snapshot()
	assert.Equal(t, int64(1), counters.StartCalls)
	assert.Equal(t, int64(1), counters.StopCalls, "aborting the publish must still stop the backend")
}
