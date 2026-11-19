package doltserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Idle stop for auto-started servers.
//
// Every server-mode store open auto-starts a detached dolt sql-server when
// none is running, and nothing used to stop it: each workspace a user had
// touched kept a server (about 2 GB in the report) until reboot. Each
// auto-start now launches a small watcher process beside its server. Once
// the server has been idle for the idle window, the watcher stops it through
// the `bd dolt stop` path, and the next bd command auto-starts it as before.
//
// All watcher state lives under <serverDir>/dolt-server.activity/, a name the
// shipped .gitignore template already ignores:
//
//	watcher.pid        the watcher process: "<pid>\n<window>\n"

const (
	// DefaultIdleTimeout is the idle window when none is configured.
	DefaultIdleTimeout = 30 * time.Minute

	activityDirName    = "dolt-server.activity"
	watcherPIDFileName = "watcher.pid"

	minIdlePollEvery = time.Second
	maxIdlePollEvery = 5 * time.Minute
)

func activityDir(serverDir string) string { return filepath.Join(serverDir, activityDirName) }
func watcherPIDPath(serverDir string) string {
	return filepath.Join(activityDir(serverDir), watcherPIDFileName)
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

// idleWatchEligible reports whether an auto-start of serverDir's server, with
// the pre-start config cfg, launches an idle watcher. Only a server bd fully
// owns qualifies: an owned (not external, not shared) server on a port bd
// chose, with no remotesapi listener another machine may be using.
func idleWatchEligible(cfg *Config, mode ServerMode, sharedMode bool, timeout time.Duration) bool {
	if timeout <= 0 || IdleWatchSpawner == nil {
		return false
	}
	if mode != ServerModeOwned || sharedMode || cfg == nil || cfg.PortSharedServer {
		return false
	}
	if cfg.Port > 0 && cfg.PortSource != PortSourcePortFile {
		return false
	}
	return cfg.RemotesAPIPort <= 0
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

// clearIdleWatchRecords removes the per-server watcher records.
func clearIdleWatchRecords(serverDir string) {
	for _, p := range []string{watcherPIDPath(serverDir)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			debug.Logf("idle watch: removing %s: %v", p, err)
		}
	}
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
}

// idleTracker decides when a server has been idle for its window. It never
// calls a server idle on doubt: a failed poll, or a poll with no earlier
// counter to compare with, counts as activity.
type idleTracker struct {
	timeout      time.Duration
	haveBaseline bool
	lastActivity time.Time
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
)

func (o IdleWatchOutcome) String() string {
	switch o {
	case IdleWatchReaped:
		return "reaped"
	case IdleWatchServerGone:
		return "server-gone"
	default:
		return "canceled"
	}
}

// idleWatcher is RunIdleWatch's state. Its clock, sleep and status reader are
// fields so tests can drive it without waiting.
type idleWatcher struct {
	opts       IdleWatchOpts
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
	readStatus func(ctx context.Context, host string, port int) (serverStatus, error)
	alive      func(int) bool
	stop       func(serverDir string) error

	pollEvery time.Duration
	tracker   *idleTracker
}

func newIdleWatcher(opts IdleWatchOpts) *idleWatcher {
	poll := opts.Timeout / 6
	if poll < minIdlePollEvery {
		poll = minIdlePollEvery
	}
	if poll > maxIdlePollEvery {
		poll = maxIdlePollEvery
	}
	return &idleWatcher{
		opts:       opts,
		now:        time.Now,
		sleep:      sleepCtx,
		readStatus: readServerStatus,
		alive:      isProcessAlive,
		stop:       stopLocked,
		pollEvery:  poll,
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
// it has been idle for opts.Timeout.
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
	for {
		now := w.now()
		w.tracker.observe(now, w.sample(ctx))
		if w.tracker.idle(now) {
			outcome, done, err := w.reap(ctx)
			if done || err != nil {
				return outcome, err
			}
		}
		if err := w.sleep(ctx, w.pollEvery); err != nil {
			return IdleWatchCanceled, nil
		}
	}
}

// serverGone reports whether the watched server stopped or was replaced.
func (w *idleWatcher) serverGone() bool {
	return !w.alive(w.opts.PID) || readPIDFile(w.opts.ServerDir) != w.opts.PID
}

func (w *idleWatcher) sample(ctx context.Context) idleSample {
	var s idleSample
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

	if w.serverGone() {
		return IdleWatchServerGone, true, nil
	}
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
