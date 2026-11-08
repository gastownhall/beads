// Package pushstate reads .beads/push-state.json, the per-machine file bd
// records Dolt push outcomes in. The file lives beside the database instead
// of inside it so multi-machine setups do not merge-conflict over it
// (GH#2466); this package owns its schema so every reader and writer agrees
// on what the fields mean (GH#6289).
//
// last_push and last_commit are success watermarks: they only describe a
// push that reached the remote. A failed or timed-out attempt records
// last_attempt (which the auto-push debounce throttles on) and last_error
// (the scrubbed reason it failed) and leaves the success watermarks alone,
// so a failure can never read as a successful push. A successful push
// clears last_error. Older binaries that predate the attempt/error fields
// still read the file (unknown fields are dropped) but may advance
// last_push on failure; see docs/reference/configuration.md before
// mixing writer versions on one workspace.
package pushstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State is the parsed content of .beads/push-state.json.
type State struct {
	// LastPush is the RFC3339 time of the most recent push that reached
	// the remote.
	LastPush string `json:"last_push"`
	// LastCommit is the Dolt commit the most recent successful push
	// published.
	LastCommit string `json:"last_commit"`
	// LastAttempt is the RFC3339 time of the most recent push attempt,
	// successful or not. Auto-push debounces on it.
	LastAttempt string `json:"last_attempt,omitempty"`
	// LastError is the single-line, credential-free reason the most
	// recent attempt failed. Empty once a push succeeds.
	LastError string `json:"last_error,omitempty"`
}

// FileName is the push state file's name inside the beads directory.
const FileName = "push-state.json"

// Load reads the push state recorded under beadsDir. A missing file is not
// an error: it returns (nil, nil), the never-pushed state. A file that is
// present but unreadable or malformed returns the error, so callers can
// warn that local push history cannot be trusted rather than silently
// treating it as absent (GH#6289).
func Load(beadsDir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(beadsDir, FileName)) //nolint:gosec // path is a beads directory
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return &st, nil
}
