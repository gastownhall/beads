// Idle watch: bd stops an auto-started dolt sql-server after it has been
// idle for dolt.idle-timeout (GH#4282). A small watcher process — spawned by
// EnsureRunningDetailed for servers bd itself auto-started, never for an
// explicit `bd dolt start` — polls the server's global status counters over
// the MySQL protocol and, when nothing has connected and no bd store holds the
// workspace open for the whole window, stops the server through the same
// stopLocked path `bd dolt stop` uses. The next bd command auto-starts a fresh
// server exactly as before, so the cost of a reap is one startup.
//
// All watch state lives under <serverDir>/dolt-server.activity/, a name the
// shipped .gitignore template and doctor already require: watcher.pid names the
// watching process (a `bd dolt start` that adopts the server removes it, which
// is how the watcher learns to stand down), reaping marks an in-flight reap,
// and client-<pid>-<nonce> files are leases held by open bd stores (see
// AcquireClientLease). The directory also replaces the regular file of the
// same name the pre-2452 idle monitor left behind.

package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DefaultIdleTimeout is the window after which an idle auto-started server is
// stopped, when neither dolt.idle-timeout nor BEADS_DOLT_IDLE_TIMEOUT names
// one. Short enough to reclaim the ~2 GB of an abandoned server the same day,
// long enough that an unattended-but-in-use workspace (a long rebase, a
// paused editor session) is not restarted under its user.
const DefaultIdleTimeout = 30 * time.Minute

// State file names under <serverDir>/dolt-server.activity/.
const (
	ActivityDirName   = "dolt-server.activity"
	watcherPIDName    = "watcher.pid"
	reapingName       = "reaping"
	clientLeasePrefix = "client-"
)

func activityDir(serverDir string) string { return filepath.Join(serverDir, ActivityDirName) }
func watcherPIDPath(serverDir string) string {
	return filepath.Join(activityDir(serverDir), watcherPIDName)
}
func reapingPath(serverDir string) string { return filepath.Join(activityDir(serverDir), reapingName) }
func leasePathFor(serverDir, name string) string {
	return filepath.Join(activityDir(serverDir), name)
}

