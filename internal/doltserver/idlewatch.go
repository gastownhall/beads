package doltserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Idle stop for auto-started servers.
//
// Every server-mode store open auto-starts a detached dolt sql-server when
// none is running, and nothing used to stop it: each workspace a user had
// touched kept a server (about 2 GB in the report) until reboot. Each
// auto-start now launches a small watcher process beside its server. Once
// the server has had no new or open SQL connection and no open bd store for
// the idle window, the watcher stops it through the `bd dolt stop` path, and
// the next bd command auto-starts it as before.
//
// A server started explicitly (`bd dolt start`, `bd dolt restart`) gets no
// watcher, and Start cancels the watcher of an auto-started server it adopts,
// so an explicitly started server stays up as it always has.
//
// All watcher state lives under <serverDir>/dolt-server.activity/, a name the
// shipped .gitignore template already ignores:
//
//	watcher.pid        the watcher process: "<pid>\n<window>\n"
//	reaping            a watcher is stopping the server right now
//	idle-off           why the running server has no watcher (for status)
//	client-<pid>-<id>  a client lease: a bd store is open on this server

const (
	// IdleTimeoutEnv overrides dolt.idle-timeout.
	IdleTimeoutEnv = "BEADS_DOLT_IDLE_TIMEOUT"
	// IdleTimeoutConfigKey is the config.yaml key for the idle window.
	IdleTimeoutConfigKey = "dolt.idle-timeout"
	// DefaultIdleTimeout is the idle window when none is configured.
	DefaultIdleTimeout = 30 * time.Minute

	activityDirName    = "dolt-server.activity"
	watcherPIDFileName = "watcher.pid"
	reapingMarkerName  = "reaping"
	idleOffFileName    = "idle-off"
	clientLeasePrefix  = "client-"

	// idleWatchLivenessEvery bounds how long a watcher outlives its server
	// when something else stops it. The promise is 5s; checking every
	// second keeps it with room for a slow check on a loaded machine.
	idleWatchLivenessEvery = time.Second
	minIdlePollEvery       = time.Second
	maxIdlePollEvery       = 5 * time.Minute
)

func activityDir(serverDir string) string { return filepath.Join(serverDir, activityDirName) }
func watcherPIDPath(serverDir string) string {
	return filepath.Join(activityDir(serverDir), watcherPIDFileName)
}
func reapingMarkerPath(serverDir string) string {
	return filepath.Join(activityDir(serverDir), reapingMarkerName)
}
func idleOffPath(serverDir string) string {
	return filepath.Join(activityDir(serverDir), idleOffFileName)
}

