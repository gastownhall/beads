package proxy

import (
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// Status is a point-in-time description of the process tree under a proxied
// root: the proxy bd connects through, and the dolt backend the proxy
// supervises on a managed-local topology.
type Status struct {
	ProxyRunning bool
	ProxyPID     int
	ProxyPort    int
	// BackendRunning describes the proxy-child record only. An external
	// proxied topology never writes one — bd spawns no dolt there — so a
	// false value means "bd supervises no backend here", which is not the
	// same claim as "the database is down". Callers that can tell the
	// topologies apart must say which one they are reporting.
	BackendRunning bool
	BackendPID     int
	BackendPort    int
}

// ReadStatus answers what is running under rootDir without starting,
// stopping, or adopting anything: it reads the two pid records and verifies
// the recorded processes are still the ones that wrote them. Safe for
// diagnostics, which is the whole point — a reporting command must not have
// lifecycle side effects.
func ReadStatus(rootDir string) Status {
	var st Status
	if discovery := readAndDial(rootDir); discovery.status == adoptionAdopted {
		st.ProxyRunning = true
		st.ProxyPID = discovery.pidfile.Pid
		st.ProxyPort = discovery.pidfile.Port
	}
	st.BackendRunning, st.BackendPID, st.BackendPort = readBackendRecord(rootDir)
	return st
}

// readBackendRecord verifies the supervised dolt backend from its pidfile.
// Unlike the proxy there is no control channel to interrogate — the backend is
// a plain dolt sql-server — so identity stops at the birth token. That still
// rejects a recycled pid, which a bare liveness check would not, and it is the
// same first gate the proxy's own adoption path applies. A record that fails
// any check reports nothing rather than a pid a caller might act on: a stale
// record's pid is precisely the one that must not be signaled.
func readBackendRecord(rootDir string) (running bool, pid int, port int) {
	pf, err := pidfile.Read(rootDir, server.PIDFileName)
	if err != nil || pf == nil {
		return false, 0, 0
	}
	if err := pf.ValidateV2(pidfile.KindDoltBackend); err != nil {
		return false, 0, 0
	}
	matched, err := verifyProcessIdentity(pf.Pid, procid.Token(pf.Birth))
	if err != nil || !matched {
		return false, 0, 0
	}
	return true, pf.Pid, pf.Port
}