// ensureActivityDir creates the activity directory, replacing a leftover
// regular file of the same name (written by the old idle monitor, removed in
// GH#2452) so the watch state can live in it.
func ensureActivityDir(serverDir string) error {
	dir := activityDir(serverDir)
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("replacing stale %s file: %w", ActivityDirName, err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return nil
}

// IdleWatchOpts names the server one watcher process watches. The spawning bd
// resolves Timeout (see ResolveIdleTimeout) so the watcher itself reads no
// configuration.
type IdleWatchOpts struct {
	// ServerDir is the directory holding the server's state files (pid,
	// port, activity). This is beadsDir in per-project mode.
	ServerDir string
	// PID is the watched dolt sql-server process.
	PID int
	// Host and Port are the server's MySQL listener.
	Host string
	Port int
	// Timeout is the idle window; 0 or less never stops anything.
	Timeout time.Duration
}

// IdleWatchSpawnerFunc launches an idle watcher for opts. The CLI sets it to
// re-exec bd's hidden dolt-idle-watch command; test binaries and SDK embedders
// leave it unset and keep today's never-reap behavior.
type IdleWatchSpawnerFunc func(opts IdleWatchOpts)

// idleWatchSpawner is the installed hook; see SetIdleWatchSpawner.
var idleWatchSpawner IdleWatchSpawnerFunc

// SetIdleWatchSpawner installs the hook EnsureRunningDetailed uses to launch
// the idle watcher for a server it auto-started. Call with nil to remove it.
func SetIdleWatchSpawner(spawner IdleWatchSpawnerFunc) {
	idleWatchSpawner = spawner
}

// idleWatchSpawnerSet reports whether a spawner is installed, for
// TestIdleWatchEligibility's sake as much as the spawn decision's.
func idleWatchSpawnerSet() bool { return idleWatchSpawner != nil }

// ResolveIdleTimeout is ResolveIdleTimeoutForDir for the active workspace's
// beadsDir-less callers: it reads the globally bound config.
func ResolveIdleTimeout() time.Duration {
	return resolveIdleTimeout("")
}

// ResolveIdleTimeoutForDir resolves the idle window for the workspace at
// beadsDir: BEADS_DOLT_IDLE_TIMEOUT first, then dolt.idle-timeout in
// <beadsDir>/config.yaml, then DefaultIdleTimeout. Zero means the idle
// watcher is disabled. An unparsable value warns once and yields the default.
func ResolveIdleTimeoutForDir(beadsDir string) time.Duration {
	return resolveIdleTimeout(beadsDir)
}

func resolveIdleTimeout(beadsDir string) time.Duration {
	if raw, ok := os.LookupEnv("BEADS_DOLT_IDLE_TIMEOUT"); ok && strings.TrimSpace(raw) != "" {
		if d, valid := parseIdleTimeout(raw); valid {
			return d
		}
		fmt.Fprintf(os.Stderr, "Warning: BEADS_DOLT_IDLE_TIMEOUT=%q is not a valid duration; using the default %s\n",
			raw, DefaultIdleTimeout)
		return DefaultIdleTimeout
	}
	var raw string
	if beadsDir == "" {
		// The globally bound config names the active workspace's settings.
		raw = strings.TrimSpace(config.GetString("dolt.idle-timeout"))
	} else {
		raw = strings.TrimSpace(config.GetStringFromDir(beadsDir, "dolt.idle-timeout"))
	}
	if raw != "" {
		if d, valid := parseIdleTimeout(raw); valid {
			return d
		}
		fmt.Fprintf(os.Stderr, "Warning: dolt.idle-timeout: %q is not a valid duration; using the default %s\n",
			raw, DefaultIdleTimeout)
	}
	return DefaultIdleTimeout
}

// parseIdleTimeout parses one idle-timeout setting: a Go duration, with a
// bare "0" (also what a YAML `idle-timeout: 0` parses to) meaning disabled.
func parseIdleTimeout(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if raw == "0" {
		// What a YAML `idle-timeout: 0` parses to: disabled.
		return 0, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

// idleWatchEligible reports whether an auto-started server gets an idle
// watcher. The spawner must be set (test binaries and SDK embedders keep
// today's behavior), the window enabled, the server one bd owns — owned
// mode, not the shared server — and every listener one bd chose: a port the
// operator pinned (env var, config.yaml, metadata.json) or a configured
// remotesapi port describes a deployment whose lifecycle is not bd's to end,
// the same movable rule Start uses to decide a failed bind may move ports.
func idleWatchEligible(mode ServerMode, sharedMode bool, port int, portSource PortSource, portSharedServer bool, remotesAPIPort int, timeout time.Duration) bool {
	if !idleWatchSpawnerSet() || timeout <= 0 {
		return false
	}
	if mode != ServerModeOwned || sharedMode {
		return false
	}
	movable := port == 0 || (portSource == PortSourcePortFile && !portSharedServer)
	if !movable || remotesAPIPort != 0 {
		return false
	}
	return true
}

// serverStatus is one poll's reading of the server's global status counters.
// Both are maintained by the MySQL engine (go-mysql-server increments
// Connections on every accept and tracks Threads_connected), so a connection
// that opened and closed between two polls is still visible in the delta.
type serverStatus struct {
	connections      int
	threadsConnected int
}

// idleStatusReader reads a server's global status counters. A variable so
// tests can fake the server's answers; the real one opens one short
// connection with the credentials DialConfig uses and closes it again.
var idleStatusReader = readServerGlobalStatus

// readServerGlobalStatus opens one short connection to the dolt sql-server at
// host:port and reads Connections and Threads_connected from SHOW GLOBAL
// STATUS. known reports whether both counters were present: a missing
// variable counts as uncertainty, which the caller treats as activity.
func readServerGlobalStatus(host string, port int) (status serverStatus, known bool, err error) {
	dsn := doltutil.ServerDSN{Host: host, Port: port, User: "root", Timeout: 5 * time.Second}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return serverStatus{}, false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return serverStatus{}, false, err
	}
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS")
	if err != nil {
		return serverStatus{}, false, err
	}
	defer rows.Close()
	connectionsSet, threadsSet := false, false
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return serverStatus{}, false, err
		}
		switch name {
		case "Connections":
			status.connections, _ = strconv.Atoi(value)
			connectionsSet = true
		case "Threads_connected":
			status.threadsConnected, _ = strconv.Atoi(value)
			threadsSet = true
		}
	}
	if err := rows.Err(); err != nil {
		return serverStatus{}, false, err
	}
	return status, connectionsSet && threadsSet, nil
}

// idlePoller accumulates the samples between polls and decides, for each
// interval, whether a client was active in it.
type idlePoller struct {
	// lastGood is the previous poll's counters, nil until one poll succeeded
	// (and after any failed poll: the interval that failed is unknown, so the
	// next good one re-establishes the baseline as activity).
	lastGood *serverStatus
	// dialsSinceGood counts the watcher's own dials since the last good
	// sample, this poll's dial included.
	dialsSinceGood int
}

