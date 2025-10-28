package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher monitors JSONL and git ref changes using filesystem events.
type FileWatcher struct {
	watcher   *fsnotify.Watcher
	debouncer *Debouncer
	jsonlPath string
}

// NewFileWatcher creates a file watcher for the given JSONL path.
// onChanged is called when the file or git refs change, after debouncing.
func NewFileWatcher(jsonlPath string, onChanged func()) (*FileWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	fw := &FileWatcher{
		watcher:   watcher,
		jsonlPath: jsonlPath,
		debouncer: NewDebouncer(500*time.Millisecond, onChanged),
	}

	// Watch the JSONL file
	if err := watcher.Add(jsonlPath); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to watch JSONL: %w", err)
	}

	// Also watch .git/refs/heads for branch changes (best effort)
	gitRefsPath := filepath.Join(filepath.Dir(jsonlPath), "..", ".git", "refs", "heads")
	_ = watcher.Add(gitRefsPath) // Ignore error - not all setups have this

	return fw, nil
}

// Start begins monitoring filesystem events.
// Runs in background goroutine until context is canceled.
func (fw *FileWatcher) Start(ctx context.Context, log daemonLogger) {
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
					}
				}

				// Handle git ref changes
				if event.Op&fsnotify.Write != 0 && filepath.Dir(event.Name) != filepath.Dir(fw.jsonlPath) {
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

// Close stops the file watcher and releases resources.
func (fw *FileWatcher) Close() error {
	fw.debouncer.Cancel()
	return fw.watcher.Close()
}
