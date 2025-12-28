package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/steveyegge/beads/internal/git"
)

// FileWatcher monitors JSONL and git ref changes using filesystem events or polling.
type FileWatcher struct {
	watcher        *fsnotify.Watcher
	debouncer      *Debouncer
	jsonlPath      string
	parentDir      string
	pollingMode    bool
	lastModTime    time.Time
	lastExists     bool
	lastSize       int64
	pollInterval   time.Duration
	gitRefsPath    string
	gitHeadPath    string
	lastHeadModTime time.Time
	lastHeadExists bool
	cancel         context.CancelFunc
	wg             sync.WaitGroup // Track goroutines for graceful shutdown
	// Log deduplication: track last log times to avoid duplicate messages
	lastFileLogTime   time.Time
	lastGitRefLogTime time.Time
	logDedupeWindow   time.Duration
	logMu             sync.Mutex
}

// NewFileWatcher creates a file watcher for the given JSONL path.
// onChanged is called when the file or git refs change, after debouncing.
// Falls back to polling mode if fsnotify fails (controlled by BEADS_WATCHER_FALLBACK env var).
func NewFileWatcher(jsonlPath string, onChanged func()) (*FileWatcher, error) {
	fw := &FileWatcher{
		jsonlPath:       jsonlPath,
		parentDir:       filepath.Dir(jsonlPath),
		debouncer:       NewDebouncer(500*time.Millisecond, onChanged),
		pollInterval:    5 * time.Second,
		logDedupeWindow: 500 * time.Millisecond, // Deduplicate logs within this window
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

	// Store git paths for filtering using worktree-aware detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		// Not a git repo, skip git path setup
		fw.gitRefsPath = ""
		fw.gitHeadPath = ""
	} else {
		fw.gitRefsPath = filepath.Join(gitDir, "refs", "heads")
		fw.gitHeadPath = filepath.Join(gitDir, "HEAD")
	}

	// Get initial git HEAD state for polling
	if stat, err := os.Stat(fw.gitHeadPath); err == nil {
		fw.lastHeadModTime = stat.ModTime()
		fw.lastHeadExists = true
	}

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

	// Watch the parent directory (catches creates/renames)
	if err := watcher.Add(fw.parentDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to watch parent directory %s: %v\n", fw.parentDir, err)
	}

	// Watch the JSONL file (may not exist yet)
	if err := watcher.Add(jsonlPath); err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet - rely on parent dir watch
			fmt.Fprintf(os.Stderr, "Info: JSONL file %s doesn't exist yet, watching parent directory\n", jsonlPath)
		} else {
			_ = watcher.Close()
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
	}

	// Also watch .git/refs/heads and .git/HEAD for branch changes (best effort)
	if fw.gitRefsPath != "" {
		_ = watcher.Add(fw.gitRefsPath) // Ignore error - not all setups have this
	}
	if fw.gitHeadPath != "" {
		_ = watcher.Add(fw.gitHeadPath)  // Ignore error - not all setups have this
	}

	return fw, nil
}

// shouldLogFileChange returns true if enough time has passed since last file change log
func (fw *FileWatcher) shouldLogFileChange() bool {
	fw.logMu.Lock()
	defer fw.logMu.Unlock()
	now := time.Now()
	if now.Sub(fw.lastFileLogTime) >= fw.logDedupeWindow {
		fw.lastFileLogTime = now
		return true
	}
	return false
}

