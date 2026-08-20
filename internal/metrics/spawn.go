package metrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const SendMetricsSubcommand = "send-metrics"

// EnvIsFlusher marks the detached send-metrics child process. The parent sets it
// on the child's environment and MaybeSpawnFlusher refuses to spawn when it is
// set, so the flusher can never spawn another flusher (an unbounded process
// chain). This structural guard does not depend on send-metrics calling os.Exit
// before control returns to main()'s spawn tail.
const EnvIsFlusher = "BD_IS_FLUSHER"

// EnvTestMode is the same test-run flag the storage layer already checks
// (e.g. internal/storage/dolt/open.go, store.go): `os.Getenv(EnvTestMode) ==
// "1"`. MaybeSpawnFlusher honors it too so test suites no longer need to
// hand-set BD_DISABLE_EVENT_FLUSH=1 just to stop the detached send-metrics
// child from racing t.TempDir's RemoveAll (gastownhall/beads#5032).
const EnvTestMode = "BEADS_TEST_MODE"

// inTestMode reports whether the process is running under a beads test
// suite, using the same idiom as the rest of the codebase.
func inTestMode() bool {
	return os.Getenv(EnvTestMode) == "1"
}

// shouldSpawnFlusher is the env/config half of the spawn gate, extracted so
// tests can assert the decision without forking a real detached child. The
// stateful half (pending events + throttle) is flusherDue.
//
// Enabled() is deliberately NOT part of this gate (GH#5712): with telemetry
// disabled the detached child still has one job — pruning the queued backlog
// a previously-enabled configuration left behind. RunSendMetrics exits after
// the prune, before any upload, when disabled; and flusherDue's marker-first
// throttle keeps the disabled path exactly as cheap as the enabled one (one
// marker stat inside the interval, and no spawn at all once the queue has
// decayed empty — a never-enabled machine has no queue dir, so its scan is a
// single failed open).
func shouldSpawnFlusher() bool {
	return os.Getenv(EnvIsFlusher) != "1" && !flushDisabledByEnv() && !inTestMode()
}

// flushInterval is the minimum spacing between detached send-metrics spawns.
// Every bd invocation used to spawn one unconditionally — a full re-exec of
// the ~200MB binary plus an HTTPS POST, measured at 8.5-14.1ms in-band before
// the child even starts, ~10k spawns/day on a busy machine. Events tolerate
// batching: nothing downstream needs sub-5-minute telemetry latency, and any
// backlog uploads in one flush.
const flushInterval = 5 * time.Minute

// flushMarkerName is the throttle marker inside the eventsData dir. Its mtime
// records the last spawn ATTEMPT (not success: the parent detaches before the
// child's outcome is knowable, and attempt-based throttling is what bounds the
// spawn rate even when uploads keep failing). The eventkit FileFlusher only
// touches `*.evtq` entries, so the marker is inert to it.
const flushMarkerName = ".last-flush"

// flusherDue reports whether a detached flush is worth spawning: the last
// spawn attempt is at least flushInterval old, and the queue holds work — a
// queued event batch, or an orphaned emitter temp already past its prune TTL.
// The marker stat runs FIRST so the throttled path — the common case, every
// invocation inside the interval — costs one stat and never scans the queue:
// when uploads keep failing the queue backs up without bound (148k entries /
// 1.1GB observed on one dev machine), and a scan of a directory that size
// costs ~250ms. A marker mtime more than one interval in the future (clock
// stepped back) reads as due rather than suppressing uploads until the wall
// clock catches up; smaller negative ages are ordinary timestamp skew (e.g.
// the caller sampled now just before the marker was written) and stay
// throttled.
func flusherDue(dir string, now time.Time) bool {
	if fi, err := os.Stat(filepath.Join(dir, flushMarkerName)); err == nil {
		age := now.Sub(fi.ModTime())
		if age < flushInterval && age > -flushInterval {
			return false
		}
	}
	return scanQueue(dir, now)
}

// scanQueue is hasQueuedEvents behind a seam so tests can assert the
// throttled path never reaches the scan — the perf property the marker-first
// ordering exists for.
var scanQueue = hasQueuedEvents