// record folds one poll into the window and reports whether the interval
// showed client activity. Uncertainty — an error, a missing variable, no
// baseline yet — always counts as activity: the watcher never reaps on doubt.
// A live client lease counts as activity too.
func (p *idlePoller) record(status serverStatus, known bool, err error, liveLease bool) bool {
	p.dialsSinceGood++ // this poll's own dial
	var active bool
	switch {
	case err != nil || !known:
		active = true
		p.lastGood = nil
	case p.lastGood == nil:
		// First sample: a baseline, never a reap decision.
		active = true
		p.lastGood = &status
	default:
		delta := status.connections - p.lastGood.connections
		active = delta > p.dialsSinceGood || status.threadsConnected > 1
		p.lastGood = &status
	}
	p.dialsSinceGood = 0
	if liveLease {
		active = true
	}
	return active
}

// AcquireClientLease records that a bd store is open on the workspace at
// serverDir, so an idle watcher watching its server will not reap it while
// the store lives even though the store holds no open SQL connection (a
// `bd serve` process, a pooled connection between requests). The returned
// release func removes the lease; a nil func means no lease was taken.
//
// When a reap is in flight (the reaping marker present), the lease first takes
// and releases the lifecycle lock, so the in-flight reap finishes before this
// store's open continues: the store writes its lease before it reads the
// marker and the watcher writes the marker before it re-reads leases, so one
// side always sees the other. A lease error is the caller's to log and
// swallow — a lease must never fail a command.
func AcquireClientLease(serverDir string) (release func(), err error) {
	if serverDir == "" {
		return nil, errors.New("idle watch lease: no server dir")
	}
	if err := ensureActivityDir(serverDir); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s%d-%d", clientLeasePrefix, os.Getpid(), time.Now().UnixNano())
	path := leasePathFor(serverDir, name)
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, fmt.Errorf("writing idle-watch lease %s: %w", path, err)
	}
	if _, err := os.Stat(reapingPath(serverDir)); err == nil {
		// A reap may be stopping the server right now. Waiting for the
		// lifecycle lock means it is done: either the server is gone and
		// this open fails as it would have, or the reap declined and the
		// server is up with this lease now protecting it.
		if lockF, lockErr := acquireLifecycleLock(serverDir); lockErr == nil {
			releaseLifecycleLock(lockF)
		}
	}
	release = func() { _ = os.Remove(path) }
	return release, nil
}

// scanLeases reports whether any live client lease exists in serverDir's
// activity directory and prunes the ones whose owning process is dead. A
// lease whose name does not parse is left alone.
func scanLeases(serverDir string) bool {
	entries, err := os.ReadDir(activityDir(serverDir))
	if err != nil {
		// Unreadable is uncertain: count as a live lease.
		return true
	}
	live := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, clientLeasePrefix) {
			continue
		}
		pid, ok := leasePID(name)
		if !ok {
			continue
		}
		if isProcessAlive(pid) {
			live = true
			continue
		}
		_ = os.Remove(leasePathFor(serverDir, name))
	}
	return live
}

