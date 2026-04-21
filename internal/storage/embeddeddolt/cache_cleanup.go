//go:build cgo

package embeddeddolt

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cleanGitRemoteCacheGarbage removes orphaned tmp_pack_* files from the
// Dolt git-remote-cache. These files are created by `git fetch` (invoked
// by Dolt's GitBlobstore) and should be renamed to final .pack/.idx files
// on success or deleted on failure. In practice, failed or interrupted
// fetches leave them behind indefinitely, and Dolt's built-in periodic
// git gc (maybeRunGC, gated to once per 24h) either never runs or cannot
// keep up with the accumulation rate.
//
// On a real machine with normal beads usage, this leak consumed 102 GB
// (412 files) in 7 days. See https://github.com/gastownhall/beads/issues/3354
//
// This function is safe to call concurrently and is rate-limited to avoid
// unnecessary filesystem walks on hot paths.
func (s *EmbeddedDoltStore) cleanGitRemoteCacheGarbage() {
	if !cacheCleanupThrottle.shouldRun() {
		return
	}

	cacheBase := filepath.Join(s.dataDir, s.database, ".dolt", "git-remote-cache")
	if _, err := os.Stat(cacheBase); os.IsNotExist(err) {
		return
	}

	cutoff := time.Now().Add(-tmpPackMinAge)

	_ = filepath.WalkDir(cacheBase, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "tmp_pack_") && !strings.HasPrefix(name, "tmp_idx_") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			// #nosec G122 -- path is under .dolt/git-remote-cache/ which is
			// owned by the user running bd. A TOCTOU symlink swap would
			// require write access to that directory; in that case the
			// attacker already controls the Dolt data. The tmp_pack_/tmp_idx_
			// prefix check further narrows the scope to files Dolt itself writes.
			_ = os.Remove(path)
		}
		return nil
	})
}

const (
	// tmpPackMinAge is the minimum age before a tmp_pack file is considered
	// garbage. Files younger than this may belong to an in-progress fetch.
	tmpPackMinAge = 5 * time.Minute

	// cacheCleanupInterval is how often cleanGitRemoteCacheGarbage actually
	// walks the filesystem when called repeatedly.
	cacheCleanupInterval = 10 * time.Minute
)

// throttle gates a function to run at most once per interval.
type throttle struct {
	mu       sync.Mutex
	interval time.Duration
	lastRun  time.Time
}

func (t *throttle) shouldRun() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.lastRun) < t.interval {
		return false
	}
	t.lastRun = time.Now()
	return true
}

// cacheCleanupThrottle is a package-level throttle shared across all
// EmbeddedDoltStore instances in the same process.
var cacheCleanupThrottle = &throttle{interval: cacheCleanupInterval}
