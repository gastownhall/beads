// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/metadata.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// LocalMetadataFileName holds the per-user local metadata this backend answers
// from disk instead of from the server (design D3/D4).
const LocalMetadataFileName = "http_local_metadata.json"

// localMetadata is the per-user state file beside the activation sidecar. It is
// small, rewritten whole, and 0600 like the sidecar.
type localMetadata struct {
	path string
	mu   sync.Mutex
}

func newLocalMetadata(beadsDir string) *localMetadata {
	if beadsDir == "" {
		return nil
	}
	return &localMetadata{path: filepath.Join(beadsDir, LocalMetadataFileName)}
}

func (l *localMetadata) load() (map[string]string, error) {
	data, err := os.ReadFile(l.path) // #nosec G304 - controlled path
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", LocalMetadataFileName, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", LocalMetadataFileName, err)
	}
	return m, nil
}

func (l *localMetadata) get(key string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.load()
	if err != nil {
		return "", err
	}
	return m[key], nil
}

func (l *localMetadata) set(key, value string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.load()
	if err != nil {
		return err
	}
	m[key] = value
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", LocalMetadataFileName, err)
	}
	if err := os.WriteFile(l.path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", LocalMetadataFileName, err)
	}
	return nil
}

// GetLocalMetadata is served from the per-user state file, not from the server
// (design D4, "Tips GetLocalMetadata/SetLocalMetadata"): tips-shown timestamps
// are per-user UI state and storing them server-side would be the wrong scope.
// A missing key is ("", nil), matching every other backend.
func (s *Store) GetLocalMetadata(_ context.Context, key string) (string, error) {
	if s.local == nil {
		return "", nil
	}
	return s.local.get(key)
}

// SetLocalMetadata writes the same per-user file. It must not error on a store
// with nowhere to write — cmd/bd defers a tip write to PersistentPostRunE and
// treats any error there as fatal (design D3), and an ephemeral `--server-url`
// workspace has no durable .beads to hold the file. Tips are suppressed for
// those invocations, so dropping the write loses nothing a user asked for.
func (s *Store) SetLocalMetadata(_ context.Context, key, value string) error {
	if s.local == nil {
		return nil
	}
	return s.local.set(key, value)
}

// GetMetadata answers the workspace-identity probe from the cached handshake
// (design D4, "validateWorkspaceIdentity"). Serving `_project_id` is what makes
// wrong-server protection actually enforce: cmd/bd swallows a store error here
// into "skip validation", so a refusal would silently disarm the check.
//
// Before the handshake there is nothing to compare, and ("", nil) is the
// documented "new or pre-identity database" answer that skips validation
// without claiming a mismatch. Every other key refuses: no wire operation
// exposes the metadata table, and answering "" for an unknown key would be a
// dropped read dressed as an empty one.
//
// A wrong-server handshake is the one error that must NOT collapse to that
// benign skip (ga-b8ddd.11, Option C): the mismatch carries the id the server
// actually owns, so handing that Got value back is what lets cmd/bd's
// validateWorkspaceIdentity compare it against the workspace's expected
// project id — the sidecar's ExpectProjectID for this backend, not
// metadata.json (bee-ghosttrack CHANGES_REQUESTED on #7288: metadata.json's
// project_id is not kept current across `bd connect`) — and FIRE the
// identity check — the write is refused, exit 1. What this path does NOT surface
// is the wire ProjectMismatchError's own text: only the Got id survives this
// method, so the server URL, database, repo root and recovery clause it carries
// are discarded here. cmd/bd renders the http-appropriate recovery separately,
// off the RemoteWorkspaceBackend capability (ga-b8ddd.16), rather than from
// this returned string. Any other handshake failure has no identity to offer,
// so it stays the ("", nil) "nothing to compare" skip the caller already treats
// as benign.
func (s *Store) GetMetadata(ctx context.Context, key string) (string, error) {
	if key != "_project_id" {
		return "", s.unsupported("GetMetadata")
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		var mismatch *wire.ProjectMismatchError
		if errors.As(err, &mismatch) {
			return mismatch.Got, nil
		}
		return "", nil
	}
	if snap == nil {
		return "", nil
	}
	return snap.ProjectId, nil
}
