package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ActivityWatcher monitors the beads directory for changes using filesystem events.
// Falls back to polling if fsnotify fails (some filesystems don't support it).
type ActivityWatcher struct {
	watcher      *fsnotify.Watcher
	watchPaths   []string // Paths being watched
	pollingMode  bool     // True if using polling fallback
	pollInterval time.Duration
	events       chan struct{} // Sends wake-up signals on changes
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex

	// Polling state
	lastModTimes map[string]time.Time
}

// NewActivityWatcher creates a watcher for activity feed updates.
// Watches the dolt noms directory for commits, falling back to polling if fsnotify fails.
// The beadsDir should be the .beads directory path.
// The pollInterval is used for polling fallback mode.
func NewActivityWatcher(beadsDir string, pollInterval time.Duration) *ActivityWatcher {
	aw := &ActivityWatcher{
		pollInterval: pollInterval,
		events:       make(chan struct{}, 1), // Buffered to avoid blocking
		lastModTimes: make(map[string]time.Time),
	}

	// Determine watch paths - prefer dolt noms directory if it exists
	doltNomsPath := filepath.Join(beadsDir, "dolt", ".dolt", "noms")
	doltPath := filepath.Join(beadsDir, "dolt", ".dolt")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Build list of paths to watch (in priority order)
	var watchPaths []string
	if stat, err := os.Stat(doltNomsPath); err == nil && stat.IsDir() {
		// Watch dolt noms directory for commits
		watchPaths = append(watchPaths, doltNomsPath)
	} else if stat, err := os.Stat(doltPath); err == nil && stat.IsDir() {
		// Fallback to .dolt directory
		watchPaths = append(watchPaths, doltPath)
	}
	// Also watch JSONL for non-dolt or hybrid setups
	if _, err := os.Stat(jsonlPath); err == nil {
		watchPaths = append(watchPaths, jsonlPath)
	}
	// Watch the beads dir itself as last resort
	if len(watchPaths) == 0 {
		watchPaths = append(watchPaths, beadsDir)
	}

	aw.watchPaths = watchPaths

	// Initialize modification times for polling
	for _, p := range watchPaths {
		if stat, err := os.Stat(p); err == nil {
			aw.lastModTimes[p] = stat.ModTime()
		}
	}

	// Try to create fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Fall back to polling mode
		aw.pollingMode = true
		return aw
	}

	// Add watches for each path
	watchedAny := false
	for _, p := range watchPaths {
		if err := watcher.Add(p); err != nil {
			// Log but continue - some paths may not be watchable
			continue
		}
		watchedAny = true
	}

	if !watchedAny {
		// No paths could be watched, fall back to polling
		_ = watcher.Close()
		aw.pollingMode = true
		return aw
	}

	aw.watcher = watcher
	return aw
}

// Events returns the channel that receives wake-up signals when changes are detected.
// The channel sends an empty struct for each detected change (debounced).
func (aw *ActivityWatcher) Events() <-chan struct{} {
	return aw.events
}

// IsPolling returns true if the watcher is using polling fallback.
func (aw *ActivityWatcher) IsPolling() bool {
	return aw.pollingMode
}

// Start begins monitoring for changes.
// Returns immediately, monitoring happens in background goroutine.
func (aw *ActivityWatcher) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	aw.cancel = cancel

	if aw.pollingMode {
		aw.startPolling(ctx)
	} else {
		aw.startFSWatch(ctx)
	}
}

// startFSWatch starts fsnotify-based watching.
func (aw *ActivityWatcher) startFSWatch(ctx context.Context) {
	aw.wg.Add(1)
	go func() {
		defer aw.wg.Done()

		// Debounce: don't send more than one event per 50ms
		var lastEvent time.Time
		debounceWindow := 50 * time.Millisecond

		for {
			select {
			case event, ok := <-aw.watcher.Events:
				if !ok {
					return
				}

				// Only trigger on write events
				if event.Op&fsnotify.Write == 0 && event.Op&fsnotify.Create == 0 {
					continue
				}

				// Debounce rapid events
				now := time.Now()
				if now.Sub(lastEvent) < debounceWindow {
					continue
				}
				lastEvent = now

				// Send non-blocking wake-up signal
				select {
				case aw.events <- struct{}{}:
				default:
					// Channel already has a pending event
				}

			case _, ok := <-aw.watcher.Errors:
				if !ok {
					return
				}
				// Log errors but continue watching

			case <-ctx.Done():
				return
			}
		}
	}()
}

// startPolling starts polling-based change detection.
func (aw *ActivityWatcher) startPolling(ctx context.Context) {
	aw.wg.Add(1)
	go func() {
		defer aw.wg.Done()

		ticker := time.NewTicker(aw.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if aw.checkForChanges() {
					// Send non-blocking wake-up signal
					select {
					case aw.events <- struct{}{}:
					default:
					}
				}

			case <-ctx.Done():
				return
			}
		}
	}()
}

// checkForChanges checks if any watched paths have been modified.
func (aw *ActivityWatcher) checkForChanges() bool {
	aw.mu.Lock()
	defer aw.mu.Unlock()

	changed := false
	for _, p := range aw.watchPaths {
		stat, err := os.Stat(p)
		if err != nil {
			continue
		}

		lastMod, exists := aw.lastModTimes[p]
		if !exists || !stat.ModTime().Equal(lastMod) {
			aw.lastModTimes[p] = stat.ModTime()
			changed = true
		}
	}
	return changed
}

// Close stops the watcher and releases resources.
func (aw *ActivityWatcher) Close() error {
	if aw.cancel != nil {
		aw.cancel()
	}
	aw.wg.Wait()
	close(aw.events)
	if aw.watcher != nil {
		return aw.watcher.Close()
	}
	return nil
}
