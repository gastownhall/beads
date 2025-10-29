package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher monitors JSONL and git ref changes using filesystem events or polling.
type FileWatcher struct {
	watcher      *fsnotify.Watcher
	debouncer    *Debouncer
	jsonlPath    string
	pollingMode  bool
	lastModTime  time.Time
	lastExists   bool
	lastSize     int64
	pollInterval time.Duration
	gitRefsPath  string
	cancel       context.CancelFunc
}

// NewFileWatcher creates a file watcher for the given JSONL path.
// onChanged is called when the file or git refs change, after debouncing.
// Falls back to polling mode if fsnotify fails (controlled by BEADS_WATCHER_FALLBACK env var).
func NewFileWatcher(jsonlPath string, onChanged func()) (*FileWatcher, error) {
	fw := &FileWatcher{
		jsonlPath:    jsonlPath,
		debouncer:    NewDebouncer(500*time.Millisecond, onChanged),
		pollInterval: 5 * time.Second,
	}

	// Get initial file state for polling fallback
	if stat, err := os.Stat(jsonlPath); err == nil {
		fw.lastModTime = stat.ModTime()
		fw.lastExists = true
		fw.lastSize = stat.Size()
	}

	// Check if fallback is disabled
	fallbackEnv := os.Getenv("BEADS_WATCHER_FALLBACK")
	fallbackDisabled := fallbackEnv == "false" || fallbackEnv == "0"

	// Store git refs path for filtering
	fw.gitRefsPath = filepath.Join(filepath.Dir(jsonlPath), "..", ".git", "refs", "heads")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		if fallbackDisabled {
			return nil, fmt.Errorf("fsnotify.NewWatcher() failed and BEADS_WATCHER_FALLBACK is disabled: %w", err)
		}
		// Fall back to polling mode
		fmt.Fprintf(os.Stderr, "Warning: fsnotify.NewWatcher() failed (%v), falling back to polling mode (%v interval)\n", err, fw.pollInterval)
		fmt.Fprintf(os.Stderr, "Set BEADS_WATCHER_FALLBACK=false to disable this fallback and require fsnotify\n")
		fw.pollingMode = true
		return fw, nil
	}

	fw.watcher = watcher

	// Watch the JSONL file
	if err := watcher.Add(jsonlPath); err != nil {
		watcher.Close()
		if fallbackDisabled {
			return nil, fmt.Errorf("failed to watch JSONL and BEADS_WATCHER_FALLBACK is disabled: %w", err)
		}
		// Fall back to polling mode
		fmt.Fprintf(os.Stderr, "Warning: failed to watch JSONL (%v), falling back to polling mode (%v interval)\n", err, fw.pollInterval)
		fmt.Fprintf(os.Stderr, "Set BEADS_WATCHER_FALLBACK=false to disable this fallback and require fsnotify\n")
		fw.pollingMode = true
		fw.watcher = nil
		return fw, nil
	}

	// Also watch .git/refs/heads for branch changes (best effort)
	_ = watcher.Add(fw.gitRefsPath) // Ignore error - not all setups have this

	return fw, nil
}

// Start begins monitoring filesystem events or polling.
// Runs in background goroutine until context is canceled.
// Should only be called once per FileWatcher instance.
func (fw *FileWatcher) Start(ctx context.Context, log daemonLogger) {
	// Create internal cancel so Close can stop goroutines
	ctx, cancel := context.WithCancel(ctx)
	fw.cancel = cancel

	if fw.pollingMode {
		fw.startPolling(ctx, log)
		return
	}

	go func() {
		for {
			select {
			case event, ok := <-fw.watcher.Events:
				if !ok {
					return
				}

				// Handle JSONL write events
				if event.Name == fw.jsonlPath && event.Op&fsnotify.Write != 0 {
					log.log("File change detected: %s", event.Name)
					fw.debouncer.Trigger()
				}

				// Handle JSONL removal/rename (e.g., git checkout)
				if event.Name == fw.jsonlPath && (event.Op&fsnotify.Remove != 0 || event.Op&fsnotify.Rename != 0) {
					log.log("JSONL removed/renamed, re-establishing watch")
					fw.watcher.Remove(fw.jsonlPath)
					// Brief wait for file to be recreated
					time.Sleep(100 * time.Millisecond)
					if err := fw.watcher.Add(fw.jsonlPath); err != nil {
						log.log("Failed to re-watch JSONL: %v", err)
					} else {
						// File was recreated, trigger to reload
						fw.debouncer.Trigger()
					}
				}

				// Handle git ref changes (only events under gitRefsPath)
				if event.Op&fsnotify.Write != 0 && strings.HasPrefix(event.Name, fw.gitRefsPath) {
					log.log("Git ref change detected: %s", event.Name)
					fw.debouncer.Trigger()
				}

			case err, ok := <-fw.watcher.Errors:
				if !ok {
					return
				}
				log.log("Watcher error: %v", err)

			case <-ctx.Done():
				return
			}
		}
	}()
}

// startPolling begins polling for file changes using a ticker.
func (fw *FileWatcher) startPolling(ctx context.Context, log daemonLogger) {
	log.log("Starting polling mode with %v interval", fw.pollInterval)
	ticker := time.NewTicker(fw.pollInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stat, err := os.Stat(fw.jsonlPath)
				if err != nil {
					if os.IsNotExist(err) {
						// File disappeared
						if fw.lastExists {
							fw.lastExists = false
							fw.lastModTime = time.Time{}
							fw.lastSize = 0
							log.log("File missing (polling): %s", fw.jsonlPath)
							fw.debouncer.Trigger()
						}
						continue
					}
					log.log("Polling error: %v", err)
					continue
				}

				// File exists
				if !fw.lastExists {
					// File appeared
					fw.lastExists = true
					fw.lastModTime = stat.ModTime()
					fw.lastSize = stat.Size()
					log.log("File appeared (polling): %s", fw.jsonlPath)
					fw.debouncer.Trigger()
					continue
				}

				// File exists and existed before - check for changes
				if !stat.ModTime().Equal(fw.lastModTime) || stat.Size() != fw.lastSize {
					fw.lastModTime = stat.ModTime()
					fw.lastSize = stat.Size()
					log.log("File change detected (polling): %s", fw.jsonlPath)
					fw.debouncer.Trigger()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Close stops the file watcher and releases resources.
func (fw *FileWatcher) Close() error {
	// Stop background goroutines
	if fw.cancel != nil {
		fw.cancel()
	}
	fw.debouncer.Cancel()
	if fw.watcher != nil {
		return fw.watcher.Close()
	}
	return nil
}
