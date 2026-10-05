package doltserver

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// startedServer is a dolt sql-server child Start launched. A goroutine waits
// on it, which both reports its exit and reaps it: without that, a child that
// exits while bd is still running stays a zombie, and a zombie still answers
// kill(pid, 0), so Start could not see that its server had died.
type startedServer struct {
	pid    int
	proc   *os.Process
	exited chan struct{}
}

func launchServer(cmd *exec.Cmd) (*startedServer, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &startedServer{pid: cmd.Process.Pid, proc: cmd.Process, exited: make(chan struct{})}
	go func() {
		_, _ = s.proc.Wait()
		close(s.exited)
	}()
	return s, nil
}

func (s *startedServer) hasExited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// killExitWait bounds how long killAndWait waits for a killed child to exit.
const killExitWait = 5 * time.Second

// killAndWait kills the child (and, on unix, the process group it leads, so
// a dolt behind a wrapper that did not exec goes too) and waits briefly for
// it to exit. The wait matters before a retry: the next attempt opens the
// same database, and the killed child holds dolt's database lock until the
// kernel has finished its exit.
func (s *startedServer) killAndWait() {
	if s == nil {
		return
	}
	if !s.hasExited() {
		killProcessGroup(s.pid)
	}
	_ = s.proc.Kill()
	select {
	case <-s.exited:
	case <-time.After(killExitWait):
	}
}

// writeServerStateFiles records a launched server in the PID and port files.
func writeServerStateFiles(beadsDir string, pid, port int) error {
	if err := os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(pid)), 0600); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}
	if err := writePortFile(beadsDir, port); err != nil {
		_ = os.Remove(pidPath(beadsDir))
		return fmt.Errorf("writing port file: %w", err)
	}
	return nil
}

func removeServerStateFiles(beadsDir string) {
	_ = os.Remove(pidPath(beadsDir))
	_ = os.Remove(portPath(beadsDir))
}

// pinnedPortRemedy says why Start did not move off port, which another
// process holds, and what the operator can do, worded by where the port came
// from.
func pinnedPortRemedy(cfg *Config, beadsDir string, port int) string {
	switch {
	case cfg.PortSource == PortSourceSharedServerDefault:
		return fmt.Sprintf("port %d is the shared-server default port, which every project on this host dials, so bd will not move it: "+
			"free it, or configure a different shared-server port with BEADS_DOLT_SERVER_PORT or dolt.port", port)
	case cfg.PortSource == PortSourcePortFile:
		return fmt.Sprintf("port %d is the shared server's recorded port (%s), which other projects dial, so bd will not move it: "+
			"free it, or stop the shared server and delete that file", port, portPath(beadsDir))
	default:
		src := string(cfg.PortSource)
		for _, ps := range portSources {
			if ps.source == cfg.PortSource {
				src = ps.label
			}
		}
		switch cfg.PortSource {
		case PortSourceCallerExplicit:
			src = "an explicit port setting (e.g. bd init --server-port)"
		case PortSourceUnset:
			src = "an explicit setting"
		}
		return fmt.Sprintf("port %d comes from %s, which bd will not override: free it, or configure a different one with: bd dolt set port <port>", port, src)
	}
}

// startupProbe describes how awaitOwnedListener decides that the child it
// launched is the dolt sql-server answering on host:port.
type startupProbe struct {
	host string
	port int
	// logPath and logOffset locate the child's output: it writes straight to
	// the server log (it outlives bd, so it cannot write to a pipe into bd),
	// and everything after logOffset is this child's.
	logPath   string
	logOffset int64
	// readyLineLogged is true when the child's log level lets dolt log
	// DoltReadyLine (debug mode); the default warning level does not.
	readyLineLogged bool
	timeout         time.Duration
	// owner is listenerOwnership; tests replace it.
	owner func(pid, port int) (owned, known bool)
}

const (
	startupProbeDialTimeout = 500 * time.Millisecond
	startupProbeInterval    = 250 * time.Millisecond
)