// hasQueuedEvents reports whether dir holds work a detached child should act
// on: a queued event batch, or a prune-eligible (past-pruneTTL) orphaned
// emitter temp. It reads the directory in small unsorted chunks and returns at
// the first match, instead of os.ReadDir, which reads and sorts EVERY entry
// before the caller sees one — on a backed-up queue that is the whole cost
// flusherDue exists to avoid paying per invocation.
//
// A batch matches by name alone (no stat), so the backed-up-queue hot path is
// unchanged. Orphaned .write-* temps (a FileEmitter killed between CreateTemp
// and Rename) are reclaimed ONLY by the prune child and only once past
// pruneTTL; a dir holding just those, with no batch, must still become due or
// the prune child never spawns and the temp outlives its TTL forever on a
// disabled machine (the GH#5712 orphan-only follow-up). A stale temp is
// stat-gated on the same pruneTTL threshold pruneQueue uses, so the predicate
// fires exactly when the prune would actually remove it — a still-fresh temp
// is not yet due.
func hasQueuedEvents(dir string, now time.Time) bool {
	f, err := os.Open(dir) // #nosec G304 -- dir is the metrics DataDir, not user input
	if err != nil {
		// Unreadable/missing queue dir: the emitter never wrote, so there is
		// nothing a flusher child could upload or prune.
		return false
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(64)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if filepath.Ext(name) == queuedEventExt {
				return true
			}
			if strings.HasPrefix(name, writeTempPrefix) {
				// Stat only the temp candidates (rare on a disabled machine,
				// which emits nothing new); a batch already returned above, so
				// the hot path never reaches this stat.
				if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > pruneTTL {
					return true
				}
			}
		}
		if err != nil {
			// io.EOF (scanned everything) or a read error mid-listing: either
			// way no batch and no prune-eligible temp was seen.
			return false
		}
	}
}

// touchFlushMarker stamps the throttle marker with the current time. Failures
// are ignored: the marker is a rate limiter, and on a filesystem where it
// cannot be written the worst case is the old always-spawn behavior.
//
// The marker is per-eventsData-dir. On a shared $HOME where enabled and
// disabled bd invocations target one dir, a disabled prune-only spawn also
// stamps it, so it can make a post-throttle window prune-only and delay (worst
// case, if disabled traffic dominates, starve) the enabled stream's uploads.
// This is design-tolerated latency, not a data-safety issue: enabled and
// disabled children run the identical PruneQueue(dir, now), so a disabled prune
// deletes exactly what an enabled one would; only upload cadence is affected,
// which the 7d TTL / 5-min batching budget already absorbs.
func touchFlushMarker(dir string) {
	path := filepath.Join(dir, flushMarkerName)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	// Marker doesn't exist yet (or Chtimes failed): (re)create it.
	_ = os.WriteFile(path, nil, 0o600)
}

func MaybeSpawnFlusher() {
	if !shouldSpawnFlusher() {
		return
	}
	dir, err := DataDir()
	if err != nil {
		return
	}
	if !flusherDue(dir, time.Now()) {
		return
	}
	touchFlushMarker(dir)
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, SendMetricsSubcommand)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Hand the child an explicit environment rather than letting it inherit ours
	// wholesale. By the time we spawn, the parent command may have loaded a
	// project .beads/.env that set BEADS_METRICS_ENDPOINT; an inherited value
	// would let a hostile repository redirect where the user's telemetry is
	// uploaded. Pin the endpoint to the value the parent already resolved from
	// env + user-global config (see resolveMetricsEndpoint in cmd/bd) and mark
	// the child as the flusher so it cannot recurse.
	cmd.Env = flusherChildEnv(os.Environ(), Endpoint())
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

// flusherChildEnv returns a copy of env with the metrics endpoint pinned to
// endpoint and the flusher marker set. Any inherited BEADS_METRICS_ENDPOINT (for
// example one a project .beads/.env loaded into the parent process) is dropped
// so the detached flusher cannot be redirected by project-controlled
// environment.
func flusherChildEnv(env []string, endpoint string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvEndpoint+"=") || strings.HasPrefix(kv, EnvIsFlusher+"=") {
			continue
		}
		out = append(out, kv)
	}
	if endpoint != "" {
		out = append(out, EnvEndpoint+"="+endpoint)
	}
	out = append(out, EnvIsFlusher+"=1")
	return out
}

func flushDisabledByEnv() bool {
	v := os.Getenv(EnvDisableEventFlush)
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false":
		return false
	}
	return true
}
