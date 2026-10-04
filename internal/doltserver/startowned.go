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

func (s *startedServer) kill() {
	_ = s.proc.Kill()
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
// via /proc). Where neither is available the greeting decides, as before.
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
	answeredByOther := false
	for {
		tail.feed(watch)
		if err := childStartupFailure(srv, &tail, watch, addr); err != nil {
			return err
		}
		greeted, _ := ProbeSQLServer("tcp", addr, startupProbeDialTimeout) //nolint:gosec // G704: addr is built from internal host+port, not user input
		if greeted {
			tail.feed(watch)
			owned, known := watch.IsReady(), true
			if !owned {
				owned, known = owner(srv.pid, p.port)
			}
			if owned || (!known && !p.readyLineLogged) {
				if err := childStartupFailure(srv, &tail, watch, addr); err != nil {
					return err
				}
				return nil
			}
			answeredByOther = true
		}
		if time.Now().After(deadline) {
			if answeredByOther {
				return fmt.Errorf("timeout after %s: something answered at %s, but not the dolt sql-server bd started (PID %d)", p.timeout, addr, srv.pid)
			}
			return fmt.Errorf("timeout after %s waiting for server at %s", p.timeout, addr)
		}
		select {
		case <-srv.exited:
		case <-time.After(startupProbeInterval):
		}
	}
}

// childStartupFailure returns why srv cannot become ready, or nil while it
// still may.
func childStartupFailure(srv *startedServer, tail *logTail, watch *StartupWatch, addr string) error {
	exited := srv.hasExited()
	if exited {
		// The child has exited, so its output is complete.
		tail.feed(watch)
	}
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
