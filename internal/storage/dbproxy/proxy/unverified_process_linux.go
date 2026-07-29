//go:build linux

package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// unverifiedProcess holds one stable OS reference to a PID whose birth token
// cannot be verified, so force-stop inspection and signaling act on the same
// process. A pidfd pins the PID number against reuse for the lifetime of the
// handle; on kernels without pidfds (pre-5.3) the fallback keeps the
// historical small PID-reuse race.
type unverifiedProcess struct {
	pid   int
	pidfd int // -1 when the kernel has no pidfd support
}

// openUnverifiedProcess opens a stable handle for pid. gone reports a PID
// that no longer exists.
func openUnverifiedProcess(pid int) (proc *unverifiedProcess, gone bool, err error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err == nil {
		return &unverifiedProcess{pid: pid, pidfd: fd}, false, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return nil, true, nil
	}
	if errors.Is(err, unix.ENOSYS) {
		return &unverifiedProcess{pid: pid, pidfd: -1}, false, nil
	}
	return nil, false, fmt.Errorf("pidfd open %d: %w", pid, err)
}

func (p *unverifiedProcess) executableBasename() (basename string, gone bool, err error) {
	return processExecutableBasename(p.pid)
}

// commandLineContains reports whether the process command line contains
// needle. The managed proxy child is spawned as "db-proxy-child --root
// <rootDir>", so a workspace's own processes always match their root path.
func (p *unverifiedProcess) commandLineContains(needle string) (matched bool, gone bool, err error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/cmdline")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			return false, true, nil
		}
		return false, false, fmt.Errorf("read cmdline for pid %d: %w", p.pid, err)
	}
	if len(data) == 0 {
		// Zombies expose an empty cmdline; the process has effectively exited.
		return false, true, nil
	}
	cmdline := strings.ReplaceAll(strings.TrimRight(string(data), "\x00"), "\x00", " ")
	return strings.Contains(cmdline, needle), false, nil
}

// kill sends SIGKILL through the held handle. gone reports a target that had
// already exited.
func (p *unverifiedProcess) kill() (gone bool, err error) {
	if p.pidfd >= 0 {
		if err := unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0); err != nil {
			if errors.Is(err, unix.ESRCH) {
				return true, nil
			}
			return false, fmt.Errorf("pidfd signal %d: %w", p.pid, err)
		}
		return false, nil
	}
	if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return true, nil
		}
		return false, fmt.Errorf("signal pid %d: %w", p.pid, err)
	}
	return false, nil
}

// exited reports whether the process is gone (or reduced to a zombie). While
// the pidfd is held the PID cannot be recycled, so a /proc probe is stable.
func (p *unverifiedProcess) exited() (bool, error) {
	_, gone, err := processExecutableBasename(p.pid)
	if err != nil {
		return false, err
	}
	return gone, nil
}

func (p *unverifiedProcess) close() {
	if p.pidfd >= 0 {
		_ = unix.Close(p.pidfd)
		p.pidfd = -1
	}
}
