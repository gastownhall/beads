//go:build cgo

package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func init() {
	RegisterBackend(configfile.BackendDolt, func(ctx context.Context, path string, opts Options) (storage.Storage, error) {
		// Only bootstrap in embedded mode - server mode has database on server
		if !opts.ServerMode {
			if err := bootstrapEmbeddedDolt(ctx, path, opts); err != nil {
				return nil, err
			}
		}

		store, err := dolt.New(ctx, &dolt.Config{
			Path:       path,
			Database:   opts.Database,
			ReadOnly:   opts.ReadOnly,
			ServerMode: opts.ServerMode,
			ServerHost: opts.ServerHost,
			ServerPort: opts.ServerPort,
			ServerUser: opts.ServerUser,
		})
		if err != nil {
			// If server mode failed with a connection error, fall back to embedded mode.
			// This preserves the 2-tier architecture: server when available, embedded when not.
			if opts.ServerMode && isServerConnectionError(err) {
				fmt.Fprintf(os.Stderr, "Warning: Dolt server at %s:%d is not reachable, falling back to embedded mode\n",
					opts.ServerHost, opts.ServerPort)

				// Run bootstrap for embedded mode (JSONL import if needed)
				if berr := bootstrapEmbeddedDolt(ctx, path, opts); berr != nil {
					return nil, berr
				}

				return dolt.New(ctx, &dolt.Config{
					Path:       path,
					Database:   opts.Database,
					ReadOnly:   opts.ReadOnly,
					ServerMode: false, // Fall back to embedded
				})
			}
			return nil, err
		}
		return store, nil
	})
}

// bootstrapEmbeddedDolt checks if a JSONL-to-Dolt bootstrap is needed and runs it if so.
func bootstrapEmbeddedDolt(ctx context.Context, path string, opts Options) error {
	// Path is the dolt subdirectory, parent is .beads directory
	beadsDir := filepath.Dir(path)

	bootstrapped, result, err := dolt.Bootstrap(ctx, dolt.BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    path,
		LockTimeout: opts.LockTimeout,
	})
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	if bootstrapped && result != nil {
		fmt.Fprintf(os.Stderr, "Bootstrapping Dolt from JSONL...\n")
		if len(result.ParseErrors) > 0 {
			fmt.Fprintf(os.Stderr, "  Skipped %d malformed lines (see above for details)\n", len(result.ParseErrors))
		}
		fmt.Fprintf(os.Stderr, "  Imported %d issues", result.IssuesImported)
		if result.IssuesSkipped > 0 {
			fmt.Fprintf(os.Stderr, ", skipped %d duplicates", result.IssuesSkipped)
		}
		fmt.Fprintf(os.Stderr, "\n  Dolt database ready\n")
	}
	return nil
}

// isServerConnectionError returns true if the error indicates the Dolt server
// is unreachable (connection refused, timeout, DNS failure, etc.).
func isServerConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "no such host") ||
		strings.Contains(errLower, "i/o timeout") ||
		strings.Contains(errLower, "connection reset") ||
		strings.Contains(errLower, "network is unreachable")
}
