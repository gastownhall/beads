package proxy

import (
	"context"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// defaultLogSizeCheckInterval is how often a running proxy checks the size
// of proxy.log and the server log it owns. A minute bounds the overshoot to
// what the writers append in a minute, and a stat per file per minute costs
// nothing.
const defaultLogSizeCheckInterval = time.Minute

// logSizeWatcher bounds proxy.log and, when set, the server log while the
// proxy runs. The startup rotation (forkExecChild, ListenAndServe) renames an
// oversized file before anything has it open; here every writer already
// holds its file open with O_APPEND (the proxy logger, the proxy child's
// stdout and stderr, the dolt output the backend forwards), so each file is
// copy-truncated in place (doltserver.CopyTruncateLogIfOversized) at the same
// BEADS_DOLT_LOG_MAX_BYTES ceiling. A failure is written to proxy.log and
// retried on the next tick; it never stops the proxy.
func (p *proxyServer) logSizeWatcher(ctx context.Context) error {
	interval := p.logSizeCheckInterval
	if interval <= 0 {
		interval = defaultLogSizeCheckInterval
	}
	paths := []string{filepath.Join(p.rootDir, LogFileName)}
	if p.serverLogPath != "" {
		paths = append(paths, p.serverLogPath)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			for _, path := range paths {
				rotated, err := doltserver.CopyTruncateLogIfOversized(path)
				switch {
				case err != nil:
					p.tracef("logSizeWatcher: %v", err)
				case rotated:
					p.tracef("logSizeWatcher: rotated %s to %s.1", path, path)
				}
			}
		}
	}
}
