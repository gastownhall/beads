package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

const (
	shutdownConfirmDeadline = 5 * time.Second
	shutdownConfirmPoll     = 50 * time.Millisecond
)

// Shutdown forcibly terminates the proxy and its child dolt sql-server for the
// given rootDir. Intended for test cleanup: guarantees no proxy or dolt
// sql-server process is left running against rootDir when this returns nil.
//
// For each (lock, pid) pair — proxy-child first, then proxy — it tries to
// acquire the flock. If the flock is held, it reads the pidfile, sends SIGKILL
// to the recorded PID, then polls the flock until it can be acquired (the OS
// releases flocks on process exit). The pidfile is removed on success.
func Shutdown(rootDir string) error {
	if err := shutdownPair(rootDir, server.LockFileName, server.PIDFileName); err != nil {
		return fmt.Errorf("proxy.Shutdown: dolt sql-server: %w", err)
	}
	if err := shutdownPair(rootDir, LockFileName, PIDFileName); err != nil {
		return fmt.Errorf("proxy.Shutdown: proxy: %w", err)
	}
	return nil
}

func shutdownPair(rootDir, lockName, pidName string) error {
	lockPath := filepath.Join(rootDir, lockName)

	if l, err := util.TryLock(lockPath); err == nil {
		l.Unlock()
		_ = pidfile.Remove(rootDir, pidName)
		return nil
	} else if !lockfile.IsLocked(err) {
		return fmt.Errorf("probe %s: %w", lockName, err)
	}

	if pf, err := pidfile.Read(rootDir, pidName); err == nil && pf != nil {
		if proc, ferr := os.FindProcess(pf.Pid); ferr == nil {
			_ = proc.Kill()
		}
	}

	deadline := time.Now().Add(shutdownConfirmDeadline)
	for {
		l, err := util.TryLock(lockPath)
		if err == nil {
			l.Unlock()
			_ = pidfile.Remove(rootDir, pidName)
			return nil
		}
		if !lockfile.IsLocked(err) {
			return fmt.Errorf("reacquire %s: %w", lockName, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout (%s) confirming death of process holding %s", shutdownConfirmDeadline, lockName)
		}
		time.Sleep(shutdownConfirmPoll)
	}
}