// ensureActivityDir creates serverDir's activity directory. It never creates
// serverDir itself. A regular file of the same name (left by the idle monitor
// bd removed in GH#2452) is replaced by the directory.
func ensureActivityDir(serverDir string) error {
	dir := activityDir(serverDir)
	if fi, err := os.Lstat(dir); err == nil {
		if fi.IsDir() {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("replacing legacy %s file: %w", activityDirName, err)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// IdleWatchOpts names the server a watcher watches and its idle window.
type IdleWatchOpts struct {
	ServerDir string
	PID       int
	Host      string
	Port      int
	Timeout   time.Duration
}

// IdleWatchSpawner launches an idle watcher for an auto-started server and
// returns the watcher's PID. It is nil unless the bd binary sets it (cmd/bd
// re-execs itself as the hidden `dolt-idle-watch` command), so test binaries
// and SDK embedders keep servers up as before. It is called with the
// server's lifecycle lock held; the watcher waits for that lock before it
// starts watching, so the watcher.pid record is in place by then.
var IdleWatchSpawner func(opts IdleWatchOpts) (pid int, err error)

// ResolveIdleTimeout returns the idle window for beadsDir's auto-started
// server: BEADS_DOLT_IDLE_TIMEOUT, else dolt.idle-timeout from the active
// config (or beadsDir's config.yaml when no config is loaded), else 30m. 0
// turns idle stop off. An unparsable value prints one warning and the
// default applies.
func ResolveIdleTimeout(beadsDir string) time.Duration {
	source := IdleTimeoutEnv
	raw := strings.TrimSpace(os.Getenv(IdleTimeoutEnv))
	if raw == "" {
		source = IdleTimeoutConfigKey
		raw = strings.TrimSpace(config.GetString(IdleTimeoutConfigKey))
		if raw == "" && beadsDir != "" {
			raw = strings.TrimSpace(config.GetStringFromDir(beadsDir, IdleTimeoutConfigKey))
		}
	}
	if raw == "" {
		return DefaultIdleTimeout
	}
	d, err := parseIdleTimeout(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s=%q is not a duration (e.g. 30m, 2h, or 0 to turn idle stop off); using %s\n",
			source, raw, DefaultIdleTimeout)
		return DefaultIdleTimeout
	}
	return d
}

func parseIdleTimeout(raw string) (time.Duration, error) {
	if raw == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %s", raw)
	}
	return d, nil
}

// idleOffReason says why an auto-start of serverDir's server, with the
// pre-start config cfg, launches no idle watcher, or returns "" when it
// launches one. Only a server bd fully owns qualifies: an owned (not
// external, not shared) server on a port bd chose, with no remotesapi
// listener another machine may be using.
func idleOffReason(cfg *Config, mode ServerMode, sharedMode bool, timeout time.Duration) string {
	if timeout <= 0 {
		return idleOffTimeoutZero
	}
	if IdleWatchSpawner == nil {
		return idleOffNoSpawner
	}
	if mode != ServerModeOwned || sharedMode || cfg == nil || cfg.PortSharedServer {
		return idleOffNotBdOnly
	}
	if cfg.Port > 0 && cfg.PortSource != PortSourcePortFile {
		return idleOffNotBdOnly
	}
	if cfg.RemotesAPIPort > 0 {
		return idleOffNotBdOnly
	}
	return ""
}

// launchIdleWatcherLocked launches the watcher for a server EnsureRunning just
// started. It must be called with serverDir's lifecycle lock held. Failures
// are logged at debug level: the server is up either way.
func launchIdleWatcherLocked(serverDir string, state *State, host string, timeout time.Duration) {
	if err := ensureActivityDir(serverDir); err != nil {
		debug.Logf("idle watch: %v", err)
		return
	}
	pid, err := IdleWatchSpawner(IdleWatchOpts{
		ServerDir: serverDir,
		PID:       state.PID,
		Host:      host,
		Port:      state.Port,
		Timeout:   timeout,
	})
	if err != nil {
		debug.Logf("idle watch: launching watcher for %s: %v", serverDir, err)
		return
	}
	body := fmt.Sprintf("%d\n%s\n", pid, timeout)
	if err := os.WriteFile(watcherPIDPath(serverDir), []byte(body), 0o600); err != nil {
		debug.Logf("idle watch: recording watcher: %v", err)
	}
}

// clearIdleWatchRecords removes the per-server watcher records. Leases are
// left alone: they belong to open stores, not to a server.
func clearIdleWatchRecords(serverDir string) {
	for _, p := range []string{watcherPIDPath(serverDir), reapingMarkerPath(serverDir), idleOffPath(serverDir)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			debug.Logf("idle watch: removing %s: %v", p, err)
		}
	}
}

// Reasons recordIdleOff stores for a running server with no idle watcher.
const (
	IdleOffExplicitStart   = "started with bd dolt start"
	IdleOffExplicitRestart = "started with bd dolt restart"
	idleOffTimeoutZero     = "dolt.idle-timeout is 0"
	idleOffNoSpawner       = "started by a program that runs no idle watcher"
	idleOffNotBdOnly       = "shared server, pinned port or remotesapi listener"
)