// awaitOwnedListener waits until the dolt sql-server srv is accepting
// connections on its port.
//
// A MySQL greeting on the port is not enough on its own: the port is chosen
// before dolt binds it, and if another process (another dolt, say) takes it
// in between, the greeting comes from that process while this child logs
// "Port N already in use." and exits. dolt only reaches that check after its
// own startup, which under load takes longer than any fixed grace period, so
// a greeting only counts once the listener is shown to be the child's: by
// dolt's ready line in the child's output when its log level emits one, or
// else by the listening socket belonging to the child's process tree (Linux,
// via /proc). When the socket is shown to belong to someone else the wait
// ends at once with ErrPortInUse. Where neither proof is available the
// greeting decides, as before, unless the log level promises a ready line.
// Either way a child that exits, or says its port is taken, ends the wait,
// with ErrPortInUse in the latter case.
func awaitOwnedListener(srv *startedServer, p startupProbe) error {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	owner := p.owner
	if owner == nil {
		owner = listenerOwnership
	}
	watch := NewStartupWatch(nil, p.port)
	tail := logTail{path: p.logPath, off: p.logOffset}
	deadline := time.Now().Add(p.timeout)
	answeredUnproven := false
	for {
		if err := childStartupFailure(srv, &tail, watch, addr); err != nil {
			return err
		}
		greeted, _ := ProbeSQLServer("tcp", addr, startupProbeDialTimeout) //nolint:gosec // G704: addr is built from internal host+port, not user input
		if greeted {
			// Re-read the output before judging: a child that lost the port
			// may have said so (and been reaped) since the check above.
			if err := childStartupFailure(srv, &tail, watch, addr); err != nil {
				return err
			}
			owned, known := watch.IsReady(), true
			if !owned {
				owned, known = owner(srv.pid, p.port)
			}
			switch {
			case owned || (!known && !p.readyLineLogged):
				if err := childStartupFailure(srv, &tail, watch, addr); err != nil {
					return err
				}
				return nil
			case known:
				// Proven: the listener belongs to another process. The child
				// cannot bind this port; do not wait for it to find out.
				return fmt.Errorf("%w: another process (not dolt sql-server PID %d) holds %s", ErrPortInUse, srv.pid, addr)
			default:
				answeredUnproven = true
			}
		}
		if time.Now().After(deadline) {
			return startupTimeout(p, addr, srv.pid, answeredUnproven)
		}
		wait := startupProbeInterval
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		select {
		case <-srv.exited:
		case <-time.After(wait):
			if time.Now().After(deadline) {
				return startupTimeout(p, addr, srv.pid, answeredUnproven)
			}
		}
	}
}

func startupTimeout(p startupProbe, addr string, pid int, answeredUnproven bool) error {
	if answeredUnproven {
		return fmt.Errorf("timeout after %s: something answered at %s, but dolt sql-server (PID %d) never logged %q, "+
			"which proves the listener is its own at log level debug; if a wrapper filters dolt's output, turn off debug mode (BEADS_DOLT_DEBUG, dolt.debug)",
			p.timeout, addr, pid, DoltReadyLine)
	}
	return fmt.Errorf("timeout after %s waiting for server at %s", p.timeout, addr)
}

// childStartupFailure reads the child's latest output and returns why srv
// cannot become ready, or nil while it still may. The output is read every
// time, not only once the exit has been observed: the reaper goroutine can
// lag the child's death, and the port-in-use line is already in the log.
func childStartupFailure(srv *startedServer, tail *logTail, watch *StartupWatch, addr string) error {
	exited := srv.hasExited()
	tail.feed(watch)
	if watch.SawPortInUse() {
		return fmt.Errorf("%w: dolt sql-server (PID %d) could not bind %s", ErrPortInUse, srv.pid, addr)
	}
	if exited {
		return errors.New("dolt sql-server (PID " + strconv.Itoa(srv.pid) + ") exited before accepting connections")
	}
	return nil
}

// logTail reads what has been appended to a file since off.
type logTail struct {
	path string
	off  int64
}

func (t *logTail) feed(w io.Writer) {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() < t.off {
		// Truncated underneath us (an external copytruncate rotation):
		// everything now in the file is new.
		t.off = 0
	}
	n, _ := io.Copy(w, io.NewSectionReader(f, t.off, 1<<62))
	t.off += n
}

// logSize returns the current size of f, the offset a newly launched child's
// output will start at.
func logSize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}