// leasePID extracts the owning PID from a client-<pid>-<nonce> lease name.
func leasePID(name string) (int, bool) {
	rest := strings.TrimPrefix(name, clientLeasePrefix)
	pidText, _, found := strings.Cut(rest, "-")
	if !found {
		return 0, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// watchStillValid reports whether the watcher is still the one watching this
// server: the watched PID alive, the pid file still naming it, and watcher.pid
// still naming this process. A `bd dolt start` that adopted the server and a
// stop through any other means each remove one of those, and the watcher
// exits without touching anything (GH#4282: the watcher never starts
// servers, and after a stop there is nothing to watch).
func watchStillValid(opts IdleWatchOpts) bool {
	if !isProcessAlive(opts.PID) {
		return false
	}
	data, err := os.ReadFile(pidPath(opts.ServerDir))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != opts.PID {
		return false
	}
	wdata, err := os.ReadFile(watcherPIDPath(opts.ServerDir))
	if err != nil {
		return false
	}
	wpid, err := strconv.Atoi(strings.TrimSpace(string(wdata)))
	if err != nil || wpid != os.Getpid() {
		return false
	}
	return true
}

// RunIdleWatch watches an auto-started dolt sql-server and stops it through
// the `bd dolt stop` path after opts.Timeout of no client activity. It exits
// without acting when the server stops or changes hands by any other means.
// See the package comment for the state files and the decision rules.
func RunIdleWatch(ctx context.Context, opts IdleWatchOpts) {
	if opts.ServerDir == "" || opts.PID <= 0 || opts.Port <= 0 || opts.Timeout <= 0 {
		return
	}
	if err := ensureActivityDir(opts.ServerDir); err != nil {
		debug.Logf("idle-watch: %v", err)
		return
	}
	if err := os.WriteFile(watcherPIDPath(opts.ServerDir), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		debug.Logf("idle-watch: writing watcher pid: %v", err)
		return
	}

	pollEvery := clampDuration(opts.Timeout/6, time.Second, 5*time.Minute)
	var poller idlePoller
	lastActivity := time.Now()
	var nextPoll time.Time

	for {
		if ctx.Err() != nil {
			return
		}
		if !watchStillValid(opts) {
			return
		}
		now := time.Now()
		if !nextPoll.IsZero() && now.Before(nextPoll) {
			sleepIdleStep(ctx, nextPoll.Sub(now))
			continue
		}
		nextPoll = now.Add(pollEvery)

		liveLease := scanLeases(opts.ServerDir)
		status, known, err := idleStatusReader(opts.Host, opts.Port)
		if poller.record(status, known, err, liveLease) {
			lastActivity = now
			continue
		}
		if now.Sub(lastActivity) < opts.Timeout {
			continue
		}
		stopped, exit := reapIdle(opts, &poller)
		if stopped {
			appendIdleStopLogLine(opts)
		}
		if exit {
			return
		}
		// The reap declined (activity under the marker or a stop failure);
		// start the window over rather than re-reaping every poll.
		lastActivity = time.Now()
	}
}

// sleepIdleStep sleeps step, capped so the watcher's liveness checks stay
// well inside the 5s exit budget of a server that stopped under it, and
// returns immediately when ctx is done.
func sleepIdleStep(ctx context.Context, wait time.Duration) {
	const step = 100 * time.Millisecond
	if wait <= 0 {
		wait = step
	}
	if wait > step {
		wait = step
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// reapIdle stops the idle server: it re-checks the watch, marks the reap,
// re-reads leases and counters one last time, and, still idle, stops the
// server under the lifecycle lock through the same stopLocked `bd dolt stop`
// uses (working-set flush and state-file cleanup included). It reports
// whether the server was stopped and whether the watcher should exit.
func reapIdle(opts IdleWatchOpts, poller *idlePoller) (stopped, exit bool) {
	if !watchStillValid(opts) {
		return false, true
	}
	if err := os.WriteFile(reapingPath(opts.ServerDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		// Cannot mark the reap: uncertain state, keep watching.
		debug.Logf("idle-watch: writing reaping marker: %v", err)
		return false, false
	}
	decline := func() {
		_ = os.Remove(reapingPath(opts.ServerDir))
	}

	// Anything that moved since the decision — a client connection, a lease,
	// a failed read — cancels the reap. Uncertainty keeps the server up.
	liveLease := scanLeases(opts.ServerDir)
	status, known, err := idleStatusReader(opts.Host, opts.Port)
	if poller.record(status, known, err, liveLease) {
		decline()
		return false, false
	}

	lockF, lockErr := acquireLifecycleLock(opts.ServerDir)
	if lockErr != nil {
		debug.Logf("idle-watch: acquiring lifecycle lock to reap: %v", lockErr)
		decline()
		return false, false
	}
	defer releaseLifecycleLock(lockF)
	stopErr := stopLocked(opts.ServerDir)
	if stopErr != nil {
		if errors.Is(stopErr, ErrServerNotRunning) {
			// The server went away under us: nothing to stop, nothing to
			// watch. stopLocked's cleanup already removed the marker.
			return false, true
		}
		debug.Logf("idle-watch: stopping idle server: %v", stopErr)
		_ = os.Remove(reapingPath(opts.ServerDir))
		return false, false
	}
	return true, true
}

// appendIdleStopLogLine records the idle stop in the server log, where a user
// wondering where their server went finds the reason and the remedy.
func appendIdleStopLogLine(opts IdleWatchOpts) {
	f, err := os.OpenFile(logPath(opts.ServerDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) //nolint:gosec // G304: logPath derives from the watcher's server dir
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s bd idle-watch: server stopped after %s idle; the next bd command starts it again\n",
		time.Now().UTC().Format(time.RFC3339), opts.Timeout)
}

// clampDuration clamps d into [min, max].
func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// cancelIdleWatch removes the watcher pid marker so a running watcher for
// serverDir exits instead of reaping a server another start adopted. Best
// effort: a missing marker is the watcher already gone.
func cancelIdleWatch(serverDir string) {
	_ = os.Remove(watcherPIDPath(serverDir))
}

// clearStaleReapingMarker removes a reaping marker left by a watcher that
// died mid-reap. startLocked calls it on the fresh-start path so a crashed
// watcher's marker cannot wedge later opens behind the lifecycle lock.
func clearStaleReapingMarker(serverDir string) {
	_ = os.Remove(reapingPath(serverDir))
}