// recordIdleOff notes, for bd dolt status, why serverDir's running server
// has no idle watcher. Best effort: a missing note only makes status less
// specific.
func recordIdleOff(serverDir, reason string) {
	if err := ensureActivityDir(serverDir); err != nil {
		debug.Logf("idle watch: %v", err)
		return
	}
	if err := os.WriteFile(idleOffPath(serverDir), []byte(reason+"\n"), 0o600); err != nil {
		debug.Logf("idle watch: recording idle-off reason: %v", err)
	}
}

// IdleStopState is a running bd-owned server's idle-stop setting as
// bd dolt status reports it.
type IdleStopState struct {
	// On is true while an idle watcher is running for the server.
	On bool
	// Window is the idle window when On.
	Window time.Duration
	// Reason says why idle stop is off.
	Reason string
}

func (s IdleStopState) String() string {
	if s.On {
		return "after " + FormatIdleWindow(s.Window)
	}
	return "off: " + s.Reason
}

// FormatIdleWindow prints an idle window without zero trailing units
// ("30m", "2h", "1h30m").
func FormatIdleWindow(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}

// ReadIdleStopState reports the idle-stop setting of serverDir's running
// server from its activity records.
func ReadIdleStopState(serverDir string) IdleStopState {
	if pid, window, ok := readWatcherRecord(serverDir); ok {
		if isProcessAlive(pid) {
			return IdleStopState{On: true, Window: window}
		}
		return IdleStopState{Reason: "watcher exited"}
	}
	if b, err := os.ReadFile(idleOffPath(serverDir)); err == nil { //nolint:gosec // G304: path under bd's own server dir
		if reason := strings.TrimSpace(string(b)); reason != "" {
			return IdleStopState{Reason: reason}
		}
	}
	return IdleStopState{Reason: "no watcher (started by an older bd)"}
}

// readWatcherRecord parses watcher.pid. ok is false when it is missing or
// unreadable.
func readWatcherRecord(serverDir string) (pid int, timeout time.Duration, ok bool) {
	b, err := os.ReadFile(watcherPIDPath(serverDir)) //nolint:gosec // G304: path under bd's own server dir
	if err != nil {
		return 0, 0, false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return 0, 0, false
	}
	if len(lines) > 1 {
		timeout, _ = time.ParseDuration(strings.TrimSpace(lines[1]))
	}
	return pid, timeout, true
}

// readPIDFile returns the PID dolt-server.pid names, or 0.
func readPIDFile(serverDir string) int {
	b, err := os.ReadFile(pidPath(serverDir))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// AcquireClientLease records that this process has a bd store open on
// serverDir's server, so its idle watcher does not stop the server while the
// store holds no SQL connection. release removes the lease; a lease whose
// process died is removed by the watcher.
//
// The lease is written before the reaping marker is read, and a reaping
// watcher writes the marker before it re-reads leases, so one side always
// sees the other: when a reap is in flight, this waits for it to finish
// (by taking and releasing the lifecycle lock) and the caller's auto-start
// then starts a fresh server.
func AcquireClientLease(serverDir string) (release func(), err error) {
	if err := ensureActivityDir(serverDir); err != nil {
		return nil, err
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(activityDir(serverDir),
		fmt.Sprintf("%s%d-%s", clientLeasePrefix, os.Getpid(), hex.EncodeToString(nonce[:])))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: path under bd's own server dir
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	if _, statErr := os.Stat(reapingMarkerPath(serverDir)); statErr == nil {
		if lockF, lockErr := acquireLifecycleLock(serverDir); lockErr == nil {
			releaseLifecycleLock(lockF)
		}
	}
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			_ = os.Remove(path)
		}
	}, nil
}

