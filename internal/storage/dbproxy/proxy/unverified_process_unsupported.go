//go:build !linux && !darwin && !windows

package proxy

import (
	"fmt"
	"runtime"
)

// errUnverifiedUnsupported mirrors procid.ErrUnsupported for the force-stop
// path: on platforms with no process-inspection implementation the proxy
// machinery refuses cleanly rather than guessing at a PID's identity.
var errUnverifiedUnsupported = fmt.Errorf(
	"dbproxy: unverified-process inspection is not implemented on %s", runtime.GOOS)

type unverifiedProcess struct{}

func openUnverifiedProcess(pid int) (proc *unverifiedProcess, gone bool, err error) {
	return nil, false, errUnverifiedUnsupported
}

func (p *unverifiedProcess) executableBasename() (basename string, gone bool, err error) {
	return "", false, errUnverifiedUnsupported
}

func (p *unverifiedProcess) commandLineContains(needle string) (matched bool, gone bool, err error) {
	return false, false, errUnverifiedUnsupported
}

func (p *unverifiedProcess) kill() (gone bool, err error) {
	return false, errUnverifiedUnsupported
}

func (p *unverifiedProcess) exited() (bool, error) {
	return false, errUnverifiedUnsupported
}

func (p *unverifiedProcess) close() {}
