package gitenv

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// routingRemoval records one routing entry removed for a guarded region so
// the region's release can restore both presence and value exactly.
type routingRemoval struct {
	key   string
	value string
}

var (
	removalMu    sync.Mutex
	removals     []routingRemoval
	removalDepth int
)

// WithRoutingScrubbed runs fn with bd's git routing entries removed from the
// process environment, restoring them afterwards. Engines that spawn git
// plumbing in-process (dolt remote transfers) must not inherit a routing
// context bd selected for its own git surfaces: a leaked GIT_WORK_TREE with
// no matching GIT_DIR makes every git invocation refuse with
// "GIT_WORK_TREE not allowed without specifying GIT_DIR", so a -C retarget
// would break the next remote transfer. Config suppression controls are
// preserved with the same value-blind policy as ScrubRouting: suppression
// cannot redirect a read, so removing it would only blind one.
func WithRoutingScrubbed(fn func() error) error {
	acquireRoutingRemoval()
	defer releaseRoutingRemoval()
	return fn()
}

func acquireRoutingRemoval() {
	removalMu.Lock()
	defer removalMu.Unlock()
	if removalDepth == 0 {
		for _, entry := range os.Environ() {
			key := EntryKey(entry)
			if !IsRoutingKeyForOS(key, runtime.GOOS) || isConfigSuppressionControl(entry, runtime.GOOS) {
				continue
			}
			_, value, _ := strings.Cut(entry, "=")
			// Best effort, matching the scoped guards in gittraceenv and
			// githooksenv: a transfer that may still succeed beats no
			// transfer at all, and Environ keys cannot fail Unsetenv on
			// the platforms this fleet targets.
			_ = os.Unsetenv(key)
			removals = append(removals, routingRemoval{key: key, value: value})
		}
	}
	removalDepth++
}

func releaseRoutingRemoval() {
	removalMu.Lock()
	defer removalMu.Unlock()
	removalDepth--
	if removalDepth > 0 {
		return
	}
	for _, removal := range removals {
		// Best effort for the same reason as acquire: the transfer already
		// completed, and Environ keys cannot fail Setenv here.
		_ = os.Setenv(removal.key, removal.value)
	}
	removals = nil
}