// liveClientLeases reports whether any client lease names a live process,
// removing leases whose process is gone.
func liveClientLeases(serverDir string, alive func(int) bool) bool {
	entries, err := os.ReadDir(activityDir(serverDir))
	if err != nil {
		return false
	}
	live := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, clientLeasePrefix) {
			continue
		}
		pidText, _, _ := strings.Cut(strings.TrimPrefix(name, clientLeasePrefix), "-")
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		if alive(pid) {
			live = true
			continue
		}
		_ = os.Remove(filepath.Join(activityDir(serverDir), name))
	}
	return live
}

// serverStatus is one poll of the server's connection counters.
type serverStatus struct {
	// connections is the Connections status variable: every connection the
	// server accepted since it started, however briefly it stayed open.
	connections int64
	// threads is Threads_connected: connections open now, the poll's own
	// included.
	threads int64
	// ownDials is how many connections the poll itself opened.
	ownDials int64
}

// idleSample is everything one poll learned.
type idleSample struct {
	status    serverStatus
	statusErr error
	leaseLive bool
}

// idleTracker decides when a server has been idle for its window. It never
// calls a server idle on doubt: a failed poll, or a poll with no earlier
// counter to compare with, counts as activity.
type idleTracker struct {
	timeout         time.Duration
	haveBaseline    bool
	lastConnections int64
	lastActivity    time.Time
}

func newIdleTracker(timeout time.Duration, now time.Time) *idleTracker {
	return &idleTracker{timeout: timeout, lastActivity: now}
}

// observe folds one poll into the tracker and reports whether it showed
// activity.
func (t *idleTracker) observe(now time.Time, s idleSample) bool {
	active := false
	switch {
	case s.statusErr != nil:
		// The counters cannot be compared across a failed poll.
		t.haveBaseline = false
		active = true
	case !t.haveBaseline:
		t.haveBaseline = true
		active = true
	default:
		if s.status.connections-t.lastConnections > s.status.ownDials {
			active = true
		}
	}
	if s.statusErr == nil {
		t.lastConnections = s.status.connections
		if s.status.threads > 1 {
			active = true
		}
	}
	if s.leaseLive {
		active = true
	}
	if active {
		t.lastActivity = now
	}
	return active
}

// idle reports whether the server has been continuously idle for the window.
func (t *idleTracker) idle(now time.Time) bool {
	return t.haveBaseline && now.Sub(t.lastActivity) >= t.timeout
}

// IdleWatchOutcome is why RunIdleWatch returned.
type IdleWatchOutcome int

const (
	// IdleWatchCanceled: the context ended.
	IdleWatchCanceled IdleWatchOutcome = iota
	// IdleWatchReaped: the watcher stopped the idle server.
	IdleWatchReaped
	// IdleWatchServerGone: the server stopped, or another server replaced
	// it, by other means.
	IdleWatchServerGone
	// IdleWatchSuperseded: watcher.pid no longer names this watcher (an
	// explicit start adopted the server, or a newer watcher took over).
	IdleWatchSuperseded
)

func (o IdleWatchOutcome) String() string {
	switch o {
	case IdleWatchReaped:
		return "reaped"
	case IdleWatchServerGone:
		return "server-gone"
	case IdleWatchSuperseded:
		return "superseded"
	default:
		return "canceled"
	}
}

// idleWatcher is RunIdleWatch's state. Its clock, sleep and status reader are
// fields so tests can drive it without waiting.
type idleWatcher struct {
	opts       IdleWatchOpts
	self       int
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
	readStatus func(ctx context.Context, host string, port int) (serverStatus, error)
	alive      func(int) bool
	stop       func(serverDir string) error

	livenessEvery time.Duration
	pollEvery     time.Duration
	tracker       *idleTracker
}