// shouldLogGitRefChange returns true if enough time has passed since last git ref change log
func (fw *FileWatcher) shouldLogGitRefChange() bool {
	fw.logMu.Lock()
	defer fw.logMu.Unlock()
	now := time.Now()
	if now.Sub(fw.lastGitRefLogTime) >= fw.logDedupeWindow {
		fw.lastGitRefLogTime = now
		return true
	}
	return false
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

	fw.wg.Add(1)
	go func() {
		defer fw.wg.Done()
		jsonlBase := filepath.Base(fw.jsonlPath)

		for {
			select {
			case event, ok := <-fw.watcher.Events:
				if !ok {
					return
				}

				// Handle parent directory events (file create/replace)
				if event.Name == filepath.Join(fw.parentDir, jsonlBase) && event.Op&fsnotify.Create != 0 {
					log.log("JSONL file created: %s", event.Name)
					// Ensure we're watching the file directly
					_ = fw.watcher.Add(fw.jsonlPath)
					fw.debouncer.Trigger()
					continue
				}

				// Handle JSONL write/chmod events
				if event.Name == fw.jsonlPath && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Chmod) != 0 {
					if fw.shouldLogFileChange() {
						log.log("File change detected: %s", event.Name)
					}
					fw.debouncer.Trigger()
					continue
				}

				// Handle JSONL removal/rename (e.g., git checkout)
				if event.Name == fw.jsonlPath && (event.Op&fsnotify.Remove != 0 || event.Op&fsnotify.Rename != 0) {
				log.log("JSONL removed/renamed, re-establishing watch")
				_ = fw.watcher.Remove(fw.jsonlPath)
					// Retry with exponential backoff
					fw.reEstablishWatch(ctx, log)
					continue
				}

				// Handle .git/HEAD changes (branch switches)
				if event.Name == fw.gitHeadPath && event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					log.log("Git HEAD change detected: %s", event.Name)
					fw.debouncer.Trigger()
					continue
				}

				// Handle git ref changes (only events under gitRefsPath)
				if event.Op&fsnotify.Write != 0 && strings.HasPrefix(event.Name, fw.gitRefsPath) {
					if fw.shouldLogGitRefChange() {
						log.log("Git ref change detected: %s", event.Name)
					}
					fw.debouncer.Trigger()
					continue
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

// reEstablishWatch attempts to re-add the JSONL watch with exponential backoff.
func (fw *FileWatcher) reEstablishWatch(ctx context.Context, log daemonLogger) {
	delays := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	
	for _, delay := range delays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			if err := fw.watcher.Add(fw.jsonlPath); err != nil {
				if os.IsNotExist(err) {
					log.log("JSONL still missing after %v, retrying...", delay)
					continue
				}
				log.log("Failed to re-watch JSONL after %v: %v", delay, err)
				return
			}
			// Success!
			log.log("Successfully re-established JSONL watch after %v", delay)
			fw.debouncer.Trigger()
			return
		}
	}
	log.log("Failed to re-establish JSONL watch after all retries")
}

// startPolling begins polling for file changes using a ticker.
func (fw *FileWatcher) startPolling(ctx context.Context, log daemonLogger) {
	log.log("Starting polling mode with %v interval", fw.pollInterval)
	ticker := time.NewTicker(fw.pollInterval)
	fw.wg.Add(1)
	go func() {
		defer fw.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				changed := false

				// Check JSONL file
				stat, err := os.Stat(fw.jsonlPath)
				if err != nil {
					if os.IsNotExist(err) {
						// File disappeared
						if fw.lastExists {
							fw.lastExists = false
							fw.lastModTime = time.Time{}
							fw.lastSize = 0
							log.log("File missing (polling): %s", fw.jsonlPath)
							changed = true
						}
					} else {
						log.log("Polling error: %v", err)
					}
				} else {
					// File exists
					if !fw.lastExists {
						// File appeared
						fw.lastExists = true
						fw.lastModTime = stat.ModTime()
						fw.lastSize = stat.Size()
						log.log("File appeared (polling): %s", fw.jsonlPath)
						changed = true
					} else if !stat.ModTime().Equal(fw.lastModTime) || stat.Size() != fw.lastSize {
						// File exists and existed before - check for changes
						fw.lastModTime = stat.ModTime()
						fw.lastSize = stat.Size()
						log.log("File change detected (polling): %s", fw.jsonlPath)
						changed = true
					}
				}

				// Check .git/HEAD for branch changes (only if git paths are available)
				if fw.gitHeadPath != "" {
					headStat, err := os.Stat(fw.gitHeadPath)
					if err != nil {
						if os.IsNotExist(err) {
							if fw.lastHeadExists {
								fw.lastHeadExists = false
								fw.lastHeadModTime = time.Time{}
								log.log("Git HEAD missing (polling): %s", fw.gitHeadPath)
								changed = true
							}
						}
						// Ignore other errors for HEAD - it's optional
					} else {
						// HEAD exists
						if !fw.lastHeadExists {
							// HEAD appeared
							fw.lastHeadExists = true
							fw.lastHeadModTime = headStat.ModTime()
							log.log("Git HEAD appeared (polling): %s", fw.gitHeadPath)
							changed = true
						} else if !headStat.ModTime().Equal(fw.lastHeadModTime) {
							// HEAD changed (branch switch)
							fw.lastHeadModTime = headStat.ModTime()
							log.log("Git HEAD change detected (polling): %s", fw.gitHeadPath)
							changed = true
						}
					}
				}

				if changed {
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
	// Wait for goroutines to finish before cleanup
	fw.wg.Wait()
	fw.debouncer.Cancel()
	if fw.watcher != nil {
		return fw.watcher.Close()
	}
	return nil
}
