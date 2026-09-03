package proxy

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
)

// Adoption must be proven by the authenticated control-port identity reply
// alone. A dial-and-close probe of the DATA port is not free: the proxy dials
// its backend for every accepted client connection before any bytes flow, so
// such a probe burns one full MySQL session on the shared dolt server per bd
// invocation (wy-s8ytnw). This pins that readAndDial adopts a live proxy
// without ever connecting to its data port.
func TestReadAndDialAdoptsWithoutDialingDataPort(t *testing.T) {
	root := t.TempDir()
	token, err := procid.Capture(os.Getpid())
	require.NoError(t, err)
	rootID, err := identity.RootID(root)
	require.NoError(t, err)

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dataListener.Close() })
	dataPort := dataListener.Addr().(*net.TCPAddr).Port

	var dataAccepts atomic.Int32
	go func() {
		for {
			conn, acceptErr := dataListener.Accept()
			if acceptErr != nil {
				return
			}
			dataAccepts.Add(1)
			_ = conn.Close()
		}
	}()

	_, err = identity.WriteSecret(root)
	require.NoError(t, err)
	var reply identity.IdentReply
	var replyMu sync.RWMutex
	control, err := startControl(root, func() identity.IdentReply {
		replyMu.RLock()
		defer replyMu.RUnlock()
		return reply
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	replyMu.Lock()
	reply = identity.IdentReply{
		Schema:      pidfile.SchemaV2,
		Role:        pidfile.KindProxy,
		RootID:      rootID,
		UpstreamID:  "upstream",
		PID:         os.Getpid(),
		Birth:       string(token),
		DataPort:    dataPort,
		ControlPort: control.Port(),
	}
	replyMu.Unlock()
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
		Schema:      pidfile.SchemaV2,
		Kind:        pidfile.KindProxy,
		Pid:         os.Getpid(),
		Birth:       string(token),
		Port:        dataPort,
		ControlPort: control.Port(),
		RootID:      rootID,
		UpstreamID:  "upstream",
	}))

	got := readAndDial(root)
	require.Equal(t, adoptionAdopted, got.status)
	assert.Equal(t, dataPort, got.endpoint.Port)

	// A probe would have been accepted synchronously with the dial; give the
	// accept goroutine a moment anyway so a regression cannot hide behind
	// scheduling.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), dataAccepts.Load(), "readAndDial must not open a connection to the proxy data port")
}