func newIdleWatcher(opts IdleWatchOpts) *idleWatcher {
	poll := opts.Timeout / 6
	if poll < minIdlePollEvery {
		poll = minIdlePollEvery
	}
	if poll > maxIdlePollEvery {
		poll = maxIdlePollEvery
	}
	liveness := idleWatchLivenessEvery
	if poll < liveness {
		liveness = poll
	}
	return &idleWatcher{
		opts:          opts,
		self:          os.Getpid(),
		now:           time.Now,
		sleep:         sleepCtx,
		readStatus:    readServerStatus,
		alive:         isProcessAlive,
		stop:          stopLocked,
		livenessEvery: liveness,
		pollEvery:     poll,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RunIdleWatch watches the auto-started server opts names and stops it once
// it has been idle for opts.Timeout. It returns without acting as soon as
// the server stops or is replaced by other means, or an explicit start
// cancels the watcher (watcher.pid no longer names this process).
func RunIdleWatch(ctx context.Context, opts IdleWatchOpts) (IdleWatchOutcome, error) {
	if opts.ServerDir == "" || opts.PID <= 0 || opts.Port <= 0 || opts.Timeout <= 0 {
		return IdleWatchCanceled, fmt.Errorf("idle watch: server dir, PID, port and a positive timeout are required")
	}
	return newIdleWatcher(opts).run(ctx)
}

func (w *idleWatcher) run(ctx context.Context) (IdleWatchOutcome, error) {
	// The spawning bd holds the lifecycle lock until it has recorded this
	// watcher in watcher.pid; wait for it before watching.
	lockF, err := acquireLifecycleLock(w.opts.ServerDir)
	if err != nil {
		return IdleWatchCanceled, err
	}
	releaseLifecycleLock(lockF)

	w.tracker = newIdleTracker(w.opts.Timeout, w.now())
	// Liveness is checked every second, the status poll only every
	// pollEvery, so a server stopped some other way is noticed quickly.
	var lastPoll time.Time
	for {
		if outcome, done := w.liveness(); done {
			return outcome, nil
		}
		now := w.now()
		if lastPoll.IsZero() || now.Sub(lastPoll) >= w.pollEvery {
			lastPoll = now
			w.tracker.observe(now, w.sample(ctx))
			if w.tracker.idle(now) {
				outcome, done, err := w.reap(ctx)
				if done || err != nil {
					return outcome, err
				}
			}
		}
		if err := w.sleep(ctx, w.livenessEvery); err != nil {
			return IdleWatchCanceled, nil
		}
	}
}

// liveness reports whether the watcher should exit without acting: its
// server stopped or was replaced, or watcher.pid no longer names it.
func (w *idleWatcher) liveness() (IdleWatchOutcome, bool) {
	if w.serverGone() {
		return IdleWatchServerGone, true
	}
	if w.superseded() {
		return IdleWatchSuperseded, true
	}
	return 0, false
}

// superseded reports whether watcher.pid no longer names this watcher.
func (w *idleWatcher) superseded() bool {
	pid, _, ok := readWatcherRecord(w.opts.ServerDir)
	return !ok || pid != w.self
}

// serverGone reports whether the watched server stopped or was replaced.
func (w *idleWatcher) serverGone() bool {
	return !w.alive(w.opts.PID) || readPIDFile(w.opts.ServerDir) != w.opts.PID
}

func (w *idleWatcher) sample(ctx context.Context) idleSample {
	s := idleSample{leaseLive: liveClientLeases(w.opts.ServerDir, w.alive)}
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.status, s.statusErr = w.readStatus(pollCtx, w.opts.Host, w.opts.Port)
	return s
}

// reap stops the server under the lifecycle lock, after re-checking under
// the lock that it is still this watcher's idle server. done is false when a
// late client showed up and watching goes on.
func (w *idleWatcher) reap(ctx context.Context) (outcome IdleWatchOutcome, done bool, err error) {
	serverDir := w.opts.ServerDir
	lockF, err := acquireLifecycleLock(serverDir)
	if err != nil {
		return IdleWatchCanceled, true, err
	}
	defer releaseLifecycleLock(lockF)

	if outcome, gone := w.liveness(); gone {
		return outcome, true, nil
	}
	if err := ensureActivityDir(serverDir); err != nil {
		return IdleWatchCanceled, true, err
	}
	if err := os.WriteFile(reapingMarkerPath(serverDir), nil, 0o600); err != nil {
		return IdleWatchCanceled, true, err
	}
	defer func() { _ = os.Remove(reapingMarkerPath(serverDir)) }()

	// A store that opened between the last poll and the marker holds a
	// lease (written before it read the marker) or a connection.
	if w.tracker.observe(w.now(), w.sample(ctx)) {
		return 0, false, nil
	}

	if err := IgnoreNotRunning(w.stop(serverDir)); err != nil {
		return IdleWatchCanceled, true, fmt.Errorf("idle watch: stopping idle server: %w", err)
	}
	_ = os.Remove(watcherPIDPath(serverDir))
	appendServerLog(serverDir, fmt.Sprintf("%s bd: stopped this dolt sql-server (PID %d) after %s with no clients (dolt.idle-timeout); the next bd command restarts it\n",
		time.Now().UTC().Format(time.RFC3339), w.opts.PID, w.opts.Timeout))
	return IdleWatchReaped, true, nil
}

func appendServerLog(serverDir, line string) {
	f, err := os.OpenFile(logPath(serverDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: path under bd's own server dir
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// countingConnector counts the connections a poll opens, so the poll's own
// dials (retries included) are not taken for client activity.
type countingConnector struct {
	driver.Connector
	dials atomic.Int64
}

func (c *countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.dials.Add(1)
	return c.Connector.Connect(ctx)
}

// readServerStatus reads Connections and Threads_connected over one short
// connection. A missing variable is an error, which the tracker takes as
// activity.
func readServerStatus(ctx context.Context, host string, port int) (serverStatus, error) {
	mcfg, err := mysql.ParseDSN(doltutil.ServerDSN{Host: host, Port: port, User: "root", Timeout: 5 * time.Second}.String())
	if err != nil {
		return serverStatus{}, err
	}
	connector, err := mysql.NewConnector(mcfg)
	if err != nil {
		return serverStatus{}, err
	}
	cc := &countingConnector{Connector: connector}
	db := sql.OpenDB(cc)
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS")
	if err != nil {
		return serverStatus{ownDials: cc.dials.Load()}, err
	}
	defer rows.Close()
	var st serverStatus
	var haveConns, haveThreads bool
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return serverStatus{ownDials: cc.dials.Load()}, err
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		switch {
		case strings.EqualFold(name, "Connections"):
			st.connections, haveConns = n, perr == nil
		case strings.EqualFold(name, "Threads_connected"):
			st.threads, haveThreads = n, perr == nil
		}
	}
	if err := rows.Err(); err != nil {
		return serverStatus{ownDials: cc.dials.Load()}, err
	}
	st.ownDials = cc.dials.Load()
	if !haveConns || !haveThreads {
		return st, errors.New("server status lacks Connections or Threads_connected")
	}
	return st, nil
}

// SpawnIdleWatchProcess launches exe as a detached `dolt-idle-watch` process
// for opts, with its standard streams on the null device, and returns its
// PID. cmd/bd's IdleWatchSpawner calls it with its own executable.
func SpawnIdleWatchProcess(exe string, opts IdleWatchOpts) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()
	cmd := exec.Command(exe, IdleWatchArgs(opts)...) //nolint:gosec // exe is bd's own executable
	cmd.Dir = opts.ServerDir
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = procAttrDetached()
	sanitizeInheritedFDs()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Reap it if it exits while this process is still running (bd serve).
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// IdleWatchArgs is the argument list of the hidden `bd dolt-idle-watch`
// command for opts.
func IdleWatchArgs(opts IdleWatchOpts) []string {
	return []string{
		"dolt-idle-watch",
		"--server-dir", opts.ServerDir,
		"--pid", strconv.Itoa(opts.PID),
		"--host", opts.Host,
		"--port", strconv.Itoa(opts.Port),
		"--timeout", opts.Timeout.String(),
	}
}
